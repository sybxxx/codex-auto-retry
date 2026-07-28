//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWriteJSONAtomicSurvivesBriefWindowsReaderLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{\"version\":1}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = windows.CloseHandle(handle)
		close(released)
	}()
	t.Cleanup(func() {
		select {
		case <-released:
		default:
			_ = windows.CloseHandle(handle)
		}
	})

	started := time.Now()
	if err := writeJSONAtomic(path, map[string]int{"version": 2}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < 50*time.Millisecond {
		t.Fatalf("atomic replacement did not encounter the expected reader lock: %s", elapsed)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]int
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	if value["version"] != 2 {
		t.Fatalf("atomic replacement wrote the wrong value: %v", value)
	}
}
