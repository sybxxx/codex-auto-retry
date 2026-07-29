package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

func newRuntimeState() RuntimeState {
	return RuntimeState{
		Version:         4,
		Files:           make(map[string]FileCursor),
		Threads:         make(map[string]ThreadState),
		ProcessedEvents: make(map[string]time.Time),
	}
}

func loadState(path string) (RuntimeState, error) {
	state := newRuntimeState()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return newRuntimeState(), fmt.Errorf("parse state: %w", err)
	}
	if state.Files == nil {
		state.Files = make(map[string]FileCursor)
	}
	if state.Threads == nil {
		state.Threads = make(map[string]ThreadState)
	}
	if state.ProcessedEvents == nil {
		state.ProcessedEvents = make(map[string]time.Time)
	}
	state.Version = 4
	for id, thread := range state.Threads {
		if thread.RecoveryAttempts < 1 && thread.LegacyFailures > 0 {
			thread.RecoveryAttempts = thread.LegacyFailures
		}
		if thread.ConsecutiveRetries < 1 && thread.LegacyFailures > 0 {
			thread.ConsecutiveRetries = thread.LegacyFailures
		}
		thread.LegacyFailures = 0
		if thread.Pending != nil && thread.Pending.Attempt < 1 {
			thread.Pending.Attempt = thread.RecoveryAttempts
			if thread.Pending.Attempt < 1 {
				thread.Pending.Attempt = 1
			}
		}
		if thread.Pending != nil && thread.Pending.ConsecutiveRetry < 1 {
			thread.Pending.ConsecutiveRetry = thread.ConsecutiveRetries
			if thread.Pending.ConsecutiveRetry < 1 {
				thread.Pending.ConsecutiveRetry = thread.Pending.Attempt
			}
		}
		if thread.Pending != nil && thread.Pending.MaxConsecutive < 1 {
			thread.Pending.MaxConsecutive = thread.Pending.MaxAttempts
		}
		if thread.Awaiting != nil && thread.Awaiting.ConsecutiveRetry < 1 {
			thread.Awaiting.ConsecutiveRetry = thread.ConsecutiveRetries
			if thread.Awaiting.ConsecutiveRetry < 1 {
				thread.Awaiting.ConsecutiveRetry = thread.Awaiting.Attempt
			}
		}
		if thread.Awaiting != nil && thread.Awaiting.MaxConsecutive < 1 {
			thread.Awaiting.MaxConsecutive = thread.Awaiting.MaxAttempts
		}
		if thread.Stopped != nil && thread.Stopped.ConsecutiveRetries < 1 {
			thread.Stopped.ConsecutiveRetries = thread.Stopped.Attempts
		}
		if thread.Stopped != nil && thread.Stopped.MaxConsecutive < 1 {
			thread.Stopped.MaxConsecutive = thread.Stopped.MaxAttempts
		}
		if thread.Pending != nil && thread.Pending.FailedAt.IsZero() {
			thread.Pending.FailedAt = thread.LastFailureAt
		}
		if thread.Awaiting != nil && thread.Awaiting.FailedAt.IsZero() {
			thread.Awaiting.FailedAt = thread.LastFailureAt
		}
		state.Threads[id] = thread
	}
	return state, nil
}

func (s *RuntimeState) prune(now time.Time) {
	processedCutoff := now.Add(-14 * 24 * time.Hour)
	for key, seenAt := range s.ProcessedEvents {
		if seenAt.Before(processedCutoff) {
			delete(s.ProcessedEvents, key)
		}
	}
	fileCutoff := now.Add(-45 * 24 * time.Hour)
	for path, cursor := range s.Files {
		if cursor.LastSeen.Before(fileCutoff) {
			delete(s.Files, path)
		}
	}
	threadCutoff := now.Add(-30 * 24 * time.Hour)
	for id, thread := range s.Threads {
		lastActivity := thread.LastFailureAt
		if thread.GoalUpdatedAt.After(lastActivity) {
			lastActivity = thread.GoalUpdatedAt
		}
		if thread.GoalObservedAt.After(lastActivity) {
			lastActivity = thread.GoalObservedAt
		}
		if thread.LastAbortedAt.After(lastActivity) {
			lastActivity = thread.LastAbortedAt
		}
		if thread.Stopped != nil && thread.Stopped.StoppedAt.After(lastActivity) {
			lastActivity = thread.Stopped.StoppedAt
		}
		if thread.Pending == nil && thread.Awaiting == nil && !lastActivity.IsZero() && lastActivity.Before(threadCutoff) {
			delete(s.Threads, id)
		}
	}
}
