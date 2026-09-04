package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLoggerRotatesWithinBoundedBackupCount(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxLogBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	for index := 1; index < maxLogFiles; index++ {
		if err := os.WriteFile(path+"."+itoa(index), []byte("backup"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	logger, err := newSafeLogger(path)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	if _, err := os.Stat(path + "." + itoa(maxLogFiles)); !os.IsNotExist(err) {
		t.Fatalf("logger created an unbounded backup: %v", err)
	}
	for index := 1; index < maxLogFiles; index++ {
		if _, err := os.Stat(path + "." + itoa(index)); err != nil {
			t.Fatalf("expected bounded backup %d: %v", index, err)
		}
	}
}

func itoa(value int) string {
	return strconv.Itoa(value)
}
