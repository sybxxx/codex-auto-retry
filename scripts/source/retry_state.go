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
			EventKey:            awaiting.EventKey,
			FailedTurnID:        awaiting.FailedTurnID,
			FailedAt:            awaiting.FailedAt,
			OriginTurnStartedAt: awaiting.OriginTurnStartedAt,
			Class:               awaiting.Class,
			DueAt:               now,
			CodexHome:           awaiting.CodexHome,
			RolloutPath:         awaiting.RolloutPath,
			Attempt:             awaiting.Attempt,
			MaxAttempts:         awaiting.MaxAttempts,
			ConsecutiveRetry:    awaiting.ConsecutiveRetry,
			MaxConsecutive:      awaiting.MaxConsecutive,
			DispatchFailures:    awaiting.DispatchFailures,
			ParentNotified:      awaiting.ParentNotified,
			GoalLimitRestart:    awaiting.GoalLimitRestart,
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
	if !config.SharedAppServerEnabled {
		if !controllerFailureNeedsFailOpen(d.controllerState) {
			d.controllerState = "shared_app_server_disabled"
		}
	} else if d.controllerState == "shared_app_server_disabled" {
		d.controllerState = "starting"
	}
	for threadID, thread := range d.state.Threads {
		if thread.Awaiting != nil {
			recoveryLimit, consecutiveLimit := retryLimits(thread.Awaiting.Class, config)
			thread.Awaiting.MaxAttempts = recoveryLimit
			thread.Awaiting.MaxConsecutive = consecutiveLimit
		}
		if thread.Pending != nil {
			recoveryLimit, consecutiveLimit := retryLimits(thread.Pending.Class, config)
			thread.Pending.MaxAttempts = recoveryLimit
			thread.Pending.MaxConsecutive = consecutiveLimit
			if thread.Pending.Attempt > recoveryLimit || thread.Pending.ConsecutiveRetry > consecutiveLimit {
				d.stopPendingRetryLocked(threadID, thread, time.Now().UTC())
				continue
			}
		}
		if thread.Pending != nil || thread.Awaiting != nil {
			d.state.Threads[threadID] = thread
		}
	}
}

func (d *daemon) stopPendingRetryLocked(threadID string, thread ThreadState, now time.Time) {
	pending := thread.Pending
	if pending == nil {
		return
	}
	thread.Pending = nil
	thread.Awaiting = nil
	completedAttempts := completedRetryCount(pending.Attempt, pending.MaxAttempts)
	completedConsecutive := completedRetryCount(pending.ConsecutiveRetry, pending.MaxConsecutive)
	thread.RecoveryAttempts = completedAttempts
	thread.ConsecutiveRetries = completedConsecutive
	reason := retryStopReason(pending.Attempt, pending.MaxAttempts, pending.ConsecutiveRetry, pending.MaxConsecutive)
	if pending.Class == classEmptyResponse && thread.GoalStatus == "active" {
		reason = goalEmptyResponseStopReason
	}
	thread.Stopped = &StoppedRetry{
		EventKey: pending.EventKey, FailedTurnID: pending.FailedTurnID, FailedAt: pending.FailedAt,
		OriginTurnStartedAt: pending.OriginTurnStartedAt,
		Class:               pending.Class, StoppedAt: now, CodexHome: pending.CodexHome, RolloutPath: pending.RolloutPath,
		Attempts: completedAttempts, MaxAttempts: pending.MaxAttempts,
		ConsecutiveRetries: completedConsecutive, MaxConsecutive: pending.MaxConsecutive, Reason: reason,
	}
	if reason == goalEmptyResponseStopReason {
		thread.GoalStop = &GoalStopRequest{EventKey: pending.EventKey, Reason: reason, RequestedAt: now, DueAt: now}
	}
	d.state.Threads[threadID] = thread
	d.logger.Printf("retry exhausted thread=%s category=%s recovery_attempts=%d consecutive_retries=%d reason=%s", shortThreadID(threadID), pending.Class, completedAttempts, completedConsecutive, reason)
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
		thread.RecoveryAttempts = 0
		thread.ConsecutiveRetries = 0
		thread.CurrentTurnProgress = false
		thread.GoalStop = nil
		d.state.Threads[command.ThreadID] = thread
		d.logger.Printf("retry cancelled thread=%s reason=user_control", shortThreadID(command.ThreadID))
	case commandRestartRetry:
		if thread.Stopped == nil {
			d.logger.Printf("control command ignored thread=%s reason=retry_not_stopped", shortThreadID(command.ThreadID))
			return
		}
		stopped := thread.Stopped
		if cancel := d.activeCtx[command.ThreadID]; cancel != nil {
			cancel()
		}
		thread.Stopped = nil
		thread.GoalStop = nil
		thread.GoalHeld = false
		thread.RecoveryAttempts = 1
		thread.ConsecutiveRetries = 1
		recoveryLimit, consecutiveLimit := retryLimits(stopped.Class, d.config)
		thread.Pending = &PendingRetry{
			EventKey:            stopped.EventKey,
			FailedTurnID:        stopped.FailedTurnID,
			FailedAt:            stopped.FailedAt,
			OriginTurnStartedAt: stopped.OriginTurnStartedAt,
			Class:               stopped.Class,
			DueAt:               now,
			CodexHome:           stopped.CodexHome,
			RolloutPath:         stopped.RolloutPath,
			Attempt:             1,
			MaxAttempts:         recoveryLimit,
			ConsecutiveRetry:    1,
			MaxConsecutive:      consecutiveLimit,
			GoalLimitRestart:    isGoalEmptyResponseStopReason(stopped.Reason),
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
	case "task_user_input":
		d.handleTaskUserInputLocked(item.ThreadID, event, thread)
	case "task_progress":
		d.handleTaskProgressLocked(item.ThreadID, event, thread)
	case "task_complete":
		d.handleTaskCompleteLocked(item, key, now, thread)
	case "turn_aborted":
		d.handleTurnAbortedLocked(item.ThreadID, event, thread)
	case "thread_goal_updated":
		d.handleGoalUpdatedLocked(item.ThreadID, event, thread)
	case "subagent_recovery_notice":
		d.handleSubagentRecoveryNoticeLocked(item.ThreadID, event, thread)
	}
}

