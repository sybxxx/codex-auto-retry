package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestManagementSnapshotIncludesIndependentCountdowns(t *testing.T) {
	dataDir := t.TempDir()
	service := newManagementService(dataDir)
	now := time.Date(2026, 7, 27, 6, 0, 0, 0, time.UTC)
	state := newRuntimeState()
	state.Threads["019f9d5d-9c82-75b1-b7c0-20a658af0423"] = ThreadState{Pending: &PendingRetry{
		Class:   classServer,
		DueAt:   now.Add(5500 * time.Millisecond),
		Attempt: 2,
	}}
	state.Threads["019f9d5d-9c82-75b1-b7c0-20a658af0424"] = ThreadState{Awaiting: &AwaitingRetry{
		Class:       classTransient,
		Action:      actionGoalResume,
		Attempt:     1,
		RetryTurnID: "retry-turn",
	}}
	if err := writeJSONAtomic(service.statePath, state); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(service.statusPath, StatusSnapshot{
		Version:      appVersion,
		Running:      true,
		LastScanAt:   now.Add(-time.Second),
		WatchedRoots: 2,
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.snapshot(now)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Running || snapshot.PendingRetries != 1 || snapshot.ActiveRetries != 1 || len(snapshot.Retries) != 2 {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	if snapshot.Retries[0].State != "running" || snapshot.Retries[1].SecondsRemaining != 6 {
		t.Fatalf("queue state or countdown is incorrect: %+v", snapshot.Retries)
	}
}

func TestManagementUpdatesPromptAndQueuesControls(t *testing.T) {
	dataDir := t.TempDir()
	service := newManagementService(dataDir)
	now := time.Date(2026, 7, 27, 6, 0, 0, 0, time.UTC)
	threadID := "019f9d5d-9c82-75b1-b7c0-20a658af0423"
	state := newRuntimeState()
	state.Threads[threadID] = ThreadState{Pending: &PendingRetry{DueAt: now.Add(time.Minute), Attempt: 1}}
	if err := writeJSONAtomic(service.statePath, state); err != nil {
		t.Fatal(err)
	}
	updated, err := service.setRetryPrompt("继续处理", now)
	if err != nil {
		t.Fatal(err)
	}
	if updated.RetryPrompt != "继续处理" {
		t.Fatalf("prompt was not updated: %+v", updated)
	}
	paused, err := service.setPaused(true, now)
	if err != nil || !paused.Paused {
		t.Fatalf("pause was not updated: snapshot=%+v err=%v", paused, err)
	}
	if _, err := service.retryNow(threadID, now); err != nil {
		t.Fatal(err)
	}
	commands, _, err := loadControlCommandFiles(service.commandDir)
	if err != nil || len(commands) != 1 || commands[0].Command.Action != commandRetryNow {
		t.Fatalf("retry-now command missing: commands=%+v err=%v", commands, err)
	}
}

func TestManagementRejectsOversizedPromptAndNonPendingCommand(t *testing.T) {
	service := newManagementService(t.TempDir())
	if _, err := service.setRetryPrompt(string(make([]rune, maxRetryPromptRunes+1)), time.Now()); err == nil {
		t.Fatal("oversized prompt was accepted")
	}
	if _, err := service.cancelRetry("019f9d5d-9c82-75b1-b7c0-20a658af0423", time.Now()); err == nil {
		t.Fatal("cancel command was queued for a task without a pending retry")
	}
	if _, err := os.Stat(filepath.Join(service.dataDir, "commands")); !os.IsNotExist(err) {
		t.Fatalf("command directory should not exist after rejection: %v", err)
	}
}
