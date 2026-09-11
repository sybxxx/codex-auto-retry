package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestFindThreadParentIDReadsOnlySessionMetadata(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "sessions", "2026", "09", "rollout-2026-09-12T00-00-00-019fa94e-0103-7183-b405-36bd307b6dc0.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	metadata, _ := json.Marshal(map[string]any{
		"type": "session_meta", "payload": map[string]any{"parent_thread_id": "019fa94e-0103-7183-b405-36bd307b6dcb"},
	})
	content := append(metadata, '\n')
	content = append(content, []byte(`{"type":"response_item","payload":{"type":"message","content":[{"text":"parent_thread_id is not metadata"}]}}`)...)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	parent, err := findThreadParentID(home, "019fa94e-0103-7183-b405-36bd307b6dc0")
	if err != nil || parent != "019fa94e-0103-7183-b405-36bd307b6dcb" {
		t.Fatalf("parent=%q err=%v", parent, err)
	}
}

func TestFindThreadParentIDLeavesOrdinaryTaskUnowned(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "sessions", "2026", "09", "rollout-2026-09-12T00-00-00-019fa94e-0103-7183-b405-36bd307b6dc1.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"type":"session_meta","payload":{"source":"user"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	parent, err := findThreadParentID(home, "019fa94e-0103-7183-b405-36bd307b6dc1")
	if err != nil || parent != "" {
		t.Fatalf("ordinary task parent=%q err=%v", parent, err)
	}
}
