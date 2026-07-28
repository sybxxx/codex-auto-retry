package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeResumeRunner struct {
	mu      sync.Mutex
	jobs    []RetryJob
	result  DispatchResult
	err     error
	gate    chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (f *fakeResumeRunner) Resume(ctx context.Context, job RetryJob) (DispatchResult, error) {
	f.mu.Lock()
	f.jobs = append(f.jobs, job)
	f.mu.Unlock()
	if f.entered != nil {
		f.once.Do(func() { close(f.entered) })
	}
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return DispatchResult{}, ctx.Err()
		}
	}
	return f.result, f.err
}

func (f *fakeResumeRunner) snapshot() []RetryJob {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]RetryJob(nil), f.jobs...)
}

func successfulRunner() *fakeResumeRunner {
	return &fakeResumeRunner{result: DispatchResult{
		Outcome: outcomeDispatched,
		Action:  actionConversationContinue,
	}}
}

func TestDaemonPauseAndQueuedControls(t *testing.T) {
	d := newTestDaemon(t, isolatedConfig(filepath.Join(t.TempDir(), ".codex")), successfulRunner())
	threadID := "019f9d5d-9c82-75b1-b7c0-20a658af0423"
	now := time.Date(2026, 7, 27, 7, 0, 0, 0, time.UTC)
	d.state.Threads[threadID] = ThreadState{Pending: &PendingRetry{
		EventKey:     "event",
		FailedTurnID: "failed-turn",
		Class:        classServer,
		DueAt:        now.Add(time.Minute),
		Attempt:      1,
	}}
	if _, err := saveControlState(d.controlPath, true, now); err != nil {
		t.Fatal(err)
	}
	d.refreshControlsLocked(now)
	if !d.paused || len(d.dispatchDueLocked(now.Add(2*time.Minute))) != 0 {
		t.Fatal("paused watchdog dispatched a pending retry")
	}
	if _, err := saveControlState(d.controlPath, false, now); err != nil {
		t.Fatal(err)
	}
	if _, err := queueControlCommand(d.commandDir, commandRetryNow, threadID, now); err != nil {
		t.Fatal(err)
	}
	d.refreshControlsLocked(now)
	if d.paused || !d.state.Threads[threadID].Pending.DueAt.Equal(now) {
		t.Fatalf("retry-now command was not applied: paused=%v thread=%+v", d.paused, d.state.Threads[threadID])
	}
	if _, err := queueControlCommand(d.commandDir, commandCancelRetry, threadID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	d.refreshControlsLocked(now.Add(time.Second))
	if d.state.Threads[threadID].Pending != nil {
		t.Fatal("cancel command did not remove the pending retry")
	}
}

func TestDaemonStopsAfterConfiguredRetryLimitAndCanRestart(t *testing.T) {
	config := isolatedConfig(filepath.Join(t.TempDir(), ".codex"))
	config.MaxRetryAttempts = 2
	d := newTestDaemon(t, config, successfulRunner())
	threadID := "019f9d5d-9c82-75b1-b7c0-20a658af0423"
	now := time.Date(2026, 7, 28, 7, 0, 0, 0, time.UTC)
	first := failureScannedEvent(threadID, "failed-1", now)
	d.handleEventLocked(first, now)
	thread := d.state.Threads[threadID]
	if thread.Pending == nil || thread.Pending.Attempt != 1 || thread.Pending.MaxAttempts != 2 {
		t.Fatalf("first retry was not scheduled with the global limit: %+v", thread)
	}
	exhausted := failureScannedEvent(threadID, "failed-3", now.Add(time.Minute))
	d.scheduleFailureLocked(exhausted, "event-3", now.Add(time.Minute), thread, 3)
	thread = d.state.Threads[threadID]
	if thread.Pending != nil || thread.Awaiting != nil || thread.Stopped == nil ||
		thread.Stopped.Attempts != 2 || thread.ConsecutiveFailures != 2 {
		t.Fatalf("retry chain did not stop at its limit: %+v", thread)
	}
	d.applyControlCommandLocked(ControlCommand{
		Version:   currentControlVersion,
		Action:    commandRestartRetry,
		ThreadID:  threadID,
		CreatedAt: now.Add(2 * time.Minute),
	}, now.Add(2*time.Minute))
	thread = d.state.Threads[threadID]
	if thread.Stopped != nil || thread.Pending == nil || thread.Pending.Attempt != 1 ||
		thread.Pending.MaxAttempts != 2 {
		t.Fatalf("stopped retry was not restarted with a fresh budget: %+v", thread)
	}
}

func TestDaemonRestoresOnlyUnacknowledgedStartingRetriesOnStartup(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Now().UTC()
	state := newRuntimeState()
	startingID := "019f9d5d-9c82-75b1-b7c0-20a658af0423"
	runningID := "019f9d5d-9c82-75b1-b7c0-20a658af0424"
	state.Threads[startingID] = ThreadState{Awaiting: &AwaitingRetry{
		EventKey:          "starting-event",
		FailedTurnID:      "failed",
		Class:             classServer,
		Attempt:           2,
		MaxAttempts:       5,
		DispatchStartedAt: now.Add(-time.Minute),
	}}
	state.Threads[runningID] = ThreadState{Awaiting: &AwaitingRetry{
		EventKey:          "running-event",
		FailedTurnID:      "failed",
		RetryTurnID:       "retry-turn",
		Class:             classServer,
		Attempt:           1,
		MaxAttempts:       5,
		DispatchStartedAt: now.Add(-time.Minute),
	}}
	if err := writeJSONAtomic(filepath.Join(dataDir, "state.json"), state); err != nil {
		t.Fatal(err)
	}
	logger, err := newSafeLogger(filepath.Join(dataDir, "test.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	d, err := newDaemon(defaultConfig(), dataDir, logger, successfulRunner())
	if err != nil {
		t.Fatal(err)
	}
	starting := d.state.Threads[startingID]
	running := d.state.Threads[runningID]
	if starting.Awaiting != nil || starting.Pending == nil || starting.Pending.Attempt != 2 {
		t.Fatalf("stale starting retry was not restored to pending: %+v", starting)
	}
	if running.Awaiting == nil || running.Awaiting.RetryTurnID != "retry-turn" {
		t.Fatalf("acknowledged running retry was incorrectly rewritten: %+v", running)
	}
}

func TestDaemonLoweringRetryLimitStopsAnOverBudgetPendingRetry(t *testing.T) {
	config := isolatedConfig(filepath.Join(t.TempDir(), ".codex"))
	config.MaxRetryAttempts = 5
	d := newTestDaemon(t, config, successfulRunner())
	threadID := "019f9d5d-9c82-75b1-b7c0-20a658af0423"
	d.state.Threads[threadID] = ThreadState{Pending: &PendingRetry{
		EventKey: "event", FailedTurnID: "failed", Class: classServer,
		Attempt: 5, MaxAttempts: 5,
	}}
	config.MaxRetryAttempts = 3
	if err := writeJSONAtomic(filepath.Join(d.dataDir, "config.json"), config); err != nil {
		t.Fatal(err)
	}
	d.reloadConfigLocked()
	thread := d.state.Threads[threadID]
	if thread.Pending != nil || thread.Stopped == nil || thread.Stopped.Attempts != 3 ||
		thread.Stopped.MaxAttempts != 3 {
		t.Fatalf("lowered retry limit did not stop the over-budget retry cleanly: %+v", thread)
	}
}

func isolatedConfig(codexHome string) Config {
	cfg := defaultConfig()
	cfg.SessionRoots = []string{codexHome}
	cfg.IncludeDefaultHome = false
	cfg.IncludeCockpitHomes = false
	cfg.InitialDelaySeconds = 1
	cfg.MaxDelaySeconds = 10
	cfg.StartAckTimeoutSeconds = 10
	return cfg
}

func newTestDaemon(t *testing.T, cfg Config, runner resumeRunner) *daemon {
	t.Helper()
	dataDir := t.TempDir()
	logger, err := newSafeLogger(filepath.Join(dataDir, "test.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	d, err := newDaemon(cfg, dataDir, logger, runner)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDaemonBaselinesHistoryAndDispatchesNewFailure(t *testing.T) {
	codexHome := filepath.Join(t.TempDir(), ".codex")
	sessions := filepath.Join(codexHome, "sessions", "2026", "07", "26")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	threadID := "019f9d5d-9c82-75b1-b7c0-20a658af0423"
	rollout := filepath.Join(sessions, "rollout-2026-07-26T15-39-45-"+threadID+".jsonl")
	historical := makeEventLine(t, "2026-07-26T08:00:00Z", "task_complete", "old-turn", "HTTP 503")
	if err := os.WriteFile(rollout, historical, 0o600); err != nil {
		t.Fatal(err)
	}

	runner := successfulRunner()
	d := newTestDaemon(t, isolatedConfig(codexHome), runner)
	start := time.Now().UTC()
	d.startedAt = start
	if err := d.tick(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	if thread := daemonThreadSnapshot(d, threadID); thread.Pending != nil {
		t.Fatal("historical failure must be baselined")
	}

	appendLine(t, rollout, makeEventLine(t, start.Add(time.Second).Format(time.RFC3339Nano), "task_complete", "new-turn", "HTTP 503 Service Unavailable"))
	if err := d.tick(context.Background(), start.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	thread := daemonThreadSnapshot(d, threadID)
	if thread.Pending == nil || thread.Pending.Attempt != 1 {
		t.Fatalf("retry was not scheduled: %+v", thread)
	}
	if err := d.tick(context.Background(), start.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Second, func() bool { return len(runner.snapshot()) == 1 })
	d.waitForJobs()
	job := runner.snapshot()[0]
	if job.ThreadID != threadID || job.CodexHome != codexHome || job.RolloutPath != rollout {
		t.Fatalf("unexpected retry job: %+v", job)
	}
	thread = daemonThreadSnapshot(d, threadID)
	if thread.Awaiting == nil || thread.Awaiting.Action != actionConversationContinue {
		t.Fatalf("dispatched retry was not tracked: %+v", thread)
	}
}

func TestManualTaskCancelsPendingRetry(t *testing.T) {
	codexHome := filepath.Join(t.TempDir(), ".codex")
	sessions := filepath.Join(codexHome, "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	threadID := "019f9d46-2924-7a70-8ec9-83b19f5491a9"
	rollout := filepath.Join(sessions, "rollout-2026-07-26T15-14-09-"+threadID+".jsonl")
	if err := os.WriteFile(rollout, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	d := newTestDaemon(t, isolatedConfig(codexHome), successfulRunner())
	start := time.Now().UTC()
	d.startedAt = start
	if err := d.tick(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	appendLine(t, rollout, makeEventLine(t, start.Add(time.Second).Format(time.RFC3339Nano), "task_complete", "failed", "connection reset"))
	if err := d.tick(context.Background(), start.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	appendLine(t, rollout, makeEventLine(t, start.Add(3*time.Second).Format(time.RFC3339Nano), "task_started", "manual", nil))
	if err := d.tick(context.Background(), start.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	thread := daemonThreadSnapshot(d, threadID)
	if thread.Pending != nil || thread.Awaiting != nil || thread.ConsecutiveFailures != 0 {
		t.Fatalf("manual task did not cancel retry: %+v", thread)
	}
}

func TestPausedGoalSkipsProviderFailure(t *testing.T) {
	threadID := "019fa3f6-a793-78a3-8ae6-947340d954b2"
	d := newTestDaemon(t, isolatedConfig(t.TempDir()), successfulRunner())
	pausedAt := time.Date(2026, 7, 27, 15, 50, 53, 0, time.UTC)
	d.handleEventLocked(goalScannedEvent(threadID, "paused", pausedAt), pausedAt)
	failureAt := pausedAt.Add(69 * time.Second)
	d.handleEventLocked(failureScannedEvent(threadID, "failed-turn", failureAt), failureAt)

	thread := d.state.Threads[threadID]
	if !thread.GoalHeld || thread.GoalStatus != "paused" || thread.Pending != nil || thread.Awaiting != nil {
		t.Fatalf("paused goal was queued for retry: %+v", thread)
	}
}

func TestGoalPauseCancelsPendingRetry(t *testing.T) {
	threadID := "019fa3f6-a793-78a3-8ae6-947340d954b3"
	d := newTestDaemon(t, isolatedConfig(t.TempDir()), successfulRunner())
	now := time.Now().UTC()
	d.state.Threads[threadID] = ThreadState{Pending: &PendingRetry{
		EventKey: "failure", FailedTurnID: "failed-turn", FailedAt: now,
		Class: classServer, DueAt: now.Add(time.Minute), Attempt: 1,
	}}
	d.handleEventLocked(goalScannedEvent(threadID, "paused", now.Add(time.Second)), now.Add(time.Second))

	thread := d.state.Threads[threadID]
	if !thread.GoalHeld || thread.Pending != nil || thread.Awaiting != nil {
		t.Fatalf("goal pause did not cancel the countdown: %+v", thread)
	}
}

func TestGoalPauseCancelsRunningRetryAndIgnoresStaleResult(t *testing.T) {
	threadID := "019fa3f6-a793-78a3-8ae6-947340d954b4"
	runner := &fakeResumeRunner{
		result:  DispatchResult{Outcome: outcomeDispatched, Action: actionGoalResume},
		gate:    make(chan struct{}),
		entered: make(chan struct{}),
	}
	d := newTestDaemon(t, isolatedConfig(t.TempDir()), runner)
	now := time.Now().UTC()
	d.state.Threads[threadID] = ThreadState{Pending: &PendingRetry{
		EventKey: "failure", FailedTurnID: "failed-turn", FailedAt: now,
		Class: classServer, DueAt: now, Attempt: 1,
	}}
	jobs := d.dispatchDueLocked(now)
	if len(jobs) != 1 {
		t.Fatalf("expected one running retry, got %d", len(jobs))
	}
	d.wg.Add(1)
	go d.runJob(context.Background(), jobs[0])
	select {
	case <-runner.entered:
	case <-time.After(time.Second):
		t.Fatal("retry runner did not start")
	}

	d.mu.Lock()
	d.handleEventLocked(goalScannedEvent(threadID, "paused", now.Add(time.Second)), now.Add(time.Second))
	d.mu.Unlock()
	d.waitForJobs()

	thread := daemonThreadSnapshot(d, threadID)
	d.mu.Lock()
	activeCount := len(d.active)
	activeContextCount := len(d.activeCtx)
	d.mu.Unlock()
	if !thread.GoalHeld || thread.Pending != nil || thread.Awaiting != nil || activeCount != 0 || activeContextCount != 0 {
		t.Fatalf("paused running retry was not fully cancelled: thread=%+v active=%d contexts=%d", thread, activeCount, activeContextCount)
	}
}

func TestOnlyActiveGoalUpdateClearsPauseHold(t *testing.T) {
	threadID := "019fa3f6-a793-78a3-8ae6-947340d954b5"
	d := newTestDaemon(t, isolatedConfig(t.TempDir()), successfulRunner())
	now := time.Now().UTC()
	d.handleEventLocked(goalScannedEvent(threadID, "paused", now), now)
	d.handleEventLocked(scannedEvent{ThreadID: threadID, Event: RelevantEvent{
		Kind: "task_started", TurnID: "manual-turn", Timestamp: now.Add(time.Second),
	}}, now.Add(time.Second))
	if !d.state.Threads[threadID].GoalHeld {
		t.Fatal("a normal task start cleared the explicit goal pause")
	}

	d.handleEventLocked(goalScannedEvent(threadID, "active", now.Add(2*time.Second)), now.Add(2*time.Second))
	failureAt := now.Add(3 * time.Second)
	d.handleEventLocked(failureScannedEvent(threadID, "new-failure", failureAt), failureAt)
	thread := d.state.Threads[threadID]
	if thread.GoalHeld || thread.Pending == nil || !thread.Pending.FailedAt.Equal(failureAt) {
		t.Fatalf("active goal did not re-enable future retries: %+v", thread)
	}
}

func TestStaleActiveGoalUpdateCannotClearNewerPause(t *testing.T) {
	threadID := "019fa3f6-a793-78a3-8ae6-947340d954ba"
	d := newTestDaemon(t, isolatedConfig(t.TempDir()), successfulRunner())
	pausedAt := time.Now().UTC()
	d.handleEventLocked(goalScannedEvent(threadID, "paused", pausedAt), pausedAt)
	d.handleEventLocked(goalScannedEvent(threadID, "active", pausedAt.Add(-time.Minute)), pausedAt.Add(time.Second))
	thread := d.state.Threads[threadID]
	if !thread.GoalHeld || thread.GoalStatus != "paused" || !thread.GoalUpdatedAt.Equal(pausedAt) {
		t.Fatalf("stale active event cleared a newer pause: %+v", thread)
	}
}

func TestSameTimestampActiveGoalCannotClearPause(t *testing.T) {
	threadID := "019fa3f6-a793-78a3-8ae6-947340d954bb"
	d := newTestDaemon(t, isolatedConfig(t.TempDir()), successfulRunner())
	updatedAt := time.Now().UTC().Truncate(time.Second)
	d.handleEventLocked(goalScannedEvent(threadID, "paused", updatedAt), updatedAt)
	d.handleEventLocked(goalScannedEvent(threadID, "active", updatedAt), updatedAt.Add(time.Second))
	thread := d.state.Threads[threadID]
	if !thread.GoalHeld || thread.GoalStatus != "paused" {
		t.Fatalf("ambiguous same-timestamp active event cleared pause: %+v", thread)
	}
}

func TestLaterObservedActiveGoalCanClearSameSecondPause(t *testing.T) {
	threadID := "019fa3f6-a793-78a3-8ae6-947340d954bd"
	d := newTestDaemon(t, isolatedConfig(t.TempDir()), successfulRunner())
	updatedAt := time.Now().UTC().Truncate(time.Second)
	paused := goalScannedEvent(threadID, "paused", updatedAt)
	paused.Event.Timestamp = updatedAt.Add(100 * time.Millisecond)
	d.handleEventLocked(paused, paused.Event.Timestamp)
	active := goalScannedEvent(threadID, "active", updatedAt)
	active.Event.Timestamp = updatedAt.Add(200 * time.Millisecond)
	d.handleEventLocked(active, active.Event.Timestamp)
	thread := d.state.Threads[threadID]
	if thread.GoalHeld || thread.GoalStatus != "active" || !thread.GoalObservedAt.Equal(active.Event.Timestamp) {
		t.Fatalf("later explicit active event did not clear the pause: %+v", thread)
	}
}

func TestBlockedGoalBeforeFailureIsHeldForReview(t *testing.T) {
	threadID := "019fa3f6-a793-78a3-8ae6-947340d954b6"
	d := newTestDaemon(t, isolatedConfig(t.TempDir()), successfulRunner())
	blockedAt := time.Now().UTC()
	d.handleEventLocked(goalScannedEvent(threadID, "blocked", blockedAt), blockedAt)
	failureAt := blockedAt.Add(10 * time.Second)
	d.handleEventLocked(failureScannedEvent(threadID, "failed-after-review", failureAt), failureAt)
	thread := d.state.Threads[threadID]
	if !thread.GoalHeld || thread.Pending != nil {
		t.Fatalf("pre-existing blocked goal was mistaken for a provider interruption: %+v", thread)
	}
}

func TestProviderBlockedGoalIsRetryableInEitherEventOrder(t *testing.T) {
	for _, goalFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "goal_first", false: "failure_first"}[goalFirst], func(t *testing.T) {
			threadID := map[bool]string{
				true:  "019fa3f6-a793-78a3-8ae6-947340d954b7",
				false: "019fa3f6-a793-78a3-8ae6-947340d954b8",
			}[goalFirst]
			d := newTestDaemon(t, isolatedConfig(t.TempDir()), successfulRunner())
			failureAt := time.Now().UTC()
			goalEvent := goalScannedEvent(threadID, "blocked", failureAt.Add(time.Second))
			failureEvent := failureScannedEvent(threadID, "provider-failure", failureAt)
			if goalFirst {
				d.handleEventLocked(goalEvent, failureAt.Add(time.Second))
				d.handleEventLocked(failureEvent, failureAt.Add(2*time.Second))
			} else {
				d.handleEventLocked(failureEvent, failureAt)
				d.handleEventLocked(goalEvent, failureAt.Add(time.Second))
			}
			thread := d.state.Threads[threadID]
			if thread.GoalHeld || thread.Pending == nil {
				t.Fatalf("provider-caused blocked goal was not retained for retry: %+v", thread)
			}
		})
	}
}

func TestGoalPauseHoldSurvivesDaemonRestart(t *testing.T) {
	dataDir := t.TempDir()
	threadID := "019fa3f6-a793-78a3-8ae6-947340d954b9"
	pausedAt := time.Now().UTC()
	state := newRuntimeState()
	state.Initialized = true
	state.Threads[threadID] = ThreadState{
		GoalStatus: "paused", GoalUpdatedAt: pausedAt, GoalHeld: true,
	}
	if err := writeJSONAtomic(filepath.Join(dataDir, "state.json"), state); err != nil {
		t.Fatal(err)
	}
	logger, err := newSafeLogger(filepath.Join(dataDir, "restart.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	d, err := newDaemon(isolatedConfig(t.TempDir()), dataDir, logger, successfulRunner())
	if err != nil {
		t.Fatal(err)
	}
	thread := d.state.Threads[threadID]
	if !thread.GoalHeld || thread.GoalStatus != "paused" || !thread.GoalUpdatedAt.Equal(pausedAt) {
		t.Fatalf("goal pause was lost across restart: %+v", thread)
	}
}

func TestOnlyMatchingRetryTurnCanRecoverChain(t *testing.T) {
	codexHome := filepath.Join(t.TempDir(), ".codex")
	sessions := filepath.Join(codexHome, "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	threadID := "019f9d92-f91d-70a3-8fbd-428755f70729"
	rollout := filepath.Join(sessions, "rollout-2026-07-26T16-38-02-"+threadID+".jsonl")
	if err := os.WriteFile(rollout, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	runner := successfulRunner()
	d := newTestDaemon(t, isolatedConfig(codexHome), runner)
	start := time.Now().UTC()
	d.startedAt = start
	if err := d.tick(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	appendLine(t, rollout, makeEventLine(t, start.Add(time.Second).Format(time.RFC3339Nano), "task_complete", "failed-turn", "HTTP 503"))
	if err := d.tick(context.Background(), start.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := d.tick(context.Background(), start.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	d.waitForJobs()

	appendLine(t, rollout, makeEventLine(t, start.Add(5*time.Second).Format(time.RFC3339Nano), "task_complete", "unrelated-turn", nil))
	if err := d.tick(context.Background(), start.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	if thread := daemonThreadSnapshot(d, threadID); thread.Awaiting == nil {
		t.Fatal("unrelated successful completion cleared the retry chain")
	}

	appendLine(t, rollout, makeEventLine(t, start.Add(7*time.Second).Format(time.RFC3339Nano), "task_started", "retry-turn", nil))
	appendLine(t, rollout, makeEventLine(t, start.Add(8*time.Second).Format(time.RFC3339Nano), "task_complete", "retry-turn", nil))
	if err := d.tick(context.Background(), start.Add(9*time.Second)); err != nil {
		t.Fatal(err)
	}
	thread := daemonThreadSnapshot(d, threadID)
	if thread.Pending != nil || thread.Awaiting != nil || thread.ConsecutiveFailures != 0 {
		t.Fatalf("matching retry completion did not recover the chain: %+v", thread)
	}
}

func TestDifferentTaskAfterRetryAcknowledgementCancelsAutomation(t *testing.T) {
	threadID := "019f9dd2-f4ed-7b12-abee-d790fb22701e"
	start := time.Now().UTC()
	cfg := isolatedConfig(t.TempDir())
	d := newTestDaemon(t, cfg, successfulRunner())
	d.state.Threads[threadID] = ThreadState{Awaiting: &AwaitingRetry{
		EventKey:          "failed-event",
		FailedTurnID:      "failed-turn",
		RetryTurnID:       "auto-turn",
		Attempt:           1,
		DispatchStartedAt: start,
		StartDeadline:     start.Add(30 * time.Second),
	}}
	d.handleEventLocked(scannedEvent{ThreadID: threadID, Event: RelevantEvent{
		Kind: "task_started", TurnID: "manual-turn", Timestamp: start.Add(time.Second),
	}}, start.Add(time.Second))
	thread := d.state.Threads[threadID]
	if thread.Awaiting != nil || thread.ConsecutiveFailures != 0 {
		t.Fatalf("different task did not cancel automation: %+v", thread)
	}
}

func TestUserActivityReschedulesDispatchedRetry(t *testing.T) {
	threadID := "019f9dd7-2df0-73a2-a8cc-c876c28c17a9"
	cfg := isolatedConfig(t.TempDir())
	runner := &fakeResumeRunner{result: DispatchResult{Outcome: outcomeUserActive, Reason: "draft_present"}}
	d := newTestDaemon(t, cfg, runner)
	now := time.Now().UTC()
	d.state.Threads[threadID] = ThreadState{
		ConsecutiveFailures: 1,
		Pending: &PendingRetry{
			EventKey: "event", FailedTurnID: "failed", Class: classServer,
			DueAt: now, Attempt: 1,
		},
	}
	jobs := d.dispatchDueLocked(now)
	if len(jobs) != 1 {
		t.Fatalf("expected one job, got %d", len(jobs))
	}
	d.wg.Add(1)
	go d.runJob(context.Background(), jobs[0])
	d.waitForJobs()
	thread := daemonThreadSnapshot(d, threadID)
	if thread.Pending == nil || thread.Pending.Attempt != 1 || thread.Pending.DispatchFailures != 1 ||
		thread.Awaiting != nil || thread.ConsecutiveFailures != 1 {
		t.Fatalf("user activity removed the failed task instead of delaying it: %+v", thread)
	}
}

func TestControllerFailureReschedulesSameProviderAttempt(t *testing.T) {
	threadID := "019f9e7a-022b-77d2-bccc-5394d292caf3"
	cfg := isolatedConfig(t.TempDir())
	runner := &fakeResumeRunner{err: errors.New("controller unavailable")}
	d := newTestDaemon(t, cfg, runner)
	now := time.Now().UTC()
	d.state.Threads[threadID] = ThreadState{
		ConsecutiveFailures: 1,
		Pending: &PendingRetry{
			EventKey: "event", FailedTurnID: "failed", Class: classServer,
			DueAt: now, Attempt: 1,
		},
	}
	jobs := d.dispatchDueLocked(now)
	d.wg.Add(1)
	go d.runJob(context.Background(), jobs[0])
	d.waitForJobs()
	thread := daemonThreadSnapshot(d, threadID)
	if thread.Pending == nil || thread.Pending.Attempt != 1 || thread.Pending.DispatchFailures != 1 || thread.Awaiting != nil {
		t.Fatalf("controller failure was not safely rescheduled: %+v", thread)
	}
}

func TestParentOwnedSubagentDoesNotRemainInIndependentRetryQueue(t *testing.T) {
	threadID := "019fa17c-1e5f-7dc3-bae5-e84666ffc201"
	cfg := isolatedConfig(t.TempDir())
	runner := &fakeResumeRunner{result: DispatchResult{
		Outcome: outcomeNotApplicable,
		Reason:  "subagent_owned_by_parent",
	}}
	d := newTestDaemon(t, cfg, runner)
	now := time.Now().UTC()
	d.state.Threads[threadID] = ThreadState{
		ConsecutiveFailures: 1,
		Pending: &PendingRetry{
			EventKey: "event", FailedTurnID: "failed", Class: classServer,
			DueAt: now, Attempt: 1,
		},
	}
	jobs := d.dispatchDueLocked(now)
	d.wg.Add(1)
	go d.runJob(context.Background(), jobs[0])
	d.waitForJobs()
	thread := daemonThreadSnapshot(d, threadID)
	if thread.Pending != nil || thread.Awaiting != nil || thread.ConsecutiveFailures != 0 {
		t.Fatalf("parent-owned subagent remained independently queued: %+v", thread)
	}
}

func TestLiveRendererGoalHoldPersistsInDaemonState(t *testing.T) {
	threadID := "019fa3f6-a793-78a3-8ae6-947340d954bc"
	cfg := isolatedConfig(t.TempDir())
	runner := &fakeResumeRunner{result: DispatchResult{
		Outcome: outcomeNotApplicable,
		Reason:  "goal_paused",
	}}
	d := newTestDaemon(t, cfg, runner)
	now := time.Now().UTC()
	d.state.Threads[threadID] = ThreadState{Pending: &PendingRetry{
		EventKey: "event", FailedTurnID: "failed", FailedAt: now, Class: classServer,
		DueAt: now, Attempt: 1,
	}}
	jobs := d.dispatchDueLocked(now)
	d.wg.Add(1)
	go d.runJob(context.Background(), jobs[0])
	d.waitForJobs()
	thread := daemonThreadSnapshot(d, threadID)
	if !thread.GoalHeld || thread.GoalStatus != "paused" || thread.Pending != nil || thread.Awaiting != nil {
		t.Fatalf("live renderer hold was not persisted: %+v", thread)
	}
}

func TestMissingStartAcknowledgementReschedules(t *testing.T) {
	threadID := "019f9cfe-4b7f-7223-a298-e60f4c25f14a"
	cfg := isolatedConfig(t.TempDir())
	d := newTestDaemon(t, cfg, successfulRunner())
	now := time.Now().UTC()
	d.state.Threads[threadID] = ThreadState{
		ConsecutiveFailures: 1,
		Awaiting: &AwaitingRetry{
			EventKey: "event", FailedTurnID: "failed", Class: classServer,
			Attempt: 1, Action: actionGoalResume, DispatchStartedAt: now.Add(-time.Minute),
			StartDeadline: now.Add(-time.Second),
		},
	}
	d.expireUnacknowledgedLocked(now)
	thread := d.state.Threads[threadID]
	if thread.Pending == nil || thread.Pending.Attempt != 1 || thread.Pending.DispatchFailures != 1 || thread.Awaiting != nil {
		t.Fatalf("missing acknowledgement was not rescheduled: %+v", thread)
	}
}

func TestDispatchHonorsParallelLimitAndKeepsThreadsIndependent(t *testing.T) {
	cfg := isolatedConfig(t.TempDir())
	cfg.MaxParallelRetries = 2
	d := newTestDaemon(t, cfg, successfulRunner())
	now := time.Now().UTC()
	for index, threadID := range []string{
		"019f9d33-41f6-70a3-ab98-df2196cb7aa7",
		"019f9d47-7e70-7120-9b69-776d8d25ae0e",
		"019f9d58-e254-7be2-91f8-a7df30958a12",
	} {
		d.state.Threads[threadID] = ThreadState{Pending: &PendingRetry{
			EventKey: threadID, FailedTurnID: "failed", Class: classServer,
			DueAt: now.Add(-time.Duration(index) * time.Second), Attempt: 1,
		}}
	}
	jobs := d.dispatchDueLocked(now)
	if len(jobs) != 2 || len(d.active) != 2 {
		t.Fatalf("parallel retry slots were not filled: jobs=%d active=%d", len(jobs), len(d.active))
	}
	if more := d.dispatchDueLocked(now); len(more) != 0 {
		t.Fatalf("parallel limit was exceeded: %+v", more)
	}
	delete(d.active, jobs[0].ThreadID)
	more := d.dispatchDueLocked(now)
	if len(more) != 1 || more[0].ThreadID == jobs[0].ThreadID || more[0].ThreadID == jobs[1].ThreadID {
		t.Fatalf("third task did not retain its independent queue entry: %+v", more)
	}
}

func TestHeldGoalCannotDispatchPersistedPendingRetry(t *testing.T) {
	threadID := "019fa3f6-a793-78a3-8ae6-947340d954be"
	d := newTestDaemon(t, isolatedConfig(t.TempDir()), successfulRunner())
	now := time.Now().UTC()
	d.state.Threads[threadID] = ThreadState{
		GoalStatus: "paused", GoalUpdatedAt: now.Add(-time.Minute), GoalHeld: true,
		Pending: &PendingRetry{
			EventKey: "stale-event", FailedTurnID: "failed", FailedAt: now.Add(-time.Second),
			Class: classServer, DueAt: now, Attempt: 1,
		},
	}
	if jobs := d.dispatchDueLocked(now); len(jobs) != 0 {
		t.Fatalf("held goal dispatched a persisted retry: %+v", jobs)
	}
	thread := d.state.Threads[threadID]
	if thread.Pending != nil || thread.Awaiting != nil || !thread.GoalHeld {
		t.Fatalf("held goal did not clear stale queue state: %+v", thread)
	}
}

func TestFailureWrittenWhileStoppedIsProcessedAfterRestart(t *testing.T) {
	dataDir := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), ".codex")
	sessions := filepath.Join(codexHome, "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	threadID := "019f9cc4-db3f-7112-9161-9767c0f5b99f"
	rollout := filepath.Join(sessions, "rollout-2026-07-26T12-52-54-"+threadID+".jsonl")
	initial := makeEventLine(t, "2026-07-26T08:00:00Z", "task_started", "turn-a", nil)
	if err := os.WriteFile(rollout, initial, 0o600); err != nil {
		t.Fatal(err)
	}
	state := newRuntimeState()
	state.Initialized = true
	state.Files[strings.ToLower(filepath.Clean(rollout))] = FileCursor{Offset: int64(len(initial)), LastSeen: time.Now().Add(-time.Minute)}
	if err := writeJSONAtomic(filepath.Join(dataDir, "state.json"), state); err != nil {
		t.Fatal(err)
	}
	oldFailureTime := time.Now().UTC().Add(-10 * time.Minute)
	appendLine(t, rollout, makeEventLine(t, oldFailureTime.Format(time.RFC3339Nano), "task_complete", "turn-a", "HTTP 502 Bad Gateway"))

	logger, _ := newSafeLogger(filepath.Join(dataDir, "test.log"))
	defer logger.Close()
	d, _ := newDaemon(isolatedConfig(codexHome), dataDir, logger, successfulRunner())
	start := time.Now().UTC()
	d.startedAt = start
	if err := d.tick(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	if daemonThreadSnapshot(d, threadID).Pending == nil {
		t.Fatal("failure written while stopped was not scheduled after restart")
	}
}

func TestMirroredHistoryDoesNotCancelPendingRetry(t *testing.T) {
	rootA := filepath.Join(t.TempDir(), "home-a")
	rootB := filepath.Join(t.TempDir(), "home-b")
	for _, root := range []string{rootA, rootB} {
		if err := os.MkdirAll(filepath.Join(root, "sessions"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	threadID := "019f9cd9-2418-7f32-91a2-988ca910a213"
	fileName := "rollout-2026-07-26T13-15-04-" + threadID + ".jsonl"
	original := filepath.Join(rootA, "sessions", fileName)
	if err := os.WriteFile(original, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := isolatedConfig(rootA)
	cfg.SessionRoots = []string{rootA, rootB}
	d := newTestDaemon(t, cfg, successfulRunner())
	start := time.Now().UTC()
	d.startedAt = start
	if err := d.tick(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	oldStart := start.Add(-time.Minute)
	oldFailure := start.Add(-30 * time.Second)
	startLine := makeEventLine(t, oldStart.Format(time.RFC3339Nano), "task_started", "turn-a", nil)
	failureLine := makeEventLine(t, oldFailure.Format(time.RFC3339Nano), "task_complete", "turn-a", "HTTP 503")
	appendLine(t, original, failureLine)
	if err := d.tick(context.Background(), start.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if daemonThreadSnapshot(d, threadID).Pending == nil {
		t.Fatal("original failure was not scheduled")
	}
	mirror := filepath.Join(rootB, "sessions", fileName)
	if err := os.WriteFile(mirror, append(startLine, failureLine...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.tick(context.Background(), start.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	thread := daemonThreadSnapshot(d, threadID)
	if thread.Pending == nil && thread.Awaiting == nil {
		t.Fatal("mirrored history cancelled the pending retry")
	}
}

func daemonThreadSnapshot(d *daemon, threadID string) ThreadState {
	d.mu.Lock()
	defer d.mu.Unlock()
	thread := d.state.Threads[threadID]
	if thread.Pending != nil {
		pending := *thread.Pending
		thread.Pending = &pending
	}
	if thread.Awaiting != nil {
		awaiting := *thread.Awaiting
		thread.Awaiting = &awaiting
	}
	return thread
}

func appendLine(t *testing.T, path string, line []byte) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write(line); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}

func goalScannedEvent(threadID, status string, updatedAt time.Time) scannedEvent {
	return scannedEvent{ThreadID: threadID, Event: RelevantEvent{
		Kind: "thread_goal_updated", Timestamp: updatedAt,
		GoalStatus: status, GoalUpdatedAt: updatedAt,
	}}
}

func failureScannedEvent(threadID, turnID string, failedAt time.Time) scannedEvent {
	return scannedEvent{ThreadID: threadID, Event: RelevantEvent{
		Kind: "task_complete", TurnID: turnID, Timestamp: failedAt,
		ErrorText: "HTTP 503 Service Unavailable",
	}}
}
