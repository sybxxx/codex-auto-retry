package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	atomicReplaceAttempts = 12
	atomicReplaceMaxDelay = 250 * time.Millisecond
)

func writeJSONAtomic(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temp, err := os.CreateTemp(filepath.Dir(path), ".codex-auto-retry-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := replaceFileWithRetry(tempPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", filepath.Base(path), err)
	}
	return nil
}

func replaceFileWithRetry(source, destination string) error {
	delay := 15 * time.Millisecond
	var err error
	for attempt := 1; attempt <= atomicReplaceAttempts; attempt++ {
		err = os.Rename(source, destination)
		if err == nil {
			return nil
		}
		if attempt == atomicReplaceAttempts {
			break
		}
		time.Sleep(delay)
		if delay < atomicReplaceMaxDelay {
			delay *= 2
			if delay > atomicReplaceMaxDelay {
				delay = atomicReplaceMaxDelay
			}
		}
	}
	return err
}
