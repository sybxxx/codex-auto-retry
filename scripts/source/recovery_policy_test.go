package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestHeldGoalAllowsRetryForLaterExternalTurn(t *testing.T) {
	threadID := "019fa3f6-a793-78a3-8ae6-947340d954b2"
	d := newTestDaemon(t, isolatedConfig(t.TempDir()), successfulRunner())
	pausedAt := time.Date(2026, 7, 29, 3, 10, 0, 0, time.UTC)
	d.handleEventLocked(goalScannedEvent(threadID, "paused", pausedAt), pausedAt)
	startedAt := pausedAt.Add(10 * time.Second)
	d.handleEventLocked(scannedEvent{ThreadID: threadID, Event: RelevantEvent{
		Kind: "task_started", TurnID: "user-turn", Timestamp: startedAt,
	}}, startedAt)
	failureAt := startedAt.Add(20 * time.Second)
	d.handleEventLocked(failureScannedEvent(threadID, "user-turn", failureAt), failureAt)

	thread := d.state.Threads[threadID]
	if !thread.GoalHeld || thread.Pending == nil || !thread.Pending.OriginTurnStartedAt.Equal(startedAt) {
		t.Fatalf("later external turn was not queued while preserving the goal hold: %+v", thread)
	}
	jobs := d.dispatchDueLocked(failureAt.Add(10 * time.Second))
	if len(jobs) != 1 || !jobs[0].OriginTurnStartedAt.Equal(startedAt) || !d.state.Threads[threadID].GoalHeld {
		t.Fatalf("held-goal conversation retry was not dispatched independently: jobs=%+v thread=%+v", jobs, d.state.Threads[threadID])
	}
}

func TestPauseAfterExternalTurnCancelsConversationRetry(t *testing.T) {
	threadID := "019fa3f6-a793-78a3-8ae6-947340d954b3"
	d := newTestDaemon(t, isolatedConfig(t.TempDir()), successfulRunner())
	startedAt := time.Now().UTC()
	d.handleEventLocked(scannedEvent{ThreadID: threadID, Event: RelevantEvent{
		Kind: "task_started", TurnID: "user-turn", Timestamp: startedAt,
	}}, startedAt)
	failureAt := startedAt.Add(10 * time.Second)
	d.handleEventLocked(failureScannedEvent(threadID, "user-turn", failureAt), failureAt)
	d.handleEventLocked(goalScannedEvent(threadID, "paused", failureAt.Add(time.Second)), failureAt.Add(time.Second))

	thread := d.state.Threads[threadID]
	if !thread.GoalHeld || thread.Pending != nil || thread.Awaiting != nil {
		t.Fatalf("a pause after the user turn did not cancel its retry: %+v", thread)
	}
}

func TestVisibleProgressResetsOnlyConsecutiveRetryCount(t *testing.T) {
	config := isolatedConfig(t.TempDir())
	config.MaxRecoveryAttempts = 15
	config.MaxConsecutiveRetries = 2
	config.InitialDelaySeconds = 5
	config.MaxDelaySeconds = 60
	d := newTestDaemon(t, config, successfulRunner())
	threadID := "019fa94e-0103-7183-b405-36bd307b6db2"
	now := time.Now().UTC()
	d.handleEventLocked(failureScannedEvent(threadID, "initial", now), now)

	first := d.state.Threads[threadID].Pending
	if first == nil || first.Attempt != 1 || first.ConsecutiveRetry != 1 {
		t.Fatalf("initial recovery counters are incorrect: %+v", first)
	}
	setRunningRetry(d, threadID, first, "retry-1", false)
	d.handleEventLocked(scannedEvent{ThreadID: threadID, Event: RelevantEvent{
		Kind: "task_progress", Timestamp: now.Add(30 * time.Second),
	}}, now.Add(30*time.Second))
	d.handleEventLocked(failureScannedEvent(threadID, "retry-1", now.Add(time.Minute)), now.Add(time.Minute))
	second := d.state.Threads[threadID].Pending
	if second == nil || second.Attempt != 2 || second.ConsecutiveRetry != 1 ||
		second.DueAt.Sub(now.Add(time.Minute)) != 5*time.Second {
		t.Fatalf("visible progress did not reset only the consecutive count and delay: %+v", second)
	}

	setRunningRetry(d, threadID, second, "retry-2", false)
	d.handleEventLocked(failureScannedEvent(threadID, "retry-2", now.Add(2*time.Minute)), now.Add(2*time.Minute))
	third := d.state.Threads[threadID].Pending
	if third == nil || third.Attempt != 3 || third.ConsecutiveRetry != 2 ||
		third.DueAt.Sub(now.Add(2*time.Minute)) != 10*time.Second {
		t.Fatalf("no-progress retry did not increase the consecutive count and delay: %+v", third)
	}
}

