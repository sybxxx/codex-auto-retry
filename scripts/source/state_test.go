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
