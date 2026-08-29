package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type Config struct {
	ConfigVersion          int      `json:"config_version"`
	PollIntervalSeconds    int      `json:"poll_interval_seconds"`
	InitialDelaySeconds    int      `json:"initial_delay_seconds"`
	MaxDelaySeconds        int      `json:"max_delay_seconds"`
	DelayIncrementSeconds  int      `json:"delay_increment_seconds"`
	DelayStrategy          string   `json:"delay_strategy"`
	MaxConsecutiveRetries  int      `json:"max_consecutive_retries"`
	MaxRecoveryAttempts    int      `json:"max_recovery_attempts"`
	MaxParallelRetries     int      `json:"max_parallel_retries"`
	StartAckTimeoutSeconds int      `json:"start_ack_timeout_seconds"`
	AuthMaxAttempts        int      `json:"auth_max_attempts"`
	UnknownMaxAttempts     int      `json:"unknown_max_attempts"`
	SessionRoots           []string `json:"session_roots"`
	IncludeDefaultHome     bool     `json:"include_default_home"`
	IncludeCockpitHomes    bool     `json:"include_cockpit_homes"`
	PowerShellExecutable   string   `json:"powershell_executable,omitempty"`
	SharedAppServerPort    int      `json:"shared_app_server_port"`
	SharedAppServerEnabled bool     `json:"shared_app_server_enabled"`
	ControllerFailureLimit int      `json:"controller_failure_limit"`
	MemoryLimitMB          int      `json:"memory_limit_mb"`
	RetryPrompt            string   `json:"retry_prompt"`
	ShowNotifications      bool     `json:"show_notifications"`
}

const legacyRetryPrompt = "Continue the interrupted task from its current state. The previous turn ended because the model provider was temporarily unavailable. First inspect the existing conversation and workspace state, do not repeat completed side effects, then continue toward the user's latest request. Do not discuss the retry mechanism unless it affects the result."

const defaultRetryPrompt = "继续"

const maxRetryPromptRunes = 500

const (
	maxConsecutiveRetriesLimit = 100
	maxRecoveryAttemptsLimit   = 1000
	minMemoryLimitMB           = 128
	maxMemoryLimitMB           = 65536
)

const (
	currentConfigVersion             = 9
	legacyDefaultSharedAppServerPort = 49321
	defaultSharedAppServerPort       = 49621
)

const (
	delayStrategyExponential = "exponential"
	delayStrategyLinear      = "linear"
	delayStrategyFixed       = "fixed"
)

func defaultConfig() Config {
	return Config{
		ConfigVersion:          currentConfigVersion,
		PollIntervalSeconds:    2,
		InitialDelaySeconds:    5,
		MaxDelaySeconds:        300,
		DelayIncrementSeconds:  2,
		DelayStrategy:          delayStrategyExponential,
		MaxConsecutiveRetries:  5,
		MaxRecoveryAttempts:    15,
		MaxParallelRetries:     4,
		StartAckTimeoutSeconds: 30,
		AuthMaxAttempts:        6,
		UnknownMaxAttempts:     3,
		IncludeDefaultHome:     true,
		IncludeCockpitHomes:    true,
		SharedAppServerPort:    defaultSharedAppServerPort,
		SharedAppServerEnabled: false,
		ControllerFailureLimit: 3,
		MemoryLimitMB:          1024,
		RetryPrompt:            defaultRetryPrompt,
		ShowNotifications:      true,
	}
}

func loadOrCreateConfig(path string) (Config, error) {
	cfg := defaultConfig()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := writeJSONAtomic(path, cfg); err != nil {
			return Config{}, err
		}
		return cfg, nil
	}
	if err != nil {
		return Config{}, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return Config{}, fmt.Errorf("parse config fields: %w", err)
	}
	changed := false
	sourceConfigVersion := 0
	if rawVersion, found := fields["config_version"]; found {
		_ = json.Unmarshal(rawVersion, &sourceConfigVersion)
	}
	// Version 1 forced one global UI action because retries navigated the same
	// Codex window. Version 2 uses independent background requests. Version 3
	// added one ambiguous retry cap. Version 4 separates the consecutive retry
	// guard from the per-fault recovery budget and makes delay behavior explicit.
	// Version 5 adds a configurable increment for linear waits. Version 6 moves
	// recovery to the shared local app-server channel used by current Codex App.
	// Version 7 makes that channel an explicit opt-in so a broken plugin cannot
	// prevent Codex from starting with its own official backend. Version 8 moves
	// the default endpoint out of a Windows-excluded port range. Version 9 adds
	// a bounded private-memory guard for the watchdog process.
	if _, versioned := fields["config_version"]; !versioned {
		cfg.ConfigVersion = currentConfigVersion
		cfg.MaxParallelRetries = defaultConfig().MaxParallelRetries
		changed = true
	} else if cfg.ConfigVersion >= 1 && cfg.ConfigVersion < currentConfigVersion {
		cfg.ConfigVersion = currentConfigVersion
		changed = true
	}
	if cfg.ConfigVersion == currentConfigVersion {
		legacyLimit := 0
		if raw, found := fields["max_retry_attempts"]; found {
			_ = json.Unmarshal(raw, &legacyLimit)
		}
		if _, found := fields["max_consecutive_retries"]; !found {
			// The old setting actually bounded the entire recovery chain even
			// though the UI called it consecutive retries. Preserve that value
			// below as the recovery budget and introduce the safer default for
			// retries that make no visible progress.
			cfg.MaxConsecutiveRetries = defaultConfig().MaxConsecutiveRetries
			changed = true
		}
		if _, found := fields["max_recovery_attempts"]; !found {
			if legacyLimit > 0 {
				cfg.MaxRecoveryAttempts = legacyLimit
			} else {
				cfg.MaxRecoveryAttempts = defaultConfig().MaxRecoveryAttempts
			}
			changed = true
		}
		if _, found := fields["delay_strategy"]; !found {
			cfg.DelayStrategy = delayStrategyExponential
			changed = true
		}
		if _, found := fields["delay_increment_seconds"]; !found {
			cfg.DelayIncrementSeconds = defaultConfig().DelayIncrementSeconds
			changed = true
		}
		if _, found := fields["shared_app_server_port"]; !found {
			cfg.SharedAppServerPort = defaultConfig().SharedAppServerPort
			changed = true
		}
		if _, found := fields["controller_failure_limit"]; !found {
			cfg.ControllerFailureLimit = defaultConfig().ControllerFailureLimit
			changed = true
		}
		if _, found := fields["shared_app_server_enabled"]; !found {
			cfg.SharedAppServerEnabled = defaultConfig().SharedAppServerEnabled
			changed = true
		}
		if _, found := fields["memory_limit_mb"]; !found {
			cfg.MemoryLimitMB = defaultConfig().MemoryLimitMB
			changed = true
		}
	}
	if sourceConfigVersion < currentConfigVersion && !cfg.SharedAppServerEnabled && cfg.SharedAppServerPort == legacyDefaultSharedAppServerPort {
		cfg.SharedAppServerPort = defaultSharedAppServerPort
		changed = true
	}
	if cfg.RetryPrompt == legacyRetryPrompt || cfg.RetryPrompt == "Continue." {
		cfg.RetryPrompt = defaultRetryPrompt
		changed = true
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	if changed {
		if err := writeJSONAtomic(path, cfg); err != nil {
			return Config{}, fmt.Errorf("migrate config: %w", err)
		}
	}
	return cfg, nil
}

