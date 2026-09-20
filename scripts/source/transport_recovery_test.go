package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const transportTestThread = "019fa94e-0103-7183-b405-36bd307b6dbc"

type untouchedSharedServer struct {
	staticSharedServer
	calls int
}

func (s *untouchedSharedServer) Ensure(context.Context) error {
	s.calls++
	return errors.New("shared backend must not be started for official IPC")
}

func TestOfficialIPCIndependentOfSharedMode(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "unhealthy"}[enabled], func(t *testing.T) {
			dir := t.TempDir()
			cfg := defaultConfig()
			cfg.SharedAppServerEnabled = enabled
			path := filepath.Join(dir, "config.json")
			if err := writeJSONAtomic(path, cfg); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			server := &untouchedSharedServer{staticSharedServer: staticSharedServer{home: dir}}
			ipc := &fakeOfficialDesktopIPC{available: true}
			c := &sharedAppServerController{server: server, checker: staticDesktopChecker{state: desktopLegacyStdio}, officialIPC: ipc, configPath: path}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := c.Prepare(ctx); err != nil {
				t.Fatal(err)
			}
			if state, err := c.Readiness(ctx); err != nil || state != "official_ipc_ready" {
				t.Fatalf("state=%s err=%v", state, err)
			}
			result, err := c.Dispatch(ctx, transportTestThread, "", testResumeSettings(), time.Now().UTC(), time.Time{}, "", false, false, classAuthTransient, dir)
			if err != nil || result.Outcome != outcomeDispatched || len(ipc.starts) != 1 {
				t.Fatalf("dispatch=%+v err=%v starts=%v", result, err, ipc.starts)
			}
			if state, err := c.RetryThreadStatus(ctx, transportTestThread, dir); err != nil || state != "active" {
				t.Fatalf("acknowledged lifecycle=%s err=%v", state, err)
			}
			result, err = c.BlockGoal(ctx, transportTestThread, nil, dir)
			if err != nil || result.Reason != "codex_ipc_goal_control_unsupported" {
				t.Fatalf("unsupported goal control must stay closed: %+v %v", result, err)
			}
			after, _ := os.ReadFile(path)
			if server.calls != 0 || string(before) != string(after) {
				t.Fatal("IPC changed shared service or configuration")
			}
		})
	}
}

