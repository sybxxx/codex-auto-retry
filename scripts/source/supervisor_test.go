package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSupervisorBackoffIsBounded(t *testing.T) {
	if got := nextSupervisorBackoff(time.Second); got != 2*time.Second {
		t.Fatalf("first supervisor backoff = %s", got)
	}
	if got := nextSupervisorBackoff(32 * time.Second); got != supervisorMaxBackoff {
		t.Fatalf("maximum supervisor backoff = %s", got)
	}
	if got := nextSupervisorBackoff(supervisorMaxBackoff); got != supervisorMaxBackoff {
		t.Fatalf("supervisor backoff exceeded cap: %s", got)
	}
}

func TestConsumeSupervisorStopIsOneShot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "supervisor.stop")
	if consumeSupervisorStop(path) {
		t.Fatal("missing stop marker was consumed")
	}
	if err := os.WriteFile(path, []byte("stop"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !consumeSupervisorStop(path) {
		t.Fatal("stop marker was not consumed")
	}
	if consumeSupervisorStop(path) {
		t.Fatal("stop marker was consumed twice")
	}
}

func TestWorkerStateCleanlyStoppedRequiresMatchingPID(t *testing.T) {
	dataDir := t.TempDir()
	statusPath := filepath.Join(dataDir, "status.json")
	status := StatusSnapshot{PID: 42, Running: false}
	data, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statusPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if workerStateCleanlyStopped(dataDir, 41) {
		t.Fatal("stale stopped heartbeat from another worker was accepted")
	}
	if !workerStateCleanlyStopped(dataDir, 42) {
		t.Fatal("matching stopped worker heartbeat was rejected")
	}
}
