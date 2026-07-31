package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestManagementSnapshotIncludesIndependentCountdowns(t *testing.T) {
	dataDir := t.TempDir()
	service := newManagementService(dataDir)
	now := time.Date(2026, 7, 27, 6, 0, 0, 0, time.UTC)
	state := newRuntimeState()
	state.Threads["019f9d5d-9c82-75b1-b7c0-20a658af0423"] = ThreadState{Pending: &PendingRetry{
		Class: classServer, DueAt: now.Add(5500 * time.Millisecond),
		Attempt: 4, MaxAttempts: 15, ConsecutiveRetry: 1, MaxConsecutive: 5,
	}}
	state.Threads["019f9d5d-9c82-75b1-b7c0-20a658af0424"] = ThreadState{Awaiting: &AwaitingRetry{
		Class: classTransient, Action: actionGoalResume, RetryTurnID: "retry-turn",
		Attempt: 3, MaxAttempts: 15, ConsecutiveRetry: 2, MaxConsecutive: 5,
	}}
	if err := writeJSONAtomic(service.statePath, state); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(service.statusPath, StatusSnapshot{
		Version:         appVersion,
		Running:         true,
		PID:             os.Getpid(),
		LastScanAt:      now.Add(-time.Second),
		WatchedRoots:    2,
		ControllerState: "codex_restart_required",
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.snapshot(now)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Running || snapshot.ControllerState != "codex_restart_required" ||
		snapshot.PendingRetries != 1 || snapshot.ActiveRetries != 1 || len(snapshot.Retries) != 2 {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	if snapshot.Retries[0].State != "running" || snapshot.Retries[0].RecoveryAttempt != 3 ||
		snapshot.Retries[0].ConsecutiveRetry != 2 || snapshot.Retries[1].SecondsRemaining != 6 ||
		snapshot.Retries[1].RecoveryAttempt != 4 || snapshot.Retries[1].ConsecutiveRetry != 1 {
		t.Fatalf("queue state or countdown is incorrect: %+v", snapshot.Retries)
	}
}

func TestManagementShowsStoppedRetryAndQueuesRestart(t *testing.T) {
	dataDir := t.TempDir()
	service := newManagementService(dataDir)
	now := time.Date(2026, 7, 28, 6, 0, 0, 0, time.UTC)
	threadID := "019f9d5d-9c82-75b1-b7c0-20a658af0423"
	state := newRuntimeState()
	state.Threads[threadID] = ThreadState{Stopped: &StoppedRetry{
		Class: classServer, Attempts: 15, MaxAttempts: 15,
		ConsecutiveRetries: 1, MaxConsecutive: 5,
		StoppedAt: now, Reason: "recovery_attempt_limit",
	}}
	if err := writeJSONAtomic(service.statePath, state); err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.snapshot(now)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.StoppedRetries != 1 || len(snapshot.Retries) != 1 ||
		snapshot.Retries[0].State != "stopped" || !snapshot.Retries[0].CanRestart ||
		snapshot.Retries[0].RecoveryAttempt != 15 || snapshot.Retries[0].ConsecutiveRetry != 1 ||
		snapshot.Retries[0].StopReason != "recovery_attempt_limit" {
		t.Fatalf("stopped retry was not exposed: %+v", snapshot)
	}
	if _, err := service.restartRetry(threadID, now); err != nil {
		t.Fatal(err)
	}
	commands, _, err := loadControlCommandFiles(service.commandDir)
	if err != nil || len(commands) != 1 || commands[0].Command.Action != commandRestartRetry {
		t.Fatalf("restart command missing: commands=%+v err=%v", commands, err)
	}
}

func TestManagementExposesGoalEmptyResponseStopReason(t *testing.T) {
	dataDir := t.TempDir()
	service := newManagementService(dataDir)
	now := time.Date(2026, 7, 29, 9, 30, 0, 0, time.UTC)
	threadID := "019f9d5d-9c82-75b1-b7c0-20a658af0425"
	state := newRuntimeState()
	state.Threads[threadID] = ThreadState{Stopped: &StoppedRetry{
		Class: classEmptyResponse, Attempts: 4, MaxAttempts: 15,
		ConsecutiveRetries: 5, MaxConsecutive: 5,
		StoppedAt: now, Reason: goalEmptyResponseStopReason,
	}}
	if err := writeJSONAtomic(service.statePath, state); err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.snapshot(now)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.StoppedRetries != 1 || len(snapshot.Retries) != 1 ||
		snapshot.Retries[0].StopReason != goalEmptyResponseStopReason ||
		snapshot.Retries[0].Class != classEmptyResponse || !snapshot.Retries[0].CanRestart {
		t.Fatalf("goal stop reason was not exposed to management surfaces: %+v", snapshot)
	}
}

func TestManagementExposesGoalBlockFailureReason(t *testing.T) {
	dataDir := t.TempDir()
	service := newManagementService(dataDir)
	now := time.Date(2026, 7, 29, 9, 35, 0, 0, time.UTC)
	threadID := "019f9d5d-9c82-75b1-b7c0-20a658af0426"
	state := newRuntimeState()
	state.Threads[threadID] = ThreadState{Stopped: &StoppedRetry{
		Class: classEmptyResponse, Attempts: 4, MaxAttempts: 15,
		ConsecutiveRetries: 5, MaxConsecutive: 5,
		StoppedAt: now, Reason: goalEmptyResponseBlockFailReason,
	}}
	if err := writeJSONAtomic(service.statePath, state); err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.snapshot(now)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.StoppedRetries != 1 || len(snapshot.Retries) != 1 ||
		snapshot.Retries[0].StopReason != goalEmptyResponseBlockFailReason || !snapshot.Retries[0].CanRestart {
		t.Fatalf("goal-block failure reason was not exposed: %+v", snapshot)
	}
}

func TestManagementRejectsStaleRunningStatus(t *testing.T) {
	service := newManagementService(t.TempDir())
	now := time.Now().UTC()
	state := newRuntimeState()
	state.Threads["019f9d5d-9c82-75b1-b7c0-20a658af0423"] = ThreadState{Awaiting: &AwaitingRetry{
		Class: classServer, Attempt: 1, RetryTurnID: "stale-turn",
	}}
	if err := writeJSONAtomic(service.statePath, state); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(service.statusPath, StatusSnapshot{
		Version:    "stale-version",
		Running:    true,
		PID:        999999,
		LastScanAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.snapshot(now)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Running || snapshot.Version != appVersion || snapshot.ActiveRetries != 0 || len(snapshot.Retries) != 0 {
		t.Fatalf("dead process was reported as running: %+v", snapshot)
	}
}

func TestManagementUpdatesAllRetrySettings(t *testing.T) {
	service := newManagementService(t.TempDir())
	now := time.Now().UTC()
	snapshot, err := service.setRetrySettings(RetrySettings{
		RetryPrompt:           "继续检查",
		MaxConsecutiveRetries: 3,
		MaxRecoveryAttempts:   7,
		InitialDelaySeconds:   9,
		MaxDelaySeconds:       120,
		DelayIncrementSeconds: 4,
		DelayStrategy:         delayStrategyFixed,
		ShowNotifications:     false,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.RetryPrompt != "继续检查" || snapshot.MaxRecoveryAttempts != 7 ||
		snapshot.MaxConsecutiveRetries != 3 || snapshot.DelayStrategy != delayStrategyFixed ||
		snapshot.InitialDelaySeconds != 9 || snapshot.MaxDelaySeconds != 120 ||
		snapshot.DelayIncrementSeconds != 4 ||
		snapshot.ShowNotifications {
		t.Fatalf("retry settings did not round trip: %+v", snapshot)
	}
}

func TestManagementUpdatesCombinedLocalSettingsAndPause(t *testing.T) {
	service := newManagementService(t.TempDir())
	settings := RetrySettings{
		RetryPrompt:           "继续",
		MaxConsecutiveRetries: 2,
		MaxRecoveryAttempts:   4,
		InitialDelaySeconds:   6,
		MaxDelaySeconds:       90,
		DelayIncrementSeconds: 3,
		DelayStrategy:         delayStrategyExponential,
		ShowNotifications:     true,
	}
	if err := service.setLocalSettings(settings, true, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.snapshot(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Paused || snapshot.MaxRecoveryAttempts != 4 || snapshot.MaxConsecutiveRetries != 2 ||
		snapshot.DelayStrategy != delayStrategyExponential || snapshot.InitialDelaySeconds != 6 ||
		snapshot.MaxDelaySeconds != 90 || snapshot.DelayIncrementSeconds != 3 || !snapshot.ShowNotifications {
		t.Fatalf("combined local settings did not round trip: %+v", snapshot)
	}
}

func TestManagementUpdatesPromptAndQueuesControls(t *testing.T) {
	dataDir := t.TempDir()
	service := newManagementService(dataDir)
	now := time.Date(2026, 7, 27, 6, 0, 0, 0, time.UTC)
	threadID := "019f9d5d-9c82-75b1-b7c0-20a658af0423"
	state := newRuntimeState()
	state.Threads[threadID] = ThreadState{Pending: &PendingRetry{DueAt: now.Add(time.Minute), Attempt: 1}}
	if err := writeJSONAtomic(service.statePath, state); err != nil {
		t.Fatal(err)
	}
	updated, err := service.setRetryPrompt("继续处理", now)
	if err != nil {
		t.Fatal(err)
	}
	if updated.RetryPrompt != "继续处理" {
		t.Fatalf("prompt was not updated: %+v", updated)
	}
	paused, err := service.setPaused(true, now)
	if err != nil || !paused.Paused {
		t.Fatalf("pause was not updated: snapshot=%+v err=%v", paused, err)
	}
	if _, err := service.retryNow(threadID, now); err != nil {
		t.Fatal(err)
	}
	commands, _, err := loadControlCommandFiles(service.commandDir)
	if err != nil || len(commands) != 1 || commands[0].Command.Action != commandRetryNow {
		t.Fatalf("retry-now command missing: commands=%+v err=%v", commands, err)
	}
}

func TestManagementRejectsOversizedPromptAndNonPendingCommand(t *testing.T) {
	service := newManagementService(t.TempDir())
	if _, err := service.setRetryPrompt(string(make([]rune, maxRetryPromptRunes+1)), time.Now()); err == nil {
		t.Fatal("oversized prompt was accepted")
	}
	if _, err := service.cancelRetry("019f9d5d-9c82-75b1-b7c0-20a658af0423", time.Now()); err == nil {
		t.Fatal("cancel command was queued for a task without a pending retry")
	}
	if _, err := os.Stat(filepath.Join(service.dataDir, "commands")); !os.IsNotExist(err) {
		t.Fatalf("command directory should not exist after rejection: %v", err)
	}
}
