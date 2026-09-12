package main

import (
	"strings"
	"time"
)

// migrateCodex153ThreadIDs repairs state written by the pre-0.7.10 scanner.
// That scanner treated the turn UUID in <thread>_<turn>.jsonl as the task ID,
// creating one queue entry per turn. Only the filename metadata is used here;
// no conversation content is read.
func migrateCodex153ThreadIDs(state *RuntimeState) {
	if state == nil || len(state.Threads) == 0 {
		return
	}
	aliases := make(map[string]string)
	for id, thread := range state.Threads {
		threadID, turnID := rolloutIDsFromPath(threadRolloutPath(thread))
		if threadID == "" || turnID == "" || !strings.EqualFold(id, turnID) || strings.EqualFold(id, threadID) {
			continue
		}
		aliases[strings.ToLower(id)] = threadID
	}
	if len(aliases) == 0 {
		return
	}

	normalized := make(map[string]ThreadState, len(state.Threads))
	for id, thread := range state.Threads {
		target := aliases[strings.ToLower(id)]
		if target == "" {
			target = strings.ToLower(id)
		}
		if existing, found := normalized[target]; found {
			normalized[target] = mergeMigratedThreadState(existing, thread)
		} else {
			normalized[target] = thread
		}
	}
	state.Threads = normalized

	if len(state.ProcessedEvents) == 0 {
		return
	}
	for key, seenAt := range state.ProcessedEvents {
		separator := strings.IndexByte(key, '|')
		if separator <= 0 {
			continue
		}
		oldID := strings.ToLower(key[:separator])
		newID := aliases[oldID]
		if newID == "" {
			continue
		}
		newKey := newID + key[separator:]
		if previous, exists := state.ProcessedEvents[newKey]; !exists || seenAt.After(previous) {
			state.ProcessedEvents[newKey] = seenAt
		}
		delete(state.ProcessedEvents, key)
	}
}

func threadRolloutPath(thread ThreadState) string {
	if thread.Pending != nil && thread.Pending.RolloutPath != "" {
		return thread.Pending.RolloutPath
	}
	if thread.Awaiting != nil && thread.Awaiting.RolloutPath != "" {
		return thread.Awaiting.RolloutPath
	}
	if thread.Stopped != nil && thread.Stopped.RolloutPath != "" {
		return thread.Stopped.RolloutPath
	}
	return ""
}

func mergeMigratedThreadState(left, right ThreadState) ThreadState {
	leftRank, leftAt := migratedThreadStateRank(left)
	rightRank, rightAt := migratedThreadStateRank(right)
	if rightRank > leftRank || (rightRank == leftRank && rightAt.After(leftAt)) {
		return right
	}
	return left
}

func migratedThreadStateRank(thread ThreadState) (int, time.Time) {
	// Active queue state wins over a historical stopped record. Within the same
	// state, the newest lifecycle timestamp is the authoritative one.
	if thread.Awaiting != nil {
		at := thread.Awaiting.DispatchStartedAt
		if at.IsZero() {
			at = thread.Awaiting.StartedAt
		}
		return 3, at
	}
	if thread.Pending != nil {
		at := thread.Pending.FailedAt
		if at.IsZero() {
			at = thread.Pending.DueAt
		}
		return 2, at
	}
	if thread.Stopped != nil {
		return 1, thread.Stopped.StoppedAt
	}
	return 0, thread.LastFailureAt
}
