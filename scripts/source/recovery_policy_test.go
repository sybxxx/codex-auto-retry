package main

import (
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
