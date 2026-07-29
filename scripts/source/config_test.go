package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigMigratesLegacyCliDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	legacy := `{
  "poll_interval_seconds": 2,
  "initial_delay_seconds": 5,
  "max_delay_seconds": 300,
  "max_parallel_retries": 2,
  "auth_max_attempts": 6,
  "unknown_max_attempts": 3,
  "include_cockpit_homes": true,
  "codex_executable": "C:\\old\\codex.exe",
  "retry_prompt": "` + legacyRetryPrompt + `"
}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadOrCreateConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConfigVersion != currentConfigVersion || cfg.MaxParallelRetries != 4 || cfg.StartAckTimeoutSeconds != 30 || cfg.RetryPrompt != defaultRetryPrompt {
		t.Fatalf("legacy config was not migrated: %+v", cfg)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(written), "codex_executable") ||
		!strings.Contains(string(written), "start_ack_timeout_seconds") ||
		!strings.Contains(string(written), "config_version") {
		t.Fatalf("migrated config still contains CLI settings: %s", written)
	}
}

func TestVersionTwoConfigPreservesExplicitParallelLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	config := defaultConfig()
	config.ConfigVersion = 2
	config.MaxParallelRetries = 1
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	var legacyFields map[string]any
	if err := json.Unmarshal(data, &legacyFields); err != nil {
		t.Fatal(err)
	}
	delete(legacyFields, "max_consecutive_retries")
	delete(legacyFields, "max_recovery_attempts")
	delete(legacyFields, "delay_strategy")
	delete(legacyFields, "show_notifications")
	data, err = json.Marshal(legacyFields)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadOrCreateConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ConfigVersion != currentConfigVersion || loaded.MaxParallelRetries != 1 ||
		loaded.MaxRecoveryAttempts != 15 || loaded.MaxConsecutiveRetries != 5 ||
		loaded.DelayStrategy != delayStrategyExponential || !loaded.ShowNotifications {
		t.Fatalf("version two settings were not migrated: %+v", loaded)
	}
}

func TestVersionThreeRetryLimitBecomesRecoveryBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	legacy := `{
  "config_version": 3,
  "poll_interval_seconds": 2,
  "initial_delay_seconds": 5,
  "max_delay_seconds": 1800,
  "max_retry_attempts": 15,
  "max_parallel_retries": 4,
  "start_ack_timeout_seconds": 30,
  "auth_max_attempts": 6,
  "unknown_max_attempts": 3,
  "include_default_home": true,
  "include_cockpit_homes": true,
  "retry_prompt": "继续",
  "show_notifications": true
}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadOrCreateConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.MaxRecoveryAttempts != 15 || loaded.MaxConsecutiveRetries != 5 ||
		loaded.DelayStrategy != delayStrategyExponential {
		t.Fatalf("legacy retry limit was not migrated without changing its total budget: %+v", loaded)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(written), "max_retry_attempts") ||
		!strings.Contains(string(written), "max_recovery_attempts") ||
		!strings.Contains(string(written), "max_consecutive_retries") {
		t.Fatalf("legacy retry field remained after migration: %s", written)
	}
}

func TestConfigValidatesUserVisibleRetrySettings(t *testing.T) {
	config := defaultConfig()
	config.MaxConsecutiveRetries = 0
	if err := config.validate(); err == nil {
		t.Fatal("zero consecutive retry limit was accepted")
	}
	config = defaultConfig()
	config.MaxRecoveryAttempts = 101
	if err := config.validate(); err == nil {
		t.Fatal("recovery limit above 100 was accepted")
	}
	config = defaultConfig()
	config.InitialDelaySeconds = 10
	config.MaxDelaySeconds = 9
	if err := config.validate(); err == nil {
		t.Fatal("maximum delay below initial delay was accepted")
	}
	config.DelayStrategy = delayStrategyFixed
	if err := config.validate(); err != nil {
		t.Fatalf("fixed delay incorrectly required the exponential maximum: %v", err)
	}
	config.DelayStrategy = "linear"
	if err := config.validate(); err == nil {
		t.Fatal("unknown delay strategy was accepted")
	}
}
