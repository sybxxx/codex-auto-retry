package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverSessionRootsIncludesArchivedDirectory(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "archived_sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	roots := discoverSessionRoots(Config{SessionRoots: []string{home}})
	if len(roots) != 1 || roots[0].ArchivedSessions == "" || roots[0].Sessions != "" {
		t.Fatalf("archived-only Codex home was not discovered: %+v", roots)
	}
}
