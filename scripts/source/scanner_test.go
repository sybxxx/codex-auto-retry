package main

import (
	"encoding/json"
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

func TestRecoveryNoticeRoutesFromDeclaredParentToExistingChild(t *testing.T) {
	parentID := "019fa94e-0103-7183-b405-36bd307b6db7"
	childID := "019fa94e-0103-7183-b405-36bd307b6db8"
	path := filepath.Join(t.TempDir(), "rollout-2026-07-27T23-50-53-"+parentID+".jsonl")
	notice := recoveryNoticePrefix + `{"parent_thread_id":"` + parentID + `","child_thread_id":"` + childID +
		`","recovery_event_id":"car-0123456789abcdef01234567","action":"resume_existing_child","spawn_replacement":false}`
	line := []byte(`{"timestamp":"2026-07-27T23:50:53Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":`)
	encoded, err := json.Marshal(notice)
	if err != nil {
		t.Fatal(err)
	}
	line = append(line, encoded...)
	line = append(line, []byte(`}]}}`+"\n")...)
	if err := os.WriteFile(path, line, 0o600); err != nil {
		t.Fatal(err)
	}
	events, _, err := readAppendedEvents(path, 0, parentID, sessionRoot{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ThreadID != childID || events[0].Event.ParentThreadID != parentID {
		t.Fatalf("recovery notice did not route to the child: %+v", events)
	}

	events, _, err = readAppendedEvents(path, 0, "019fa94e-0103-7183-b405-36bd307b6db9", sessionRoot{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("notice from a mismatched carrier was accepted: %+v", events)
	}
}