func (c Config) validate() error {
	if c.ConfigVersion != currentConfigVersion {
		return fmt.Errorf("config_version must be %d", currentConfigVersion)
	}
	if c.PollIntervalSeconds < 1 || c.PollIntervalSeconds > 60 {
		return errors.New("poll_interval_seconds must be between 1 and 60")
	}
	if c.InitialDelaySeconds < 1 || c.InitialDelaySeconds > 3600 {
		return errors.New("initial_delay_seconds must be between 1 and 3600")
	}
	if c.DelayIncrementSeconds < 1 || c.DelayIncrementSeconds > 3600 {
		return errors.New("delay_increment_seconds must be between 1 and 3600")
	}
	if c.DelayStrategy != delayStrategyExponential && c.DelayStrategy != delayStrategyLinear && c.DelayStrategy != delayStrategyFixed {
		return errors.New("delay_strategy must be exponential, linear, or fixed")
	}
	if c.MaxDelaySeconds < 1 || c.MaxDelaySeconds > 86400 ||
		(c.DelayStrategy != delayStrategyFixed && c.MaxDelaySeconds < c.InitialDelaySeconds) {
		return errors.New("max_delay_seconds must be between 1 and 86400 and at least initial_delay_seconds for increasing delays")
	}
	if c.MaxConsecutiveRetries < 1 || c.MaxConsecutiveRetries > maxConsecutiveRetriesLimit {
		return fmt.Errorf("max_consecutive_retries must be between 1 and %d", maxConsecutiveRetriesLimit)
	}
	if c.MaxRecoveryAttempts < 1 || c.MaxRecoveryAttempts > maxRecoveryAttemptsLimit {
		return fmt.Errorf("max_recovery_attempts must be between 1 and %d", maxRecoveryAttemptsLimit)
	}
	if c.MaxParallelRetries < 1 || c.MaxParallelRetries > 16 {
		return errors.New("max_parallel_retries must be between 1 and 16")
	}
	if c.StartAckTimeoutSeconds < 10 || c.StartAckTimeoutSeconds > 300 {
		return errors.New("start_ack_timeout_seconds must be between 10 and 300")
	}
	if c.AuthMaxAttempts < 1 || c.UnknownMaxAttempts < 1 {
		return errors.New("limited retry counts must be positive")
	}
	if strings.TrimSpace(c.RetryPrompt) == "" {
		return errors.New("retry_prompt must not be empty")
	}
	if utf8.RuneCountInString(c.RetryPrompt) > maxRetryPromptRunes {
		return fmt.Errorf("retry_prompt must not exceed %d characters", maxRetryPromptRunes)
	}
	if c.SharedAppServerPort < 1024 || c.SharedAppServerPort > 65535 {
		return errors.New("shared_app_server_port must be between 1024 and 65535")
	}
	if c.ControllerFailureLimit < 1 || c.ControllerFailureLimit > 20 {
		return errors.New("controller_failure_limit must be between 1 and 20")
	}
	if c.MemoryLimitMB < minMemoryLimitMB || c.MemoryLimitMB > maxMemoryLimitMB {
		return fmt.Errorf("memory_limit_mb must be between %d and %d", minMemoryLimitMB, maxMemoryLimitMB)
	}
	return nil
}

func expandPath(value string) string {
	value = os.ExpandEnv(strings.TrimSpace(value))
	if value == "~" || strings.HasPrefix(value, "~\\") || strings.HasPrefix(value, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			value = filepath.Join(home, strings.TrimLeft(value[1:], "\\/"))
		}
	}
	abs, err := filepath.Abs(value)
	if err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(value)
}
