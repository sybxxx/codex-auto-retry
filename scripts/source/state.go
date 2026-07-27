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
		Version:         2,
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
	state.Version = 2
	for id, thread := range state.Threads {
		if thread.Pending != nil && thread.Pending.Attempt < 1 {
			thread.Pending.Attempt = thread.ConsecutiveFailures
			if thread.Pending.Attempt < 1 {
				thread.Pending.Attempt = 1
			}
			state.Threads[id] = thread
		}
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
		if thread.Pending == nil && thread.Awaiting == nil && !thread.LastFailureAt.IsZero() && thread.LastFailureAt.Before(threadCutoff) {
			delete(s.Threads, id)
		}
	}
}
