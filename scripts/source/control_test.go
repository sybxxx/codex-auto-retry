package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestControlStatePersistsPause(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.json")
	now := time.Date(2026, 7, 27, 5, 0, 0, 0, time.UTC)
	written, err := saveControlState(path, true, now)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := loadOrCreateControlState(path)
	if err != nil {
		t.Fatal(err)
	}
	if !written.Paused || !loaded.Paused || !loaded.UpdatedAt.Equal(now) {
		t.Fatalf("pause state did not round trip: %+v", loaded)
	}
}

func TestControlCommandQueueValidatesAndOrdersCommands(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "commands")
	threadID := "019f9d5d-9c82-75b1-b7c0-20a658af0423"
	now := time.Date(2026, 7, 27, 5, 0, 0, 0, time.UTC)
	if _, err := queueControlCommand(directory, commandRetryNow, threadID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := queueControlCommand(directory, commandCancelRetry, threadID, now); err != nil {
		t.Fatal(err)
	}
	commands, invalid, err := loadControlCommandFiles(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(invalid) != 0 || len(commands) != 2 {
		t.Fatalf("unexpected queued commands: valid=%d invalid=%d", len(commands), len(invalid))
	}
	if commands[0].Command.Action != commandCancelRetry || commands[1].Command.Action != commandRetryNow {
		t.Fatalf("commands were not ordered by creation filename: %+v", commands)
	}
	if _, err := queueControlCommand(directory, commandRetryNow, "not-a-thread", now); err == nil {
		t.Fatal("invalid thread ID was accepted")
	}
	if err := os.WriteFile(filepath.Join(directory, "invalid.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, invalid, err = loadControlCommandFiles(directory)
	if err != nil || len(invalid) != 1 {
		t.Fatalf("invalid command was not isolated: invalid=%v err=%v", invalid, err)
	}
}
