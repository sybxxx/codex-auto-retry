package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadStateMigratesLegacyRetryCounter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := `{
  "version": 3,
  "initialized": true,
  "files": {},
  "processed_events": {},
  "threads": {
    "019fa94e-0103-7183-b405-36bd307b6db2": {
      "consecutive_failures": 4,
      "pending": {
        "event_key": "legacy-event",
        "turn_id": "legacy-turn",
        "class": "server",
        "due_at": "2026-07-29T00:00:00Z",
        "codex_home": "C:\\\\legacy",
        "attempt": 4,
        "max_attempts": 15
      }
    }
  }
}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	thread := state.Threads["019fa94e-0103-7183-b405-36bd307b6db2"]
	if state.Version != 5 || thread.RecoveryAttempts != 4 || thread.ConsecutiveRetries != 4 ||
		thread.LegacyFailures != 0 || thread.Pending == nil || thread.Pending.ConsecutiveRetry != 4 ||
		thread.Pending.MaxConsecutive != 15 {
		t.Fatalf("legacy retry counter was not migrated into both conservative counters: %+v", thread)
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "consecutive_failures") {
		t.Fatal("legacy retry counter remained in migrated state")
	}
}

func TestLoadStateMigratesCurrentCodexThreadAndTurnFilenameIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	threadID := "01a09113-348b-7381-bbba-3c7a3d8d0219"
	turnID := "01a09370-a5fc-7d12-970d-544e9c43f612"
	rollout := filepath.Join(`C:\Users\test\.codex\sessions\2026\09\12`,
		"rollout-2026-09-12T10-27-08-"+threadID+"_"+turnID+".jsonl")
	legacy, err := json.Marshal(map[string]any{
		"version": 5, "initialized": true, "files": map[string]any{},
		"processed_events": map[string]string{
			turnID + "|task_complete|turn|2026-09-12T02:27:22Z|abc": "2026-09-12T02:27:22Z",
		},
		"threads": map[string]any{turnID: map[string]any{
			"last_failure_at": "2026-09-12T02:27:22Z",
			"pending": map[string]any{
				"event_key": turnID + "|task_complete|turn|2026-09-12T02:27:22Z|abc",
				"turn_id":   "turn", "failed_at": "2026-09-12T02:27:22Z", "class": "server",
				"due_at": "2026-09-12T02:27:48Z", "codex_home": `C:\Users\test\.codex`,
				"rollout_path": rollout, "attempt": 1, "max_attempts": 15,
				"consecutive_retry": 1, "max_consecutive_retries": 5,
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := state.Threads[turnID]; found {
		t.Fatal("turn UUID remained as a persisted task ID")
	}
	thread, found := state.Threads[threadID]
	if !found || thread.Pending == nil || thread.Pending.RolloutPath != rollout {
		t.Fatalf("thread state was not migrated to the session UUID: %+v", state.Threads)
	}
	for key := range state.ProcessedEvents {
		if strings.HasPrefix(key, turnID+"|") {
			t.Fatalf("processed event still uses turn UUID: %s", key)
		}
	}
}
