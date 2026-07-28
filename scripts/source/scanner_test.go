package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGoalEventUsesPayloadThreadInsteadOfCarrierFile(t *testing.T) {
	carrierThreadID := "019f8944-588c-7e22-898b-cf7caa2f1f65"
	path := filepath.Join(t.TempDir(), "rollout-2026-07-27T23-50-53-"+carrierThreadID+".jsonl")
	line := makeGoalEventLine(t, time.Now().UTC(), "paused", time.Now().Unix(), "ignored")
	if err := os.WriteFile(path, line, 0o600); err != nil {
		t.Fatal(err)
	}
	events, _, err := readAppendedEvents(path, 0, carrierThreadID, sessionRoot{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ThreadID != goalEventThreadID {
		t.Fatalf("goal event was assigned to its carrier file instead of its target: %+v", events)
	}
}