func TestProgressOutsideCorrelatedRetryDoesNotResetCounter(t *testing.T) {
	threadID := "019fa94e-0103-7183-b405-36bd307b6db5"
	now := time.Now().UTC()
	d := newTestDaemon(t, isolatedConfig(t.TempDir()), successfulRunner())
	d.state.Threads[threadID] = ThreadState{
		LastStartedTurnID: "manual-turn", LastStartedAt: now,
	}
	d.handleEventLocked(scannedEvent{ThreadID: threadID, Event: RelevantEvent{
		Kind: "task_progress", Timestamp: now.Add(time.Second),
	}}, now.Add(time.Second))
	if d.state.Threads[threadID].CurrentTurnProgress {
		t.Fatal("progress from a non-retry turn changed the consecutive retry state")
	}
}

func TestConsecutiveNoProgressLimitStopsBeforeRecoveryBudget(t *testing.T) {
	config := isolatedConfig(t.TempDir())
	config.MaxRecoveryAttempts = 15
	config.MaxConsecutiveRetries = 2
	d := newTestDaemon(t, config, successfulRunner())
	threadID := "019fa94e-0103-7183-b405-36bd307b6db4"
	now := time.Now().UTC()
	d.handleEventLocked(failureScannedEvent(threadID, "initial", now), now)
	first := d.state.Threads[threadID].Pending
	setRunningRetry(d, threadID, first, "retry-1", false)
	d.handleEventLocked(failureScannedEvent(threadID, "retry-1", now.Add(time.Minute)), now.Add(time.Minute))
	second := d.state.Threads[threadID].Pending
	setRunningRetry(d, threadID, second, "retry-2", false)
	d.handleEventLocked(failureScannedEvent(threadID, "retry-2", now.Add(2*time.Minute)), now.Add(2*time.Minute))

	stopped := d.state.Threads[threadID].Stopped
	if stopped == nil || stopped.Reason != "consecutive_retry_limit" ||
		stopped.Attempts != 2 || stopped.MaxAttempts != 15 ||
		stopped.ConsecutiveRetries != 2 || stopped.MaxConsecutive != 2 {
		t.Fatalf("consecutive no-progress guard did not stop independently: %+v", stopped)
	}
}

func TestRecoveryBudgetStillStopsRetriesThatAlwaysMakeProgress(t *testing.T) {
	config := isolatedConfig(t.TempDir())
	config.MaxRecoveryAttempts = 2
	config.MaxConsecutiveRetries = 2
	d := newTestDaemon(t, config, successfulRunner())
	threadID := "019fa94e-0103-7183-b405-36bd307b6db3"
	now := time.Now().UTC()
	d.handleEventLocked(failureScannedEvent(threadID, "initial", now), now)
	first := d.state.Threads[threadID].Pending
	setRunningRetry(d, threadID, first, "retry-1", true)
	d.handleEventLocked(failureScannedEvent(threadID, "retry-1", now.Add(time.Minute)), now.Add(time.Minute))
	second := d.state.Threads[threadID].Pending
	setRunningRetry(d, threadID, second, "retry-2", true)
	d.handleEventLocked(failureScannedEvent(threadID, "retry-2", now.Add(2*time.Minute)), now.Add(2*time.Minute))

	stopped := d.state.Threads[threadID].Stopped
	if stopped == nil || stopped.Reason != "recovery_attempt_limit" || stopped.Attempts != 2 || stopped.ConsecutiveRetries != 0 {
		t.Fatalf("recovery budget did not stop a chain with repeated partial progress: %+v", stopped)
	}
}