func (d *daemon) handleTaskStartedLocked(threadID string, event RelevantEvent, thread ThreadState) {
	thread.LastStartedTurnID = event.TurnID
	thread.LastStartedAt = event.Timestamp
	thread.CurrentTurnProgress = false
	if automaticGoalLimitStopped(thread) {
		d.state.Threads[threadID] = thread
		d.logger.Printf("goal turn ignored thread=%s reason=%s", shortThreadID(threadID), thread.Stopped.Reason)
		return
	}
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
		thread.LastExternalTurnID = event.TurnID
		thread.LastExternalTurnAt = event.Timestamp
		d.cancelRetryLocked(threadID, thread, "manual_task_started")
		return
	}
	if thread.Pending != nil {
		action := RetryAction("")
		if canAdoptNativeGoalTurn(thread, event) {
			action = actionGoalActive
		} else if canAdoptSubagentTurn(thread, event) {
			action = actionSubagentContinue
		}
		if action != "" {
			pending := thread.Pending
			thread.Pending = nil
			thread.LastAutoRetryAt = event.Timestamp
			thread.Awaiting = &AwaitingRetry{
				EventKey: pending.EventKey, FailedTurnID: pending.FailedTurnID,
				FailedAt: pending.FailedAt, OriginTurnStartedAt: pending.OriginTurnStartedAt,
				RetryTurnID: event.TurnID, Class: pending.Class, Action: action,
				Attempt: pending.Attempt, MaxAttempts: pending.MaxAttempts,
				ConsecutiveRetry: pending.ConsecutiveRetry, MaxConsecutive: pending.MaxConsecutive,
				DispatchFailures: pending.DispatchFailures, ParentNotified: pending.ParentNotified,
				GoalLimitRestart: pending.GoalLimitRestart, DispatchStartedAt: event.Timestamp,
				StartedAt: event.Timestamp, CodexHome: pending.CodexHome, RolloutPath: pending.RolloutPath,
			}
			d.state.Threads[threadID] = thread
			d.logger.Printf("automatic retry turn adopted thread=%s action=%s attempt=%d", shortThreadID(threadID), action, pending.Attempt)
			return
		}
		thread.LastExternalTurnID = event.TurnID
		thread.LastExternalTurnAt = event.Timestamp
		d.cancelRetryLocked(threadID, thread, "manual_task_started")
		return
	}
	thread.LastExternalTurnID = event.TurnID
	thread.LastExternalTurnAt = event.Timestamp
	thread.RecoveryAttempts = 0
	thread.ConsecutiveRetries = 0
	thread.Stopped = nil
	thread.GoalStop = nil
	d.state.Threads[threadID] = thread
}

