package main

import (
	"fmt"
	"path/filepath"
	"time"
)

const configLockTimeout = 10 * time.Second

// withConfigFileLock serializes every read-modify-write of config.json across
// the worker, MCP process, tray settings helper, and installer. The lock is a
// separate file so replacing config.json atomically never invalidates a lock
// held by another process.
func withConfigFileLock(configPath string, fn func() error) error {
	if fn == nil {
		return fmt.Errorf("config lock callback is nil")
	}
	lock, err := acquireConfigFileLock(filepath.Clean(configPath)+".lock", configLockTimeout)
	if err != nil {
		return err
	}
	defer lock.Close()
	return fn()
}

// updateConfigFile loads the latest configuration while holding the
// cross-process lock, applies one mutation, validates it, and atomically
// replaces the file. Callers never write a stale snapshot over a newer one.
func updateConfigFile(path string, mutate func(*Config) error) (Config, error) {
	var config Config
	err := withConfigFileLock(path, func() error {
		loaded, err := loadOrCreateConfigUnlocked(path)
		if err != nil {
			return err
		}
		if mutate == nil {
			return fmt.Errorf("config mutation callback is nil")
		}
		if err := mutate(&loaded); err != nil {
			return err
		}
		if err := loaded.validate(); err != nil {
			return err
		}
		if err := writeJSONAtomic(path, loaded); err != nil {
			return err
		}
		config = loaded
		return nil
	})
	return config, err
}