func TestActiveGoalNativeEmptyTurnsStayInOneRecoveryChain(t *testing.T) {
	config := isolatedConfig(t.TempDir())
	config.MaxRecoveryAttempts = 10
	config.MaxConsecutiveRetries = 5
	d := newTestDaemon(t, config, successfulRunner())
	threadID := "019fa94e-0103-7183-b405-36bd307b6dc0"
	now := time.Now().UTC()
	d.handleEventLocked(goalScannedEvent(threadID, "active", now), now)

	firstFailure := now.Add(time.Second)
	d.handleEventLocked(emptyCompletionScannedEvent(threadID, "goal-turn-0", firstFailure), firstFailure)
	d.handleEventLocked(scannedEvent{ThreadID: threadID, Event: RelevantEvent{
		Kind: "task_started", TurnID: "goal-turn-1", Timestamp: firstFailure.Add(10 * time.Millisecond),
	}}, firstFailure.Add(10*time.Millisecond))
	thread := d.state.Threads[threadID]
	if thread.Pending != nil || thread.Awaiting == nil || thread.Awaiting.Action != actionGoalActive ||
		thread.Awaiting.Attempt != 1 || thread.Awaiting.ConsecutiveRetry != 1 {
		t.Fatalf("native goal turn did not adopt the first empty-response retry: %+v", thread)
	}

	secondFailure := firstFailure.Add(5 * time.Second)
	d.handleEventLocked(emptyCompletionScannedEvent(threadID, "goal-turn-1", secondFailure), secondFailure)
	thread = d.state.Threads[threadID]
	if thread.Pending == nil || thread.Pending.Attempt != 2 || thread.Pending.ConsecutiveRetry != 2 ||
		thread.RecoveryAttempts != 2 || thread.ConsecutiveRetries != 2 {
		t.Fatalf("native goal failure reset the recovery counters: %+v", thread)
	}
	d.handleEventLocked(scannedEvent{ThreadID: threadID, Event: RelevantEvent{
		Kind: "task_started", TurnID: "goal-turn-2", Timestamp: secondFailure.Add(10 * time.Millisecond),
	}}, secondFailure.Add(10*time.Millisecond))
	thread = d.state.Threads[threadID]
	if thread.Awaiting == nil || thread.Awaiting.RetryTurnID != "goal-turn-2" || thread.Awaiting.Attempt != 2 {
		t.Fatalf("second native goal turn left the recovery chain: %+v", thread)
	}
}

func TestLaterUserInputCancelsAdoptedNativeGoalChain(t *testing.T) {
	threadID := "019fa94e-0103-7183-b405-36bd307b6dc1"
	now := time.Now().UTC()
	d := newTestDaemon(t, isolatedConfig(t.TempDir()), successfulRunner())
	d.handleEventLocked(goalScannedEvent(threadID, "active", now), now)
	failureAt := now.Add(time.Second)
	d.handleEventLocked(emptyCompletionScannedEvent(threadID, "goal-turn-0", failureAt), failureAt)
	startedAt := failureAt.Add(10 * time.Millisecond)
	d.handleEventLocked(scannedEvent{ThreadID: threadID, Event: RelevantEvent{
		Kind: "task_started", TurnID: "goal-turn-1", Timestamp: startedAt,
	}}, startedAt)
	d.handleEventLocked(scannedEvent{ThreadID: threadID, Event: RelevantEvent{
		Kind: "task_user_input", Timestamp: startedAt.Add(2 * time.Second),
	}}, startedAt.Add(2*time.Second))
	thread := d.state.Threads[threadID]
	if thread.Pending != nil || thread.Awaiting != nil || thread.Stopped != nil ||
		thread.RecoveryAttempts != 0 || thread.ConsecutiveRetries != 0 {
		t.Fatalf("later explicit user input did not cancel automatic recovery: %+v", thread)
	}
}

