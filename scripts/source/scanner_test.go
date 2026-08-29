package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestScanSessionsFindsArchivedSessionsAndMovesCursorByThreadID(t *testing.T) {
	home := t.TempDir()
	sessions := filepath.Join(home, "sessions", "2026", "08", "29")
	archived := filepath.Join(home, "archived_sessions", "2026", "08", "29")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(archived, 0o755); err != nil {
		t.Fatal(err)
	}
	threadID := "019fa7fc-5f97-7453-94a7-4ec248951038"
	fileName := "rollout-2026-08-29T00-00-00-" + threadID + ".jsonl"
	original := filepath.Join(sessions, fileName)
	line := []byte(`{"timestamp":"2026-08-29T00:00:00Z","type":"event_msg","payload":{"type":"turn_aborted","turn_id":"turn-1","reason":"provider"}}` + "\n")
	if err := os.WriteFile(original, line, 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 29, 1, 0, 0, 0, time.UTC)
	state := newRuntimeState()
	state.Initialized = true
	state.Files[strings.ToLower(filepath.Clean(original))] = FileCursor{Offset: int64(len(line)), LastSeen: now}
	archivedPath := filepath.Join(archived, fileName)
	if err := os.Rename(original, archivedPath); err != nil {
		t.Fatal(err)
	}
	appended := []byte(`{"timestamp":"2026-08-29T01:00:00Z","type":"event_msg","payload":{"type":"turn_aborted","turn_id":"turn-2","reason":"provider"}}` + "\n")
	if err := os.WriteFile(archivedPath, append(line, appended...), 0o600); err != nil {
		t.Fatal(err)
	}
	root := sessionRoot{Sessions: filepath.Join(home, "sessions"), ArchivedSessions: filepath.Join(home, "archived_sessions"), CodexHome: home}
	events, err := scanSessions([]sessionRoot{root}, &state, now.Add(time.Minute), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ThreadID != threadID || events[0].RolloutPath != archivedPath {
		t.Fatalf("archived append was not discovered: %+v", events)
	}
	if _, found := state.Files[strings.ToLower(filepath.Clean(original))]; found {
		t.Fatal("old sessions cursor was not retired after archive move")
	}
	cursor, found := state.Files[strings.ToLower(filepath.Clean(archivedPath))]
	if !found || cursor.Offset != int64(len(line)+len(appended)) {
		t.Fatalf("archived cursor was not advanced: found=%v cursor=%+v", found, cursor)
	}
}
