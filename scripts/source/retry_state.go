package main

import (
	"path/filepath"
	"time"
)

func (d *daemon) reconcileStartupState(now time.Time) {
	for threadID, thread := range d.state.Threads {
		if thread.Awaiting == nil || thread.Awaiting.RetryTurnID != "" {
			continue
		}
		awaiting := thread.Awaiting
		thread.Awaiting = nil
		thread.Pending = &PendingRetry{
			EventKey:         awaiting.EventKey,
			FailedTurnID:     awaiting.FailedTurnID,
			FailedAt:         awaiting.FailedAt,
			Class:            awaiting.Class,
			DueAt:            now,
			CodexHome:        awaiting.CodexHome,
			RolloutPath:      awaiting.RolloutPath,
			Attempt:          awaiting.Attempt,
			MaxAttempts:      awaiting.MaxAttempts,
			DispatchFailures: awaiting.DispatchFailures,
		}
		d.state.Threads[threadID] = thread
		d.logger.Printf("stale starting retry restored thread=%s", shortThreadID(threadID))
	}
}

func (d *daemon) reloadConfigLocked() {
	config, err := loadOrCreateConfig(filepath.Join(d.dataDir, "config.json"))
	if err != nil {
		d.lastError = err.Error()
		d.logger.Printf("config reload failed category=config")
		return
	}
	d.config = config
	for threadID, thread := range d.state.Threads {
		if thread.Pending == nil {
			continue
		}
		limit := retryAttemptLimit(thread.Pending.Class, config)
		thread.Pending.MaxAttempts = limit
		if thread.Pending.Attempt > limit {
			d.stopPendingRetryLocked(threadID, thread, time.Now().UTC())
			continue
		}
		d.state.Threads[threadID] = thread
	}
}

func (d *daemon) stopPendingRetryLocked(threadID string, thread ThreadState, now time.Time) {
	pending := thread.Pending
	if pending == nil {
		return
	}
	thread.Pending = nil
	thread.Awaiting = nil
	completedAttempts := pending.Attempt - 1
	if completedAttempts < 1 {
		completedAttempts = pending.MaxAttempts
	}
	if completedAttempts > pending.MaxAttempts {
		completedAttempts = pending.MaxAttempts
	}
	thread.ConsecutiveFailures = completedAttempts
	thread.Stopped = &StoppedRetry{
		EventKey:     pending.EventKey,
		FailedTurnID: pending.FailedTurnID,
		FailedAt:     pending.FailedAt,
		Class:        pending.Class,
		StoppedAt:    now,
		CodexHome:    pending.CodexHome,
		RolloutPath:  pending.RolloutPath,
		Attempts:     completedAttempts,
		MaxAttempts:  pending.MaxAttempts,
		Reason:       "attempt_limit",
	}
	d.state.Threads[threadID] = thread
	d.logger.Printf("retry exhausted thread=%s category=%s attempts=%d", shortThreadID(threadID), pending.Class, completedAttempts)
}

func (d *daemon) applyControlCommandLocked(command ControlCommand, now time.Time) {
	thread, found := d.state.Threads[command.ThreadID]
	if !found {
		d.logger.Printf("control command ignored thread=%s reason=retry_not_found", shortThreadID(command.ThreadID))
		return
	}
	switch command.Action {
	case commandRetryNow:
		if thread.Pending == nil {
			d.logger.Printf("control command ignored thread=%s reason=retry_not_pending", shortThreadID(command.ThreadID))
			return
		}
		thread.Pending.DueAt = now
		d.state.Threads[command.ThreadID] = thread
		d.logger.Printf("retry expedited thread=%s", shortThreadID(command.ThreadID))
	case commandCancelRetry:
		if thread.Pending == nil {
			d.logger.Printf("control command ignored thread=%s reason=retry_not_pending", shortThreadID(command.ThreadID))
			return
		}
		thread.Pending = nil
		thread.ConsecutiveFailures = 0
		d.state.Threads[command.ThreadID] = thread
		d.logger.Printf("retry cancelled thread=%s reason=user_control", shortThreadID(command.ThreadID))
	case commandRestartRetry:
		if thread.Stopped == nil {
			d.logger.Printf("control command ignored thread=%s reason=retry_not_stopped", shortThreadID(command.ThreadID))
			return
		}
		stopped := thread.Stopped
		thread.Stopped = nil
		thread.ConsecutiveFailures = 1
		thread.Pending = &PendingRetry{
			EventKey:     stopped.EventKey,
			FailedTurnID: stopped.FailedTurnID,
			FailedAt:     stopped.FailedAt,
			Class:        stopped.Class,
			DueAt:        now,
			CodexHome:    stopped.CodexHome,
			RolloutPath:  stopped.RolloutPath,
			Attempt:      1,
			MaxAttempts:  retryAttemptLimit(stopped.Class, d.config),
		}
		d.state.Threads[command.ThreadID] = thread
		d.logger.Printf("retry restarted thread=%s", shortThreadID(command.ThreadID))
	}
}