func (d *daemon) handleTaskUserInputLocked(threadID string, event RelevantEvent, thread ThreadState) {
	if automaticGoalLimitStopped(thread) {
		return
	}
	turnID := event.TurnID
	if turnID == "" {
		turnID = thread.LastStartedTurnID
	}
	if thread.Awaiting != nil && thread.Awaiting.RetryTurnID == turnID {
		if thread.Awaiting.Action != actionGoalActive && thread.Awaiting.Action != actionGoalResume {
			return
		}
		d.cancelRetryLocked(threadID, thread, "manual_task_input")
		return
	}
	if thread.Pending != nil && turnID != "" && turnID == thread.LastStartedTurnID {
		d.cancelRetryLocked(threadID, thread, "manual_task_input")
	}
}

func (d *daemon) handleTaskProgressLocked(threadID string, event RelevantEvent, thread ThreadState) {
	// Progress matters only for the automatic retry turn currently correlated
	// in state. This prevents a late record from an older or manual turn from
	// resetting the no-progress guard for a different retry.
	if thread.Awaiting == nil || thread.Awaiting.RetryTurnID == "" ||
		thread.Awaiting.RetryTurnID != thread.LastStartedTurnID ||
		(!thread.Awaiting.StartedAt.IsZero() && event.Timestamp.Before(thread.Awaiting.StartedAt)) {
		return
	}
	thread.CurrentTurnProgress = true
	d.state.Threads[threadID] = thread
}

func (d *daemon) handleTurnAbortedLocked(threadID string, event RelevantEvent, thread ThreadState) {
	abortedTurnID := event.TurnID
	if abortedTurnID == "" {
		abortedTurnID = thread.LastStartedTurnID
	}
	thread.LastAbortedTurnID = abortedTurnID
	thread.LastAbortedAt = event.Timestamp
	if automaticGoalLimitStopped(thread) {
		d.state.Threads[threadID] = thread
		return
	}
	if cancel := d.activeCtx[threadID]; cancel != nil {
		cancel()
	}
	hadRetry := thread.Pending != nil || thread.Awaiting != nil || thread.Stopped != nil ||
		thread.RecoveryAttempts > 0 || thread.ConsecutiveRetries > 0
	thread.Pending = nil
	thread.Awaiting = nil
	thread.Stopped = nil
	thread.GoalStop = nil
	thread.RecoveryAttempts = 0
	thread.ConsecutiveRetries = 0
	thread.CurrentTurnProgress = false
	d.state.Threads[threadID] = thread
	if hadRetry {
		d.logger.Printf("retry cancelled thread=%s reason=turn_aborted", shortThreadID(threadID))
	}
}

func (d *daemon) handleTaskCompleteLocked(item scannedEvent, key string, now time.Time, thread ThreadState) {
	event := item.Event
	if automaticGoalLimitStopped(thread) {
		d.state.Threads[item.ThreadID] = thread
		d.logger.Printf("completion ignored thread=%s reason=%s", shortThreadID(item.ThreadID), thread.Stopped.Reason)
		return
	}
	if event.TurnID != "" && event.TurnID == thread.LastAbortedTurnID {
		d.resetRetryStateLocked(item.ThreadID, thread)
		d.logger.Printf("completion ignored thread=%s reason=turn_aborted", shortThreadID(item.ThreadID))
		return
	}
	if thread.Awaiting != nil {
		awaiting := thread.Awaiting
		if awaiting.RetryTurnID == "" || awaiting.RetryTurnID != event.TurnID {
			d.logger.Printf("completion ignored thread=%s reason=turn_mismatch", shortThreadID(item.ThreadID))
			return
		}
		if completionSucceeded(event) {
			d.logger.Printf("retry chain recovered thread=%s recovery_attempt=%d consecutive_retry=%d", shortThreadID(item.ThreadID), awaiting.Attempt, awaiting.ConsecutiveRetry)
			d.resetRetryStateLocked(item.ThreadID, thread)
			return
		}
		nextConsecutive := awaiting.ConsecutiveRetry + 1
		if thread.CurrentTurnProgress {
			nextConsecutive = 1
		}
		thread.CurrentTurnProgress = false
		thread.Awaiting = nil
		d.scheduleFailureLocked(item, key, now, thread, awaiting.Attempt+1, nextConsecutive, awaiting.OriginTurnStartedAt, awaiting.ParentNotified)
		return
	}

	if thread.Pending != nil {
		if thread.Pending.FailedTurnID != event.TurnID {
			d.logger.Printf("completion ignored thread=%s reason=pending_turn_mismatch", shortThreadID(item.ThreadID))
			return
		}
		if completionSucceeded(event) {
			d.resetRetryStateLocked(item.ThreadID, thread)
		}
		return
	}

	if completionSucceeded(event) {
		d.resetRetryStateLocked(item.ThreadID, thread)
		return
	}
	thread.CurrentTurnProgress = false
	d.scheduleFailureLocked(item, key, now, thread, 1, 1, externalTurnOrigin(thread, event), false)
}

