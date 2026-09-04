package main

import (
	"path/filepath"
	"sync"
	"testing"
)

func TestConcurrentConfigUpdatesPreserveUnrelatedFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if _, err := updateConfigFile(path, func(config *Config) error { return nil }); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		if _, err := updateConfigFile(path, func(config *Config) error {
			config.RetryPrompt = "并发更新 A"
			return nil
		}); err != nil {
			t.Errorf("prompt update failed: %v", err)
		}
	}()
	go func() {
		defer wait.Done()
		if _, err := updateConfigFile(path, func(config *Config) error {
			config.InitialDelaySeconds = 9
			return nil
		}); err != nil {
			t.Errorf("delay update failed: %v", err)
		}
	}()
	wait.Wait()
	config, err := loadOrCreateConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.RetryPrompt != "并发更新 A" || config.InitialDelaySeconds != 9 {
		t.Fatalf("concurrent config updates lost unrelated fields: %+v", config)
	}
}
