package main

import (
	"context"
	"time"
)

const (
	nativeGoalContinuationWindow     = 5 * time.Second
	goalEmptyResponseStopReason      = "goal_empty_response_limit"
	goalEmptyResponseBlockFailReason = "goal_empty_response_limit_block_failed"
)

func (d *daemon) handleGoalUpdatedLocked(threadID string, event RelevantEvent, thread ThreadState) {
	previousGoalStatus := thread.GoalStatus
	staleUpdate := !thread.GoalUpdatedAt.IsZero() && event.GoalUpdatedAt.Before(thread.GoalUpdatedAt)
	staleObservation := !thread.GoalObservedAt.IsZero() && !event.Timestamp.After(thread.GoalObservedAt)
	if staleUpdate || staleObservation {
		d.logger.Printf("goal update ignored thread=%s reason=stale_goal_state", shortThreadID(threadID))
		return
	}
	thread.GoalStatus = event.GoalStatus
	thread.GoalUpdatedAt = event.GoalUpdatedAt
	thread.GoalObservedAt = event.Timestamp
	if goalLimitRestartPending(thread) && event.GoalStatus == "blocked" {
		thread.GoalHeld = true
		d.state.Threads[threadID] = thread
		return
	}
	if automaticGoalLimitStopped(thread) {
		switch event.GoalStatus {
		case "active":
			if thread.Stopped.Reason == goalEmptyResponseBlockFailReason {
				thread.GoalHeld = true
				d.state.Threads[threadID] = thread
				return
			}
			if previousGoalStatus != "active" && thread.GoalStop == nil {
				thread.Stopped = nil
				thread.RecoveryAttempts = 0
				thread.ConsecutiveRetries = 0
				thread.CurrentTurnProgress = false
				thread.GoalHeld = false
				d.state.Threads[threadID] = thread
				d.logger.Printf("goal retry hold cleared thread=%s reason=explicit_goal_activation", shortThreadID(threadID))
				return
			}
			thread.GoalHeld = false
			d.state.Threads[threadID] = thread
			return
		case "blocked":
			if cancel := d.activeCtx[threadID]; cancel != nil {
				cancel()
			}
			thread.GoalStop = nil
			thread.Stopped.Reason = goalEmptyResponseStopReason
			thread.GoalHeld = true
			d.state.Threads[threadID] = thread
			return
		case "paused", "completed", "complete", "usageLimited", "budgetLimited":
			if cancel := d.activeCtx[threadID]; cancel != nil {
				cancel()
			}
			thread.GoalStop = nil
			thread.Stopped.Reason = goalEmptyResponseStopReason
			thread.GoalHeld = true
			d.state.Threads[threadID] = thread
			return
		}
	}
	thread.GoalHeld = goalRequiresHold(event.GoalStatus, event.GoalUpdatedAt, thread.LastFailureAt)
	if !thread.GoalHeld {
		d.state.Threads[threadID] = thread
		return
	}
	if heldConversationStillAllowed(thread, event.GoalStatus, event.GoalUpdatedAt) {
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
	thread.RecoveryAttempts = 0
	thread.ConsecutiveRetries = 0
	thread.CurrentTurnProgress = false
	thread.Stopped = nil
	thread.GoalStop = nil
	d.state.Threads[threadID] = thread
	if hadRetry {
		d.logger.Printf("retry cancelled thread=%s reason=goal_held", shortThreadID(threadID))
	}
}

func canAdoptNativeGoalTurn(thread ThreadState, event RelevantEvent) bool {
	pending := thread.Pending
	if pending == nil || pending.Class != classEmptyResponse || thread.GoalStatus != "active" ||
		thread.GoalHeld || event.TurnID == "" || pending.FailedAt.IsZero() || event.Timestamp.Before(pending.FailedAt) {
		return false
	}
	return !event.Timestamp.After(pending.FailedAt.Add(nativeGoalContinuationWindow))
}

func (d *daemon) runGoalBlockJob(ctx context.Context, job RetryJob) {
	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	d.mu.Lock()
	thread := d.state.Threads[job.ThreadID]
	if thread.GoalStop == nil || thread.GoalStop.EventKey != job.EventKey || !automaticGoalLimitStopped(thread) {
		delete(d.active, job.ThreadID)
		d.mu.Unlock()
		d.logger.Printf("goal stop ignored thread=%s reason=stale_job", shortThreadID(job.ThreadID))
		return
	}
	d.activeCtx[job.ThreadID] = cancel
	d.mu.Unlock()
	d.logger.Printf("goal stop started thread=%s reason=goal_empty_response_limit", shortThreadID(job.ThreadID))
	result, err := d.runner.Resume(jobCtx, job)
	finishedAt := time.Now().UTC()

	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.active, job.ThreadID)
	delete(d.activeCtx, job.ThreadID)
	thread = d.state.Threads[job.ThreadID]
	if thread.GoalStop == nil || thread.GoalStop.EventKey != job.EventKey || !automaticGoalLimitStopped(thread) {
		d.logger.Printf("goal stop ignored thread=%s reason=stale_result", shortThreadID(job.ThreadID))
		_ = d.writeStatusLocked(true, len(discoverSessionRoots(d.config)))
		return
	}
	if err != nil || result.Outcome == outcomeRetryLater || result.Outcome == outcomeUserActive {
		d.controllerState = controllerFailureReason(result, err)
		d.rescheduleGoalBlockLocked(job.ThreadID, &thread, finishedAt, controllerFailureReason(result, err))
	} else if result.Outcome == outcomeDispatched && result.Action == actionGoalBlock {
		d.controllerState = "ready"
		thread.GoalStop = nil
		thread.Stopped.Reason = goalEmptyResponseStopReason
		switch result.Reason {
		case "goal_already_paused":
			thread.GoalStatus = "paused"
		case "goal_already_completed":
			thread.GoalStatus = "completed"
		case "goal_already_usage_limited":
			thread.GoalStatus = "usageLimited"
		case "goal_already_budget_limited":
			thread.GoalStatus = "budgetLimited"
		default:
			thread.GoalStatus = "blocked"
		}
		thread.GoalObservedAt = finishedAt
		thread.GoalHeld = true
		d.logger.Printf("goal stopped thread=%s reason=goal_empty_response_limit", shortThreadID(job.ThreadID))
	} else {
		d.rescheduleGoalBlockLocked(job.ThreadID, &thread, finishedAt, "controller_invalid_result")
	}
	d.state.Threads[job.ThreadID] = thread
	if stateErr := writeJSONAtomic(d.statePath, d.state); stateErr != nil {
		d.lastError = stateErr.Error()
	}
	_ = d.writeStatusLocked(true, len(discoverSessionRoots(d.config)))
}