func (d *daemon) scheduleFailureLocked(item scannedEvent, key string, now time.Time, thread ThreadState, recoveryAttempt, consecutiveRetry int, originTurnStartedAt time.Time, parentNotified bool) {
	if thread.GoalHeld {
		if thread.GoalStatus == "blocked" && goalBlockedByFailure(thread.GoalUpdatedAt, item.Event.Timestamp) {
			thread.GoalHeld = false
		} else if !heldConversationAllowed(thread.GoalStatus, thread.GoalUpdatedAt, originTurnStartedAt) {
			thread.Pending = nil
			thread.Awaiting = nil
			thread.RecoveryAttempts = 0
			thread.ConsecutiveRetries = 0
			thread.CurrentTurnProgress = false
			thread.LastFailureAt = item.Event.Timestamp
			d.state.Threads[item.ThreadID] = thread
			d.logger.Printf("retry skipped thread=%s category=non_retryable reason=goal_held", shortThreadID(item.ThreadID))
			return
		}
	}
	decision := classifyCompletionFailure(item.Event, d.config)
	if !decision.Retry {
		d.resetRetryStateLocked(item.ThreadID, thread)
		d.logger.Printf("retry skipped thread=%s category=non_retryable reason=%s", shortThreadID(item.ThreadID), decision.Reason)
		return
	}
	recoveryLimit, consecutiveLimit := retryLimitsForDecision(decision, d.config)
	if recoveryAttempt > recoveryLimit || consecutiveRetry > consecutiveLimit {
		completedAttempts := completedRetryCount(recoveryAttempt, recoveryLimit)
		completedConsecutive := completedRetryCount(consecutiveRetry, consecutiveLimit)
		reason := retryStopReason(recoveryAttempt, recoveryLimit, consecutiveRetry, consecutiveLimit)
		if decision.Class == classEmptyResponse && thread.GoalStatus == "active" {
			reason = goalEmptyResponseStopReason
		}
		thread.Pending = nil
		thread.Awaiting = nil
		thread.RecoveryAttempts = completedAttempts
		thread.ConsecutiveRetries = completedConsecutive
		thread.CurrentTurnProgress = false
		thread.LastFailureAt = item.Event.Timestamp
		thread.Stopped = &StoppedRetry{
			EventKey: key, FailedTurnID: item.Event.TurnID, FailedAt: item.Event.Timestamp,
			OriginTurnStartedAt: originTurnStartedAt,
			Class:               decision.Class, StoppedAt: now, CodexHome: item.Root.CodexHome, RolloutPath: item.RolloutPath,
			Attempts: completedAttempts, MaxAttempts: recoveryLimit,
			ConsecutiveRetries: completedConsecutive, MaxConsecutive: consecutiveLimit, Reason: reason,
		}
		if reason == goalEmptyResponseStopReason {
			thread.GoalStop = &GoalStopRequest{EventKey: key, Reason: reason, RequestedAt: now, DueAt: now}
		}
		d.state.Threads[item.ThreadID] = thread
		d.logger.Printf("retry exhausted thread=%s category=%s recovery_attempts=%d consecutive_retries=%d reason=%s", shortThreadID(item.ThreadID), decision.Class, completedAttempts, completedConsecutive, reason)
		return
	}

	delay := retryDelay(consecutiveRetry, d.config)
	thread.RecoveryAttempts = recoveryAttempt
	thread.ConsecutiveRetries = consecutiveRetry
	thread.CurrentTurnProgress = false
	thread.LastFailureAt = item.Event.Timestamp
	thread.Awaiting = nil
	thread.Stopped = nil
	thread.GoalStop = nil
	thread.Pending = &PendingRetry{
		EventKey:            key,
		FailedTurnID:        item.Event.TurnID,
		FailedAt:            item.Event.Timestamp,
		OriginTurnStartedAt: originTurnStartedAt,
		Class:               decision.Class,
		DueAt:               now.Add(delay),
		CodexHome:           item.Root.CodexHome,
		RolloutPath:         item.RolloutPath,
		Attempt:             recoveryAttempt, MaxAttempts: recoveryLimit,
		ConsecutiveRetry: consecutiveRetry, MaxConsecutive: consecutiveLimit,
		ParentNotified: parentNotified,
	}
	d.state.Threads[item.ThreadID] = thread
	d.logger.Printf("retry scheduled thread=%s category=%s recovery_attempt=%d consecutive_retry=%d delay_seconds=%d", shortThreadID(item.ThreadID), decision.Class, recoveryAttempt, consecutiveRetry, int(delay.Seconds()))
}