func (d *daemon) handleEventLocked(item scannedEvent, now time.Time) {
	event := item.Event
	key := eventKey(item.ThreadID, event)
	if _, exists := d.state.ProcessedEvents[key]; exists {
		return
	}
	d.state.ProcessedEvents[key] = now
	thread := d.state.Threads[item.ThreadID]

	switch event.Kind {
	case "task_started":
		d.handleTaskStartedLocked(item.ThreadID, event, thread)
	case "task_complete":
		d.handleTaskCompleteLocked(item, key, now, thread)
	case "thread_goal_updated":
		d.handleGoalUpdatedLocked(item.ThreadID, event, thread)
	}
}

func (d *daemon) handleGoalUpdatedLocked(threadID string, event RelevantEvent, thread ThreadState) {
	staleUpdate := !thread.GoalUpdatedAt.IsZero() && event.GoalUpdatedAt.Before(thread.GoalUpdatedAt)
	staleObservation := event.GoalUpdatedAt.Equal(thread.GoalUpdatedAt) &&
		!thread.GoalObservedAt.IsZero() && !event.Timestamp.After(thread.GoalObservedAt)
	if staleUpdate || staleObservation {
		d.logger.Printf("goal update ignored thread=%s reason=stale_goal_state", shortThreadID(threadID))
		return
	}
	thread.GoalStatus = event.GoalStatus
	thread.GoalUpdatedAt = event.GoalUpdatedAt
	thread.GoalObservedAt = event.Timestamp
	thread.GoalHeld = goalRequiresHold(event.GoalStatus, event.GoalUpdatedAt, thread.LastFailureAt)
	if !thread.GoalHeld {
		d.state.Threads[threadID] = thread
		return
	}
	hadRetry := thread.Pending != nil || thread.Awaiting != nil
	if _, active := d.active[threadID]; active {
		hadRetry = true
	}
	if cancel := d.activeCtx[threadID]; cancel != nil {
		cancel()
	}
	thread.Pending = nil
	thread.Awaiting = nil
	thread.ConsecutiveFailures = 0
	thread.Stopped = nil
	d.state.Threads[threadID] = thread
	if hadRetry {
		d.logger.Printf("retry cancelled thread=%s reason=goal_held", shortThreadID(threadID))
	}
}

func (d *daemon) handleTaskStartedLocked(threadID string, event RelevantEvent, thread ThreadState) {
	if thread.Awaiting != nil {
		awaiting := thread.Awaiting
		withinDispatchWindow := !event.Timestamp.Before(awaiting.DispatchStartedAt.Add(-2*time.Second)) &&
			(awaiting.StartDeadline.IsZero() || !event.Timestamp.After(awaiting.StartDeadline.Add(2*time.Second)))
		if awaiting.RetryTurnID == "" && event.TurnID != "" && withinDispatchWindow {
			awaiting.RetryTurnID = event.TurnID
			awaiting.StartedAt = event.Timestamp
			thread.Awaiting = awaiting
			d.state.Threads[threadID] = thread
			d.logger.Printf("retry acknowledged thread=%s attempt=%d", shortThreadID(threadID), awaiting.Attempt)
			return
		}
		if awaiting.RetryTurnID == event.TurnID {
			return
		}
		d.cancelRetryLocked(threadID, thread, "manual_task_started")
		return
	}
	if thread.Pending != nil {
		d.cancelRetryLocked(threadID, thread, "manual_task_started")
		return
	}
	thread.ConsecutiveFailures = 0
	thread.Stopped = nil
	d.state.Threads[threadID] = thread
}

func (d *daemon) handleTaskCompleteLocked(item scannedEvent, key string, now time.Time, thread ThreadState) {
	event := item.Event
	if thread.Awaiting != nil {
		awaiting := thread.Awaiting
		if awaiting.RetryTurnID == "" || awaiting.RetryTurnID != event.TurnID {
			d.logger.Printf("completion ignored thread=%s reason=turn_mismatch", shortThreadID(item.ThreadID))
			return
		}
		if event.ErrorText == "" {
			d.logger.Printf("retry chain recovered thread=%s attempt=%d", shortThreadID(item.ThreadID), awaiting.Attempt)
			d.resetRetryStateLocked(item.ThreadID, thread)
			return
		}
		thread.Awaiting = nil
		d.scheduleFailureLocked(item, key, now, thread, awaiting.Attempt+1)
		return
	}

	if thread.Pending != nil {
		if thread.Pending.FailedTurnID != event.TurnID {
			d.logger.Printf("completion ignored thread=%s reason=pending_turn_mismatch", shortThreadID(item.ThreadID))
			return
		}
		if event.ErrorText == "" {
			d.resetRetryStateLocked(item.ThreadID, thread)
		}
		return
	}

	if event.ErrorText == "" {
		thread.ConsecutiveFailures = 0
		thread.Stopped = nil
		d.state.Threads[item.ThreadID] = thread
		return
	}
	d.scheduleFailureLocked(item, key, now, thread, 1)
}