func TestOfficialIPCDoesNotBypassTransportOrHomeGuards(t *testing.T) {
	for _, state := range []desktopTransportState{desktopStopped, desktopUnknown, desktopSharedServer} {
		t.Run(string(state), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			if err := writeJSONAtomic(path, defaultConfig()); err != nil {
				t.Fatal(err)
			}
			ipc := &fakeOfficialDesktopIPC{available: true}
			server := &untouchedSharedServer{staticSharedServer: staticSharedServer{home: dir}}
			c := &sharedAppServerController{server: server, checker: staticDesktopChecker{state: state}, officialIPC: ipc, configPath: path}
			result, err := c.Dispatch(context.Background(), transportTestThread, "", testResumeSettings(), time.Now(), time.Time{}, "", false, false, classServer, dir)
			if err != nil || result.Outcome != outcomeRetryLater || len(ipc.starts) != 0 || server.calls != 0 {
				t.Fatalf("unproven route dispatched: %+v %v", result, err)
			}
			c.checker = staticDesktopChecker{state: desktopLegacyStdio}
			result, err = c.Dispatch(context.Background(), transportTestThread, "", testResumeSettings(), time.Now(), time.Time{}, "", false, false, classServer, "foreign-home")
			if err != nil || result.Reason != "codex_home_not_shared" || len(ipc.starts) != 0 {
				t.Fatalf("home guard bypassed: %+v %v", result, err)
			}
			if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Readiness(context.Background()); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}

func transportStoppedThread(now time.Time) ThreadState {
	blocked := true
	return ThreadState{
		RecoveryStartedAt: now.Add(-time.Minute), LastStartedTurnID: "failed", LastStartedAt: now.Add(-30 * time.Second),
		Stopped: &StoppedRetry{EventKey: "failure", FailedTurnID: "failed", FailedAt: now.Add(-20 * time.Second),
			StoppedAt: now.Add(-10 * time.Second), Class: classRateLimit, MaxAttempts: 15, MaxConsecutive: 5,
			Reason: "shared_app_server_disabled", TransportBlocked: &blocked},
	}
}

func TestTransportRetryReopensOnceWithPreservedBudget(t *testing.T) {
	now := time.Now().UTC()
	cfg := isolatedConfig(t.TempDir())
	cfg.SharedAppServerEnabled = false
	runner := &stateAwareRunner{fakeResumeRunner: successfulRunner(), state: "official_ipc_ready"}
	d := newTestDaemon(t, cfg, runner)
	thread := transportStoppedThread(now)
	thread.Stopped.Attempts, thread.Stopped.ConsecutiveRetries = 3, 2
	d.state.Threads[transportTestThread] = thread
	if !d.controllerRestartReady(context.Background(), now) {
		t.Fatal("disabled shared mode suppressed IPC probe")
	}
	d.reloadConfigLocked()
	if d.controllerState != "official_ipc_ready" {
		t.Fatal("config reload erased verified IPC state")
	}
	d.reopenRestartRequiredLocked(now)
	d.reopenRestartRequiredLocked(now)
	got := d.state.Threads[transportTestThread]
	if got.Stopped != nil || got.Pending == nil || got.Pending.Attempt != 4 || got.Pending.ConsecutiveRetry != 3 || !got.RecoveryStartedAt.Equal(thread.RecoveryStartedAt) {
		t.Fatalf("reopen reset budget: %+v", got)
	}
	jobs := d.dispatchDueLocked(now)
	if len(jobs) != 1 || len(d.dispatchDueLocked(now)) != 0 {
		t.Fatal("duplicate or missing dispatch")
	}
	d.wg.Add(1)
	d.runJob(context.Background(), jobs[0])
	if d.controllerState != "official_ipc_ready" || len(runner.snapshot()) != 1 {
		t.Fatal("accepted dispatch lost IPC state")
	}
	d.reopenRestartRequiredLocked(now)
	if len(d.dispatchDueLocked(now)) != 0 {
		t.Fatal("accepted retry was duplicated")
	}
}

func TestTransportRetryRejectsUnsafeOrExpiredStops(t *testing.T) {
	now := time.Now().UTC()
	cases := map[string]func(*ThreadState){
		"historical":           func(s *ThreadState) { s.Stopped.Historical = true },
		"expired":              func(s *ThreadState) { s.RecoveryStartedAt = now.Add(-31 * time.Minute) },
		"future":               func(s *ThreadState) { s.RecoveryStartedAt = now.Add(time.Minute) },
		"old-failure-replayed": func(s *ThreadState) { s.Stopped.FailedAt = now.Add(-31 * time.Minute) },
		"paused-goal":          func(s *ThreadState) { s.GoalHeld = true },
		"completed-goal":       func(s *ThreadState) { s.GoalStatus = "completed" },
		"unknown-goal":         func(s *ThreadState) { s.GoalStatus = "unknown" },
		"new-turn":             func(s *ThreadState) { s.LastStartedTurnID = "new" },
		"new-input":            func(s *ThreadState) { s.LastExternalTurnAt = now },
		"aborted":              func(s *ThreadState) { s.LastAbortedTurnID = "failed" },
		"later-abort":          func(s *ThreadState) { s.LastAbortedAt = now },
		"awaiting":             func(s *ThreadState) { s.Awaiting = &AwaitingRetry{} },
		"pending":              func(s *ThreadState) { s.Pending = &PendingRetry{} },
		"goal-stop":            func(s *ThreadState) { s.GoalStop = &GoalStopRequest{} },
		"closed-app":           func(s *ThreadState) { s.Stopped.Reason = "codex_not_running" },
		"ambiguous-dispatch":   func(s *ThreadState) { s.Stopped.TransportBlocked = nil; s.Stopped.Attempts = 1 },
		"controller-budget":    func(s *ThreadState) { s.Stopped.Reason = "controller_failures" },
		"user-stop":            func(s *ThreadState) { s.Stopped.Reason = "user_control" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			thread := transportStoppedThread(now)
			change(&thread)
			if canReopenTransportRetry(thread, now) {
				t.Fatal("unsafe stop reopened")
			}
		})
	}
	legacy := transportStoppedThread(now)
	legacy.Stopped.TransportBlocked = nil
	if !canReopenTransportRetry(legacy, now) {
		t.Fatal("recent zero-attempt legacy stop was not adopted")
	}
	legacy.LastAutoRetryAt = now
	if canReopenTransportRetry(legacy, now) {
		t.Fatal("ambiguous legacy dispatch was adopted")
	}
}

func TestTransportRetryPauseLimitsActiveAndRestart(t *testing.T) {
	now := time.Now().UTC()
	for _, guard := range []string{"paused", "active", "recovery-budget", "consecutive-budget", "lowered-limit"} {
		t.Run(guard, func(t *testing.T) {
			d := newTestDaemon(t, isolatedConfig(t.TempDir()), successfulRunner())
			thread := transportStoppedThread(now)
			switch guard {
			case "paused":
				d.paused = true
			case "active":
				d.active[transportTestThread] = RetryJob{}
			case "recovery-budget":
				thread.Stopped.Attempts = thread.Stopped.MaxAttempts
			case "consecutive-budget":
				thread.Stopped.ConsecutiveRetries = thread.Stopped.MaxConsecutive
			case "lowered-limit":
				thread.Stopped.Attempts = 3
				d.config.MaxRecoveryAttempts = 3
			}
			d.state.Threads[transportTestThread] = thread
			d.reopenRestartRequiredLocked(now)
			if d.state.Threads[transportTestThread].Stopped == nil {
				t.Fatal("guard did not prevent reopen")
			}
		})
	}
	d := newTestDaemon(t, isolatedConfig(t.TempDir()), successfulRunner())
	thread := transportStoppedThread(now)
	thread.Pending = &PendingRetry{EventKey: "failure", FailedTurnID: "failed", FailedAt: thread.Stopped.FailedAt, Class: classRateLimit, DueAt: now, Attempt: 4, MaxAttempts: 15, ConsecutiveRetry: 3, MaxConsecutive: 5}
	thread.Stopped = nil
	d.state.Threads[transportTestThread] = thread
	d.stopPendingForControllerLocked(transportTestThread, thread, now, "shared_app_server_disabled")
	if marker := d.state.Threads[transportTestThread].Stopped.TransportBlocked; marker == nil || !*marker {
		t.Fatal("pre-dispatch stop lost its recovery marker")
	}
	if err := d.persistStateLocked(); err != nil {
		t.Fatal(err)
	}
	restarted, err := newDaemon(d.config, d.dataDir, d.logger, successfulRunner())
	if err != nil {
		t.Fatal(err)
	}
	restarted.reopenRestartRequiredLocked(now.Add(time.Second))
	got := restarted.state.Threads[transportTestThread]
	if got.Pending == nil || got.Pending.Attempt != 4 || got.Pending.ConsecutiveRetry != 3 {
		t.Fatalf("restart lost counters: %+v", got)
	}
	restarted.applyControlCommandLocked(ControlCommand{ThreadID: transportTestThread, Action: commandCancelRetry}, now)
	restarted.reopenRestartRequiredLocked(now)
	if restarted.state.Threads[transportTestThread].Pending != nil {
		t.Fatal("cancelled retry reopened")
	}
}

func TestTransportRetryNeverReopensAmbiguousParentOrDispatch(t *testing.T) {
	now := time.Now().UTC()
	for _, scenario := range []string{"parent-notified", "dispatch-failed", "goal-restart"} {
		t.Run(scenario, func(t *testing.T) {
			d := newTestDaemon(t, isolatedConfig(t.TempDir()), successfulRunner())
			thread := transportStoppedThread(now)
			thread.Pending = &PendingRetry{FailedTurnID: "failed", FailedAt: thread.Stopped.FailedAt, Attempt: 1, MaxAttempts: 15, ConsecutiveRetry: 1, MaxConsecutive: 5}
			switch scenario {
			case "parent-notified":
				thread.Pending.ParentNotified = true
			case "dispatch-failed":
				thread.Pending.DispatchFailures = 1
			case "goal-restart":
				thread.Pending.GoalLimitRestart = true
			}
			d.stopPendingForControllerLocked(transportTestThread, thread, now, "shared_app_server_disabled")
			got := d.state.Threads[transportTestThread]
			if got.Stopped.TransportBlocked == nil || *got.Stopped.TransportBlocked || canReopenTransportRetry(got, now) {
				t.Fatal("ambiguous work rejoined automatic chain")
			}
			if err := d.persistStateLocked(); err != nil {
				t.Fatal(err)
			}
			loaded, err := loadState(d.statePath)
			if err != nil {
				t.Fatal(err)
			}
			if canReopenTransportRetry(loaded.Threads[transportTestThread], now) {
				t.Fatal("explicit false marker was lost and mistaken for legacy state")
			}
		})
	}
}

func TestTransportRetryDoesNotAutoReopenUnacknowledgedCrash(t *testing.T) {
	now := time.Now().UTC()
	d := newTestDaemon(t, isolatedConfig(t.TempDir()), successfulRunner())
	thread := transportStoppedThread(now)
	thread.LastAutoRetryAt = now.Add(-time.Second)
	thread.Awaiting = &AwaitingRetry{EventKey: "failure", FailedTurnID: "failed", FailedAt: thread.Stopped.FailedAt,
		Attempt: 1, MaxAttempts: 15, ConsecutiveRetry: 1, MaxConsecutive: 5}
	thread.Stopped = nil
	d.state.Threads[transportTestThread] = thread
	d.reconcileStartupState(now)
	d.stopPendingForControllerLocked(transportTestThread, d.state.Threads[transportTestThread], now, "shared_app_server_disabled")
	got := d.state.Threads[transportTestThread]
	if got.Stopped.TransportBlocked == nil || *got.Stopped.TransportBlocked || canReopenTransportRetry(got, now) {
		t.Fatal("unacknowledged dispatch was incorrectly marked never-sent")
	}
}

func TestReportedProviderErrorsRemainRetryable(t *testing.T) {
	for text, want := range map[string]FailureClass{
		"Selected model is at capacity. Please try a different model.":               classRateLimit,
		"stream disconnected before completion: auth_unavailable: no auth available": classAuthTransient,
	} {
		got := classifyFailure(text, defaultConfig())
		if !got.Retry || got.Class != want {
			t.Fatalf("decision=%+v want=%s", got, want)
		}
	}
}

// Exercise scanning, persistence, real controller routing and acknowledgement
// together using synthetic rollouts and an in-memory IPC endpoint only.
func TestTransportRecoveryLifecycle(t *testing.T) {
	for _, intervention := range []string{"none", "new-turn", "abort", "pause", "cancel"} {
		t.Run(intervention, func(t *testing.T) {
			home := t.TempDir()
			sessions := filepath.Join(home, "sessions")
			if err := os.MkdirAll(sessions, 0700); err != nil {
				t.Fatal(err)
			}
			rollout := filepath.Join(sessions, "rollout-2026-09-20T00-00-00-"+transportTestThread+".jsonl")
			if err := os.WriteFile(rollout, append(marshalSyntheticTurnContext(t, syntheticSettingsPayload("test-model", "high", "")), '\n'), 0600); err != nil {
				t.Fatal(err)
			}
			cfg := isolatedConfig(home)
			cfg.SharedAppServerEnabled = false
			d := newTestDaemon(t, cfg, successfulRunner())
			server := &untouchedSharedServer{staticSharedServer: staticSharedServer{home: home}}
			ipc := &fakeOfficialDesktopIPC{}
			controller := &sharedAppServerController{server: server, checker: staticDesktopChecker{state: desktopLegacyStdio}, officialIPC: ipc, configPath: filepath.Join(d.dataDir, "config.json")}
			runner := &appResumeRunner{controller: controller, configPath: controller.configPath}
			d.runner = runner
			now := time.Now().UTC()
			d.state.Initialized = true
			appendLine(t, rollout, makeEventLine(t, now.Add(-3*time.Second).Format(time.RFC3339Nano), "task_started", "failed", nil))
			appendLine(t, rollout, makeEventLine(t, now.Add(-2*time.Second).Format(time.RFC3339Nano), "task_complete", "failed", "Selected model is at capacity. Please try a different model."))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := d.tick(ctx, now); err != nil {
				t.Fatal(err)
			}
			if err := d.tick(ctx, now.Add(2*time.Second)); err != nil {
				t.Fatal(err)
			}
			stopped := d.state.Threads[transportTestThread].Stopped
			if stopped == nil || stopped.Attempts != 0 || stopped.Reason != "shared_app_server_disabled" {
				t.Fatalf("failure not stopped before dispatch: %+v", stopped)
			}
			restarted, err := newDaemon(cfg, d.dataDir, d.logger, runner)
			if err != nil {
				t.Fatal(err)
			}
			ipc.available = true
			switch intervention {
			case "new-turn":
				appendLine(t, rollout, makeEventLine(t, now.Format(time.RFC3339Nano), "task_started", "user-turn", nil))
			case "abort":
				restarted.handleTurnAbortedLocked(transportTestThread, RelevantEvent{TurnID: "failed", Timestamp: now}, restarted.state.Threads[transportTestThread])
			case "pause":
				if _, err := saveControlState(d.controlPath, true, now); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				if _, err := queueControlCommand(d.commandDir, commandCancelRetry, transportTestThread, now); err != nil {
					t.Fatal(err)
				}
			}
			if err := restarted.tick(ctx, now.Add(3*time.Second)); err != nil {
				t.Fatal(err)
			}
			restarted.waitForJobs()
			want := 0
			if intervention == "none" {
				want = 1
			}
			if len(ipc.starts) != want || server.calls != 0 {
				t.Fatalf("starts=%v shared calls=%d want=%d", ipc.starts, server.calls, want)
			}
			if intervention != "none" {
				return
			}
			thread := restarted.state.Threads[transportTestThread]
			if thread.Awaiting == nil || thread.Awaiting.Attempt != 1 {
				t.Fatalf("request not awaiting acknowledgement: %+v", thread)
			}
			ackAt := thread.Awaiting.DispatchStartedAt
			appendLine(t, rollout, makeEventLine(t, ackAt.Format(time.RFC3339Nano), "task_started", "retry-turn", nil))
			appendLine(t, rollout, makeEventLine(t, ackAt.Add(time.Second).Format(time.RFC3339Nano), "task_complete", "retry-turn", nil))
			if err := restarted.tick(ctx, now.Add(4*time.Second)); err != nil {
				t.Fatal(err)
			}
			restarted.waitForJobs()
			thread = restarted.state.Threads[transportTestThread]
			if thread.Awaiting != nil || thread.Pending != nil || thread.Stopped != nil || thread.RecoveryAttempts != 0 || len(ipc.starts) != 1 {
				t.Fatalf("recovery duplicated or did not complete: %+v starts=%v", thread, ipc.starts)
			}
		})
	}
}

func TestTransportRetryExpiresDuringPauseAndPreservesClosedApp(t *testing.T) {
	now := time.Now().UTC()
	cfg := isolatedConfig(t.TempDir())
	cfg.SharedAppServerEnabled = false
	d := newTestDaemon(t, cfg, successfulRunner())
	d.controllerState = "official_ipc_ready"
	d.state.Threads[transportTestThread] = transportStoppedThread(now)
	d.reopenRestartRequiredLocked(now)
	if jobs := d.dispatchDueLocked(now.Add(31 * time.Minute)); len(jobs) != 0 {
		t.Fatal("expired chain dispatched")
	}
	if d.state.Threads[transportTestThread].Stopped.Reason != "recovery_time_limit" {
		t.Fatal("timeout not visible")
	}
	d.state.Threads[transportTestThread] = transportStoppedThread(now)
	d.reopenRestartRequiredLocked(now)
	d.controllerState = "codex_not_running"
	d.reloadConfigLocked()
	if jobs := d.dispatchDueLocked(now); len(jobs) != 0 {
		t.Fatal("closed app dispatched")
	}
	if d.state.Threads[transportTestThread].Stopped.Reason != "codex_not_running" {
		t.Fatal("closed app was relabeled as automatically recoverable")
	}
}
