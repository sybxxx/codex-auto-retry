package main

import "time"

func (d *daemon) recordControllerSuccessLocked() {
	if d.config.SharedAppServerEnabled && d.controllerState != "official_ipc_ready" {
		d.controllerState = "ready"
	}
}

// Only a known pre-dispatch stop may rejoin an automatic chain. Ambiguous
// dispatch failures, user stops and exhausted budgets always require review.
func canReopenTransportRetry(thread ThreadState, now time.Time) bool {
	s := thread.Stopped
	if s == nil || s.Historical || thread.Pending != nil || thread.Awaiting != nil ||
		thread.GoalStop != nil || thread.GoalHeld {
		return false
	}
	if s.Reason != "codex_restart_required" && s.Reason != "shared_app_server_disabled" {
		return false
	}
	if thread.GoalStatus != "" && thread.GoalStatus != "active" {
		return false
	}
	start := thread.RecoveryStartedAt
	if start.IsZero() {
		start = s.FailedAt
	}
	if start.IsZero() || now.Before(start) || now.Sub(start) > maxAutomaticRecoveryDuration {
		return false
	}
	if s.FailedAt.IsZero() || now.Before(s.FailedAt) || now.Sub(s.FailedAt) > maxAutomaticRecoveryDuration {
		return false
	}
	if s.Reason == "shared_app_server_disabled" {
		// Older versions recorded zero-attempt pre-dispatch stops without the
		// marker. Adopt only recent failures with the same completed turn.
		legacyUnsent := s.TransportBlocked == nil && s.Attempts == 0 && s.ConsecutiveRetries == 0 &&
			!thread.LastAutoRetryAt.After(s.FailedAt)
		knownUnsent := s.TransportBlocked != nil && *s.TransportBlocked
		if (!knownUnsent && !legacyUnsent) || s.FailedTurnID == "" ||
			s.FailedAt.IsZero() || thread.LastStartedTurnID != s.FailedTurnID ||
			thread.LastStartedAt.After(s.FailedAt) {
			return false
		}
	}
	if thread.LastStartedAt.After(s.FailedAt) || thread.LastExternalTurnAt.After(s.FailedAt) ||
		(!thread.LastAbortedAt.IsZero() && !thread.LastAbortedAt.Before(s.FailedAt)) ||
		(s.FailedTurnID != "" && thread.LastAbortedTurnID == s.FailedTurnID) {
		return false
	}
	return true
}
