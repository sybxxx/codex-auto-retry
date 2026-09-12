package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"
)

const (
	maxProcessedEventEntries = 20000
	maxFileCursorEntries     = 2000
	maxThreadEntries         = 500
	maxRuntimeStateBytes     = 8 * 1024 * 1024
)

func newRuntimeState() RuntimeState {
	return RuntimeState{
		Version:         5,
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
	if len(data) > maxRuntimeStateBytes {
		return state, fmt.Errorf("runtime state exceeds %d bytes", maxRuntimeStateBytes)
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
	migrateCodex153ThreadIDs(&state)
	state.Version = 5
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
		if thread.GoalStop != nil && (thread.Stopped == nil || thread.GoalStop.EventKey != thread.Stopped.EventKey) {
			thread.GoalStop = nil
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
		if thread.GoalStop != nil && thread.GoalStop.RequestedAt.After(lastActivity) {
			lastActivity = thread.GoalStop.RequestedAt
		}
		if thread.Pending == nil && thread.Awaiting == nil && !lastActivity.IsZero() && lastActivity.Before(threadCutoff) {
			delete(s.Threads, id)
		}
	}
	trimProcessedEvents(s.ProcessedEvents, maxProcessedEventEntries)
	trimFileCursors(s.Files, maxFileCursorEntries)
	trimInactiveThreads(s.Threads, maxThreadEntries, now)
}

func trimProcessedEvents(events map[string]time.Time, limit int) {
	if len(events) <= limit {
		return
	}
	type entry struct {
		key string
		at  time.Time
	}
	entries := make([]entry, 0, len(events))
	for key, at := range events {
		entries = append(entries, entry{key: key, at: at})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].at.Equal(entries[j].at) {
			return entries[i].key < entries[j].key
		}
		return entries[i].at.Before(entries[j].at)
	})
	for _, item := range entries[:len(entries)-limit] {
		delete(events, item.key)
	}
}

func trimFileCursors(files map[string]FileCursor, limit int) {
	if len(files) <= limit {
		return
	}
	type entry struct {
		path string
		at   time.Time
	}
	entries := make([]entry, 0, len(files))
	for path, cursor := range files {
		entries = append(entries, entry{path: path, at: cursor.LastSeen})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].at.Equal(entries[j].at) {
			return entries[i].path < entries[j].path
		}
		return entries[i].at.Before(entries[j].at)
	})
	for _, item := range entries[:len(entries)-limit] {
		delete(files, item.path)
	}
}

func trimInactiveThreads(threads map[string]ThreadState, limit int, now time.Time) {
	if len(threads) <= limit {
		return
	}
	type entry struct {
		id string
		at time.Time
	}
	entries := make([]entry, 0, len(threads))
	for id, thread := range threads {
		// Never discard a retry that can still run or a recent stopped entry.
		if thread.Pending != nil || thread.Awaiting != nil {
			continue
		}
		at := thread.LastFailureAt
		if thread.GoalUpdatedAt.After(at) {
			at = thread.GoalUpdatedAt
		}
		if thread.GoalObservedAt.After(at) {
			at = thread.GoalObservedAt
		}
		if thread.LastAbortedAt.After(at) {
			at = thread.LastAbortedAt
		}
		if thread.Stopped != nil && thread.Stopped.StoppedAt.After(at) {
			at = thread.Stopped.StoppedAt
		}
		if thread.GoalStop != nil && thread.GoalStop.RequestedAt.After(at) {
			at = thread.GoalStop.RequestedAt
		}
		if thread.Stopped != nil && !thread.Stopped.Historical &&
			(thread.Stopped.StoppedAt.IsZero() || now.Sub(thread.Stopped.StoppedAt) <= stoppedRetryDisplayWindow) {
			continue
		}
		entries = append(entries, entry{id: id, at: at})
	}
	if len(threads)-len(entries) >= limit {
		// Active and visible stopped entries already consume the entire budget.
		return
	}
	removeCount := len(threads) - limit
	if removeCount > len(entries) {
		removeCount = len(entries)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].at.Equal(entries[j].at) {
			return entries[i].id < entries[j].id
		}
		return entries[i].at.Before(entries[j].at)
	})
	for _, item := range entries[:removeCount] {
		delete(threads, item.id)
	}
}

func writeRuntimeStateAtomic(path string, state RuntimeState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(data) > maxRuntimeStateBytes {
		return fmt.Errorf("runtime state exceeds %d bytes", maxRuntimeStateBytes)
	}
	return writeJSONAtomic(path, state)
}