func (d *daemon) scheduleFailureLocked(item scannedEvent, key string, now time.Time, thread ThreadState, attempt int) {
	if thread.GoalHeld {
		if thread.GoalStatus == "blocked" && goalBlockedByFailure(thread.GoalUpdatedAt, item.Event.Timestamp) {
			thread.GoalHeld = false
		} else {
			thread.Pending = nil
			thread.Awaiting = nil
			thread.ConsecutiveFailures = 0
			thread.LastFailureAt = item.Event.Timestamp
			d.state.Threads[item.ThreadID] = thread
			d.logger.Printf("retry skipped thread=%s category=non_retryable reason=goal_held", shortThreadID(item.ThreadID))
			return
		}
	}
	decision := classifyFailure(item.Event.ErrorText, d.config)
	if !decision.Retry {
		d.resetRetryStateLocked(item.ThreadID, thread)
		d.logger.Printf("retry skipped thread=%s category=non_retryable reason=%s", shortThreadID(item.ThreadID), decision.Reason)
		return
	}
	decision.MaxAttempts = retryAttemptLimitForDecision(decision, d.config)
	if attempt > decision.MaxAttempts {
		completedAttempts := attempt - 1
		thread.Pending = nil
		thread.Awaiting = nil
		thread.ConsecutiveFailures = completedAttempts
		thread.LastFailureAt = item.Event.Timestamp
		thread.Stopped = &StoppedRetry{
			EventKey:     key,
			FailedTurnID: item.Event.TurnID,
			FailedAt:     item.Event.Timestamp,
			Class:        decision.Class,
			StoppedAt:    now,
			CodexHome:    item.Root.CodexHome,
			RolloutPath:  item.RolloutPath,
			Attempts:     completedAttempts,
			MaxAttempts:  decision.MaxAttempts,
			Reason:       "attempt_limit",
		}
		d.state.Threads[item.ThreadID] = thread
		d.logger.Printf("retry exhausted thread=%s category=%s attempts=%d", shortThreadID(item.ThreadID), decision.Class, completedAttempts)
		return
	}

	delay := retryDelay(attempt, d.config)
	thread.ConsecutiveFailures = attempt
	thread.LastFailureAt = item.Event.Timestamp
	thread.Awaiting = nil
	thread.Stopped = nil
	thread.Pending = &PendingRetry{
		EventKey:     key,
		FailedTurnID: item.Event.TurnID,
		FailedAt:     item.Event.Timestamp,
		Class:        decision.Class,
		DueAt:        now.Add(delay),
		CodexHome:    item.Root.CodexHome,
		RolloutPath:  item.RolloutPath,
		Attempt:      attempt,
		MaxAttempts:  decision.MaxAttempts,
	}
	d.state.Threads[item.ThreadID] = thread
	d.logger.Printf("retry scheduled thread=%s category=%s attempt=%d delay_seconds=%d", shortThreadID(item.ThreadID), decision.Class, attempt, int(delay.Seconds()))
}

func retryAttemptLimitForDecision(decision RetryDecision, config Config) int {
	limit := config.MaxRetryAttempts
	if decision.MaxAttempts > 0 && decision.MaxAttempts < limit {
		limit = decision.MaxAttempts
	}
	return limit
}

func retryAttemptLimit(class FailureClass, config Config) int {
	decision := RetryDecision{Class: class}
	switch class {
	case classAuthLimited:
		decision.MaxAttempts = config.AuthMaxAttempts
	case classUnknown:
		decision.MaxAttempts = config.UnknownMaxAttempts
	}
	return retryAttemptLimitForDecision(decision, config)
}

func goalRequiresHold(status string, goalUpdatedAt, failureAt time.Time) bool {
	if status == "active" {
		return false
	}
	if status == "blocked" {
		return !goalBlockedByFailure(goalUpdatedAt, failureAt)
	}
	return true
}

func goalBlockedByFailure(goalUpdatedAt, failureAt time.Time) bool {
	if goalUpdatedAt.IsZero() || failureAt.IsZero() {
		return false
	}
	return !goalUpdatedAt.Before(failureAt.Add(-2*time.Second)) &&
		!goalUpdatedAt.After(failureAt.Add(5*time.Second))
}

func (d *daemon) cancelRetryLocked(threadID string, thread ThreadState, reason string) {
	if cancel := d.activeCtx[threadID]; cancel != nil {
		cancel()
	}
	thread.Pending = nil
	thread.Awaiting = nil
	thread.ConsecutiveFailures = 0
	thread.Stopped = nil
	d.state.Threads[threadID] = thread
	d.logger.Printf("retry cancelled thread=%s reason=%s", shortThreadID(threadID), reason)
}

func (d *daemon) resetRetryStateLocked(threadID string, thread ThreadState) {
	thread.Pending = nil
	thread.Awaiting = nil
	thread.ConsecutiveFailures = 0
	thread.Stopped = nil
	d.state.Threads[threadID] = thread
}