func externalTurnOrigin(thread ThreadState, event RelevantEvent) time.Time {
	if event.TurnID == "" || event.TurnID != thread.LastExternalTurnID || thread.LastExternalTurnAt.IsZero() ||
		event.Timestamp.Before(thread.LastExternalTurnAt) {
		return time.Time{}
	}
	return thread.LastExternalTurnAt
}

func retryLimitsForDecision(decision RetryDecision, config Config) (int, int) {
	recoveryLimit := config.MaxRecoveryAttempts
	consecutiveLimit := config.MaxConsecutiveRetries
	if decision.MaxAttempts > 0 {
		if decision.MaxAttempts < recoveryLimit {
			recoveryLimit = decision.MaxAttempts
		}
	}
	if decision.MaxConsecutive > 0 {
		if decision.MaxConsecutive < consecutiveLimit {
			consecutiveLimit = decision.MaxConsecutive
		}
	} else if decision.MaxAttempts > 0 && decision.Class != classUnknown {
		// Authentication failures retain their deliberately conservative
		// per-class ceiling for both counters. Unknown provider failures are
		// different: their 3-attempt classifier guard must not silently replace
		// the user-configured no-progress ceiling shown in the panel.
		if decision.MaxAttempts < consecutiveLimit {
			consecutiveLimit = decision.MaxAttempts
		}
	}
	return recoveryLimit, consecutiveLimit
}

func retryLimits(class FailureClass, config Config) (int, int) {
	decision := RetryDecision{Class: class}
	switch class {
	case classAuthLimited:
		decision.MaxAttempts = config.AuthMaxAttempts
	case classUnknown:
		decision.MaxAttempts = config.UnknownMaxAttempts
	}
	return retryLimitsForDecision(decision, config)
}

func completedRetryCount(nextAttempt, limit int) int {
	completed := nextAttempt - 1
	if completed < 0 {
		completed = 0
	}
	if completed > limit {
		completed = limit
	}
	return completed
}

func retryStopReason(recoveryAttempt, recoveryLimit, consecutiveRetry, consecutiveLimit int) string {
	if recoveryAttempt > recoveryLimit {
		return "recovery_attempt_limit"
	}
	if consecutiveRetry > consecutiveLimit {
		return "consecutive_retry_limit"
	}
	return "retry_limit"
}

func (d *daemon) cancelRetryLocked(threadID string, thread ThreadState, reason string) {
	if cancel := d.activeCtx[threadID]; cancel != nil {
		cancel()
	}
	thread.Pending = nil
	thread.Awaiting = nil
	thread.RecoveryAttempts = 0
	thread.ConsecutiveRetries = 0
	thread.CurrentTurnProgress = false
	thread.Stopped = nil
	thread.GoalStop = nil
	d.state.Threads[threadID] = thread
	d.logger.Printf("retry cancelled thread=%s reason=%s", shortThreadID(threadID), reason)
}

func (d *daemon) resetRetryStateLocked(threadID string, thread ThreadState) {
	thread.Pending = nil
	thread.Awaiting = nil
	thread.RecoveryAttempts = 0
	thread.ConsecutiveRetries = 0
	thread.CurrentTurnProgress = false
	thread.Stopped = nil
	thread.GoalStop = nil
	d.state.Threads[threadID] = thread
}
