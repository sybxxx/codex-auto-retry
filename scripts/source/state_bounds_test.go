package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRuntimeStatePruneAppliesHardCollectionBounds(t *testing.T) {
	now := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	state := newRuntimeState()
	for i := 0; i < maxProcessedEventEntries+100; i++ {
		state.ProcessedEvents[fmt.Sprintf("event-%06d", i)] = now.Add(-time.Duration(i) * time.Second)
	}
	for i := 0; i < maxFileCursorEntries+100; i++ {
		state.Files[fmt.Sprintf("rollout-%06d.jsonl", i)] = FileCursor{LastSeen: now.Add(-time.Duration(i) * time.Second)}
	}
	for i := 0; i < maxThreadEntries+100; i++ {
		state.Threads[fmt.Sprintf("thread-%06d", i)] = ThreadState{LastFailureAt: now.Add(-time.Duration(i) * time.Second)}
	}
	state.prune(now)
	if len(state.ProcessedEvents) > maxProcessedEventEntries || len(state.Files) > maxFileCursorEntries || len(state.Threads) > maxThreadEntries {
		t.Fatalf("state collection bounds were not applied: events=%d files=%d threads=%d", len(state.ProcessedEvents), len(state.Files), len(state.Threads))
	}
	if _, ok := state.ProcessedEvents[fmt.Sprintf("event-%06d", maxProcessedEventEntries+99)]; ok {
		t.Fatal("oldest processed event was not pruned first")
	}
}

func TestRuntimeStateWriterRejectsOversizedState(t *testing.T) {
	state := newRuntimeState()
	state.Files[strings.Repeat("x", maxRuntimeStateBytes)] = FileCursor{}
	if err := writeRuntimeStateAtomic(t.TempDir()+"/state.json", state); err == nil {
		t.Fatal("oversized runtime state was accepted")
	}
}