func TestExplicitUserInputCancelsDispatchedGoalResume(t *testing.T) {
	threadID := "019fa94e-0103-7183-b405-36bd307b6dc4"
	now := time.Now().UTC()
	d := newTestDaemon(t, isolatedConfig(t.TempDir()), successfulRunner())
	d.state.Threads[threadID] = ThreadState{
		LastStartedTurnID:  "goal-resume-turn",
		RecoveryAttempts:   2,
		ConsecutiveRetries: 2,
		Awaiting: &AwaitingRetry{
			EventKey: "goal-resume", RetryTurnID: "goal-resume-turn",
			Class: classEmptyResponse, Action: actionGoalResume,
			Attempt: 2, ConsecutiveRetry: 2, StartedAt: now,
		},
	}
	d.handleEventLocked(scannedEvent{ThreadID: threadID, Event: RelevantEvent{
		Kind: "task_user_input", Timestamp: now.Add(time.Second),
	}}, now.Add(time.Second))
	thread := d.state.Threads[threadID]
	if thread.Awaiting != nil || thread.Pending != nil || thread.RecoveryAttempts != 0 || thread.ConsecutiveRetries != 0 {
		t.Fatalf("explicit user input did not supersede a dispatched goal continuation: %+v", thread)
	}
}

func TestProgrammaticUserMarkerDoesNotCancelConversationRetry(t *testing.T) {
	threadID := "019fa94e-0103-7183-b405-36bd307b6dc5"
	now := time.Now().UTC()
	d := newTestDaemon(t, isolatedConfig(t.TempDir()), successfulRunner())
	d.state.Threads[threadID] = ThreadState{
		LastStartedTurnID:  "conversation-retry-turn",
		RecoveryAttempts:   2,
		ConsecutiveRetries: 2,
		Awaiting: &AwaitingRetry{
			EventKey: "conversation-retry", RetryTurnID: "conversation-retry-turn",
			Class: classEmptyResponse, Action: actionConversationContinue,
			Attempt: 2, ConsecutiveRetry: 2, StartedAt: now,
		},
	}
	d.handleEventLocked(scannedEvent{ThreadID: threadID, Event: RelevantEvent{
		Kind: "task_user_input", Timestamp: now.Add(time.Second),
	}}, now.Add(time.Second))
	thread := d.state.Threads[threadID]
	if thread.Awaiting == nil || thread.Awaiting.EventKey != "conversation-retry" || thread.RecoveryAttempts != 2 {
		t.Fatalf("programmatic conversation input cancelled its own retry: %+v", thread)
	}
}