func (d *daemon) rescheduleGoalBlockLocked(threadID string, thread *ThreadState, now time.Time, reason string) {
	failures := thread.GoalStop.DispatchFailures + 1
	if controllerFailureNeedsAction(reason) || failures >= d.config.ControllerFailureLimit {
		thread.GoalStop = nil
		thread.GoalHeld = true
		if thread.Stopped != nil {
			if controllerFailureNeedsAction(reason) {
				thread.Stopped.Reason = reason
			} else {
				thread.Stopped.Reason = goalEmptyResponseBlockFailReason
			}
		}
		d.lastError = reason
		d.logger.Printf("goal stop failed thread=%s reason=%s", shortThreadID(threadID), reason)
		return
	}
	thread.GoalStop.DispatchFailures = failures
	thread.GoalStop.DueAt = now.Add(retryDelay(failures, d.config))
	d.logger.Printf("goal stop rescheduled thread=%s reason=%s", shortThreadID(threadID), reason)
}

func goalLimitRestartPending(thread ThreadState) bool {
	return (thread.Pending != nil && thread.Pending.GoalLimitRestart) ||
		(thread.Awaiting != nil && thread.Awaiting.GoalLimitRestart)
}

func goalHoldStatusForReason(reason string) (string, bool) {
	switch reason {
	case "goal_paused":
		return "paused", true
	case "goal_completed":
		return "completed", true
	case "goal_usage_limited":
		return "usageLimited", true
	case "goal_budget_limited":
		return "budgetLimited", true
	case "goal_blocked_before_failure":
		return "blocked", true
	case "goal_status_changed", "goal_status_unsupported":
		return "", true
	default:
		return "", false
	}
}

func heldConversationAllowed(status string, goalUpdatedAt, originTurnStartedAt time.Time) bool {
	if originTurnStartedAt.IsZero() || (status != "paused" && status != "blocked") {
		return false
	}
	// Older state can know that the goal is held without having its timestamp.
	// The live controller performs the authoritative timestamp check before any
	// continuation starts.
	return goalUpdatedAt.IsZero() || !goalUpdatedAt.After(originTurnStartedAt)
}

func heldConversationStillAllowed(thread ThreadState, status string, goalUpdatedAt time.Time) bool {
	origin := time.Time{}
	if thread.Pending != nil {
		origin = thread.Pending.OriginTurnStartedAt
	} else if thread.Awaiting != nil {
		origin = thread.Awaiting.OriginTurnStartedAt
	}
	return heldConversationAllowed(status, goalUpdatedAt, origin)
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

func automaticGoalLimitStopped(thread ThreadState) bool {
	return thread.Stopped != nil && isGoalEmptyResponseStopReason(thread.Stopped.Reason)
}

func isGoalEmptyResponseStopReason(reason string) bool {
	return reason == goalEmptyResponseStopReason || reason == goalEmptyResponseBlockFailReason
}
