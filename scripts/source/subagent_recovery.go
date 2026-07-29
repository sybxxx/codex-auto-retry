package main

func canAdoptSubagentTurn(thread ThreadState, event RelevantEvent) bool {
	pending := thread.Pending
	return pending != nil && pending.ParentNotified && pending.Class == classEmptyResponse &&
		event.TurnID != "" && !pending.FailedAt.IsZero() && !event.Timestamp.Before(pending.FailedAt)
}

func (d *daemon) handleSubagentRecoveryNoticeLocked(threadID string, event RelevantEvent, thread ThreadState) {
	markPending := thread.Pending != nil && event.RecoveryEventID == recoveryEventID(threadID, thread.Pending.EventKey)
	markAwaiting := thread.Awaiting != nil && event.RecoveryEventID == recoveryEventID(threadID, thread.Awaiting.EventKey)
	if !markPending && !markAwaiting {
		return
	}
	if markPending {
		thread.Pending.ParentNotified = true
	}
	if markAwaiting {
		thread.Awaiting.ParentNotified = true
	}
	d.state.Threads[threadID] = thread
	d.logger.Printf("subagent recovery notice confirmed thread=%s", shortThreadID(threadID))
}