func TestGoalEmptyResponseLimitQueuesBoundedGoalStop(t *testing.T) {
	config := isolatedConfig(t.TempDir())
	config.MaxRecoveryAttempts = 10
	config.MaxConsecutiveRetries = 2
	runner := &fakeResumeRunner{result: DispatchResult{Outcome: outcomeDispatched, Action: actionGoalBlock,
		Reason: "goal_blocked_after_empty_response_limit"}}
	d := newTestDaemon(t, config, runner)
	threadID := "019fa94e-0103-7183-b405-36bd307b6dc2"
	now := time.Now().UTC()
	d.handleEventLocked(goalScannedEvent(threadID, "active", now), now)
	failedAt := now.Add(time.Second)
	d.handleEventLocked(emptyCompletionScannedEvent(threadID, "goal-turn-0", failedAt), failedAt)

	for attempt := 1; attempt <= 2; attempt++ {
		turnID := fmt.Sprintf("goal-turn-%d", attempt)
		startedAt := failedAt.Add(10 * time.Millisecond)
		d.handleEventLocked(scannedEvent{ThreadID: threadID, Event: RelevantEvent{
			Kind: "task_started", TurnID: turnID, Timestamp: startedAt,
		}}, startedAt)
		failedAt = failedAt.Add(5 * time.Second)
		d.handleEventLocked(emptyCompletionScannedEvent(threadID, turnID, failedAt), failedAt)
	}
	thread := d.state.Threads[threadID]
	if thread.Stopped == nil || thread.Stopped.Reason != "goal_empty_response_limit" ||
		thread.Stopped.ConsecutiveRetries != 2 || thread.GoalStop == nil || thread.GoalStatus != "active" {
		t.Fatalf("goal limit did not create a durable stop request: %+v", thread)
	}

	d.handleEventLocked(scannedEvent{ThreadID: threadID, Event: RelevantEvent{
		Kind: "task_started", TurnID: "extra-native-turn", Timestamp: failedAt.Add(10 * time.Millisecond),
	}}, failedAt.Add(10*time.Millisecond))
	if thread = d.state.Threads[threadID]; thread.Stopped == nil || thread.GoalStop == nil {
		t.Fatalf("an extra native turn cleared the exhausted chain: %+v", thread)
	}

	jobs := d.dispatchDueLocked(failedAt.Add(time.Second))
	if len(jobs) != 1 || jobs[0].Kind != jobGoalBlock || jobs[0].ThreadID != threadID {
		t.Fatalf("goal-stop controller job was not prioritized: %+v", jobs)
	}
	d.wg.Add(1)
	go d.runJob(context.Background(), jobs[0])
	d.waitForJobs()
	thread = daemonThreadSnapshot(d, threadID)
	if thread.Stopped == nil || thread.GoalStop != nil || thread.GoalStatus != "blocked" || !thread.GoalHeld {
		t.Fatalf("goal stop did not preserve the reason while blocking the goal: %+v", thread)
	}
	blockedObservation := thread.GoalObservedAt
	staleActive := goalScannedEvent(threadID, "active", blockedObservation.Add(time.Second))
	staleActive.Event.Timestamp = blockedObservation.Add(-time.Millisecond)
	d.handleEventLocked(staleActive, blockedObservation)
	thread = d.state.Threads[threadID]
	if thread.GoalStatus != "blocked" || thread.Stopped == nil || !thread.GoalHeld {
		t.Fatalf("a stale active update reopened an exhausted goal: %+v", thread)
	}

	d.handleEventLocked(goalScannedEvent(threadID, "active", blockedObservation.Add(2*time.Second)), blockedObservation.Add(2*time.Second))
	thread = d.state.Threads[threadID]
	if thread.Stopped != nil || thread.GoalHeld || thread.RecoveryAttempts != 0 {
		t.Fatalf("explicit activation did not clear the automatic goal hold: %+v", thread)
	}
}

func TestGoalStopControllerFailureDoesNotConsumeProviderBudget(t *testing.T) {
	threadID := "019fa94e-0103-7183-b405-36bd307b6dc3"
	now := time.Now().UTC()
	runner := &fakeResumeRunner{err: errors.New("controller unavailable")}
	d := newTestDaemon(t, isolatedConfig(t.TempDir()), runner)
	d.state.Threads[threadID] = ThreadState{
		RecoveryAttempts: 4, ConsecutiveRetries: 4, GoalStatus: "active",
		Stopped: &StoppedRetry{EventKey: "goal-limit", Class: classEmptyResponse, Reason: "goal_empty_response_limit",
			Attempts: 4, MaxAttempts: 10, ConsecutiveRetries: 4, MaxConsecutive: 4, StoppedAt: now},
		GoalStop: &GoalStopRequest{EventKey: "goal-limit", Reason: "goal_empty_response_limit", RequestedAt: now, DueAt: now},
	}
	jobs := d.dispatchDueLocked(now)
	if len(jobs) != 1 || jobs[0].Kind != jobGoalBlock {
		t.Fatalf("goal stop was not dispatched: %+v", jobs)
	}
	d.wg.Add(1)
	go d.runJob(context.Background(), jobs[0])
	d.waitForJobs()
	thread := daemonThreadSnapshot(d, threadID)
	if thread.Stopped == nil || thread.GoalStop == nil || thread.GoalStop.DispatchFailures != 1 ||
		thread.RecoveryAttempts != 4 || thread.ConsecutiveRetries != 4 {
		t.Fatalf("controller failure changed provider retry budgets: %+v", thread)
	}
}

