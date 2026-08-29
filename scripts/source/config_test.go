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
	if cfg.ConfigVersion != currentConfigVersion || cfg.MaxParallelRetries != 4 || cfg.StartAckTimeoutSeconds != 30 || cfg.MemoryLimitMB != defaultConfig().MemoryLimitMB || cfg.RetryPrompt != defaultRetryPrompt {
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
	delete(legacyFields, "delay_increment_seconds")
	delete(legacyFields, "shared_app_server_port")
	delete(legacyFields, "controller_failure_limit")
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
		loaded.DelayStrategy != delayStrategyExponential || loaded.DelayIncrementSeconds != 2 ||
		loaded.SharedAppServerPort != defaultConfig().SharedAppServerPort ||
		loaded.ControllerFailureLimit != defaultConfig().ControllerFailureLimit || !loaded.ShowNotifications {
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
		loaded.DelayStrategy != delayStrategyExponential || loaded.DelayIncrementSeconds != 2 {
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
	config.MaxConsecutiveRetries = maxConsecutiveRetriesLimit
	config.MaxRecoveryAttempts = maxRecoveryAttemptsLimit
	if err := config.validate(); err != nil {
		t.Fatalf("documented retry limit maximums were rejected: %v", err)
	}
	config.MaxConsecutiveRetries = maxConsecutiveRetriesLimit + 1
	if err := config.validate(); err == nil {
		t.Fatal("consecutive retry limit above the documented maximum was accepted")
	}
	config = defaultConfig()
	config.MemoryLimitMB = minMemoryLimitMB - 1
	if err := config.validate(); err == nil {
		t.Fatal("memory limit below the documented minimum was accepted")
	}
	config = defaultConfig()
	config.MemoryLimitMB = maxMemoryLimitMB + 1
	if err := config.validate(); err == nil {
		t.Fatal("memory limit above the documented maximum was accepted")
	}
	config = defaultConfig()
	config.MaxRecoveryAttempts = maxRecoveryAttemptsLimit + 1
	if err := config.validate(); err == nil {
		t.Fatal("recovery limit above the documented maximum was accepted")
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
	config.DelayStrategy = delayStrategyLinear
	config.MaxDelaySeconds = 9
	if err := config.validate(); err == nil {
		t.Fatal("linear delay accepted a maximum below the initial delay")
	}
	config.MaxDelaySeconds = 10
	config.DelayIncrementSeconds = 0
	if err := config.validate(); err == nil {
		t.Fatal("linear delay accepted a zero increment")
	}
	config.DelayIncrementSeconds = 2
	if err := config.validate(); err != nil {
		t.Fatalf("linear delay settings were rejected: %v", err)
	}
	config.DelayStrategy = "unknown"
	if err := config.validate(); err == nil {
		t.Fatal("unknown delay strategy was accepted")
	}
}

func TestVersionFiveMigrationPreservesInstalledRetryPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := []byte(`{
  "config_version": 5,
  "poll_interval_seconds": 2,
  "initial_delay_seconds": 3,
  "max_delay_seconds": 1800,
  "delay_increment_seconds": 10,
  "delay_strategy": "fixed",
  "max_consecutive_retries": 100,
  "max_recovery_attempts": 1000,
  "max_parallel_retries": 4,
  "start_ack_timeout_seconds": 30,
  "auth_max_attempts": 6,
  "unknown_max_attempts": 3,
  "session_roots": null,
  "include_default_home": true,
  "include_cockpit_homes": true,
  "retry_prompt": "继续",
  "show_notifications": true
}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadOrCreateConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ConfigVersion != currentConfigVersion || loaded.InitialDelaySeconds != 3 ||
		loaded.MaxDelaySeconds != 1800 || loaded.DelayIncrementSeconds != 10 ||
		loaded.DelayStrategy != delayStrategyFixed || loaded.MaxConsecutiveRetries != 100 ||
		loaded.MaxRecoveryAttempts != 1000 || loaded.RetryPrompt != "继续" ||
		loaded.SharedAppServerPort != defaultConfig().SharedAppServerPort || loaded.ControllerFailureLimit != 3 {
		t.Fatalf("version five migration changed the installed retry policy: %+v", loaded)
	}
	if loaded.SharedAppServerEnabled {
		t.Fatal("shared app-server mode must remain disabled when migrating an older config")
	}
}

func TestSharedAppServerIsOptInByDefault(t *testing.T) {
	config := defaultConfig()
	if config.SharedAppServerEnabled {
		t.Fatal("shared app-server mode is enabled by default")
	}
	if err := config.validate(); err != nil {
		t.Fatalf("default config is invalid: %v", err)
	}
}

func TestConfigMigrationAddsDisabledSharedAppServerField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	legacy := `{
  "config_version": 6,
  "poll_interval_seconds": 2,
  "initial_delay_seconds": 5,
  "max_delay_seconds": 300,
  "delay_increment_seconds": 2,
  "delay_strategy": "exponential",
  "max_consecutive_retries": 5,
  "max_recovery_attempts": 15,
  "max_parallel_retries": 4,
  "start_ack_timeout_seconds": 30,
  "auth_max_attempts": 6,
  "unknown_max_attempts": 3,
  "include_default_home": true,
  "include_cockpit_homes": true,
  "shared_app_server_port": 49321,
  "controller_failure_limit": 3,
  "retry_prompt": "缁х画",
  "show_notifications": true
}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadOrCreateConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ConfigVersion != currentConfigVersion || loaded.SharedAppServerEnabled {
		t.Fatalf("migration did not default shared mode to disabled: %+v", loaded)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "shared_app_server_enabled") {
		t.Fatalf("migrated config omitted shared mode field: %s", written)
	}
}

func TestConfigMigrationMovesLegacySharedServerDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	config := defaultConfig()
	config.ConfigVersion = 7
	config.SharedAppServerPort = legacyDefaultSharedAppServerPort
	data, err := json.Marshal(config)
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
	if loaded.ConfigVersion != currentConfigVersion || loaded.SharedAppServerPort != defaultSharedAppServerPort {
		t.Fatalf("legacy shared-server default was not moved: %+v", loaded)
	}
}

func TestCurrentConfigPreservesExplicitLegacySharedServerPort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	config := defaultConfig()
	config.SharedAppServerPort = legacyDefaultSharedAppServerPort
	data, err := json.Marshal(config)
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
	if loaded.SharedAppServerPort != legacyDefaultSharedAppServerPort {
		t.Fatalf("current explicit shared-server port was unexpectedly changed: %+v", loaded)
	}
}

func TestEnabledLegacySharedServerPortIsNotChangedDuringMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	config := defaultConfig()
	config.ConfigVersion = 7
	config.SharedAppServerPort = legacyDefaultSharedAppServerPort
	config.SharedAppServerEnabled = true
	data, err := json.Marshal(config)
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
	if loaded.SharedAppServerPort != legacyDefaultSharedAppServerPort || !loaded.SharedAppServerEnabled {
		t.Fatalf("enabled legacy shared-server configuration was changed unsafely: %+v", loaded)
	}
}
