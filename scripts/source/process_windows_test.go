//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProcessOwnsRuntimeRejectsReusedPIDFromAnotherExecutable(t *testing.T) {
	dataDir := t.TempDir()
	if !processOwnsRuntime(os.Getpid(), dataDir) {
		t.Fatal("live test process was not accepted when no installed runtime path exists")
	}
	if err := os.WriteFile(filepath.Join(dataDir, "codex-auto-retry.exe"), []byte("not this process"), 0o600); err != nil {
		t.Fatal(err)
	}
	if processOwnsRuntime(os.Getpid(), dataDir) {
		t.Fatal("PID owned by another executable was accepted as the watchdog")
	}
}