func TestGoalStopControllerHasVisibleTerminalFailure(t *testing.T) {
	threadID := "019fa94e-0103-7183-b405-36bd307b6dc6"
	now := time.Now().UTC()
	runner := &fakeResumeRunner{err: errors.New("controller unavailable")}
	cfg := isolatedConfig(t.TempDir())
	d := newTestDaemon(t, cfg, runner)
	d.state.Threads[threadID] = ThreadState{
		RecoveryAttempts: 4, ConsecutiveRetries: 4, GoalStatus: "active",
		Stopped: &StoppedRetry{EventKey: "goal-limit-terminal", Class: classEmptyResponse, Reason: goalEmptyResponseStopReason,
			Attempts: 4, MaxAttempts: 10, ConsecutiveRetries: 4, MaxConsecutive: 4, StoppedAt: now},
		GoalStop: &GoalStopRequest{EventKey: "goal-limit-terminal", Reason: goalEmptyResponseStopReason,
			RequestedAt: now, DueAt: now, DispatchFailures: cfg.ControllerFailureLimit - 1},
	}
	jobs := d.dispatchDueLocked(now)
	if len(jobs) != 1 || jobs[0].Kind != jobGoalBlock {
		t.Fatalf("final goal-stop attempt was not dispatched: %+v", jobs)
	}
	d.wg.Add(1)
	go d.runJob(context.Background(), jobs[0])
	d.waitForJobs()
	thread := daemonThreadSnapshot(d, threadID)
	if thread.GoalStop != nil || thread.Stopped == nil || thread.Stopped.Reason != goalEmptyResponseBlockFailReason ||
		!thread.GoalHeld || thread.RecoveryAttempts != 4 || thread.ConsecutiveRetries != 4 {
		t.Fatalf("goal-stop controller did not enter a bounded visible failure state: %+v", thread)
	}

	d.handleEventLocked(scannedEvent{ThreadID: threadID, Event: RelevantEvent{
		Kind: "task_started", TurnID: "native-after-control-failure", Timestamp: now.Add(time.Second),
	}}, now.Add(time.Second))
	thread = d.state.Threads[threadID]
	if thread.Stopped == nil || thread.Stopped.Reason != goalEmptyResponseBlockFailReason {
		t.Fatalf("a native goal turn cleared the terminal control failure: %+v", thread)
	}
}

func setRunningRetry(d *daemon, threadID string, pending *PendingRetry, retryTurnID string, madeProgress bool) {
	d.state.Threads[threadID] = ThreadState{
		LastStartedTurnID:   retryTurnID,
		CurrentTurnProgress: madeProgress,
		RecoveryAttempts:    pending.Attempt,
		ConsecutiveRetries:  pending.ConsecutiveRetry,
		Awaiting: &AwaitingRetry{
			EventKey:            pending.EventKey,
			FailedTurnID:        pending.FailedTurnID,
			FailedAt:            pending.FailedAt,
			OriginTurnStartedAt: pending.OriginTurnStartedAt,
			RetryTurnID:         retryTurnID,
			Class:               pending.Class,
			Action:              actionConversationContinue,
			Attempt:             pending.Attempt,
			MaxAttempts:         pending.MaxAttempts,
			ConsecutiveRetry:    pending.ConsecutiveRetry,
			MaxConsecutive:      pending.MaxConsecutive,
		},
	}
}
