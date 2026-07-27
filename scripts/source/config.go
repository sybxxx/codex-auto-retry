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
	MaxParallelRetries     int      `json:"max_parallel_retries"`
	StartAckTimeoutSeconds int      `json:"start_ack_timeout_seconds"`
	AuthMaxAttempts        int      `json:"auth_max_attempts"`
	UnknownMaxAttempts     int      `json:"unknown_max_attempts"`
	SessionRoots           []string `json:"session_roots"`
	IncludeDefaultHome     bool     `json:"include_default_home"`
	IncludeCockpitHomes    bool     `json:"include_cockpit_homes"`
	PowerShellExecutable   string   `json:"powershell_executable,omitempty"`
	RendererDebugPort      int      `json:"renderer_debug_port,omitempty"`
	RetryPrompt            string   `json:"retry_prompt"`
}

const legacyRetryPrompt = "Continue the interrupted task from its current state. The previous turn ended because the model provider was temporarily unavailable. First inspect the existing conversation and workspace state, do not repeat completed side effects, then continue toward the user's latest request. Do not discuss the retry mechanism unless it affects the result."

const defaultRetryPrompt = "继续"

const maxRetryPromptRunes = 500

const currentConfigVersion = 2

func defaultConfig() Config {
	return Config{
		ConfigVersion:          currentConfigVersion,
		PollIntervalSeconds:    2,
		InitialDelaySeconds:    5,
		MaxDelaySeconds:        300,
		MaxParallelRetries:     4,
		StartAckTimeoutSeconds: 30,
		AuthMaxAttempts:        6,
		UnknownMaxAttempts:     3,
		IncludeDefaultHome:     true,
		IncludeCockpitHomes:    true,
		RetryPrompt:            defaultRetryPrompt,
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
	// Version 1 forced one global UI action because retries navigated the same
	// Codex window. Version 2 uses independent background requests.
	if _, versioned := fields["config_version"]; !versioned {
		cfg.ConfigVersion = currentConfigVersion
		cfg.MaxParallelRetries = defaultConfig().MaxParallelRetries
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
	if c.MaxDelaySeconds < c.InitialDelaySeconds || c.MaxDelaySeconds > 86400 {
		return errors.New("max_delay_seconds must be at least initial_delay_seconds and no more than 86400")
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
	if c.RendererDebugPort < 0 || c.RendererDebugPort > 65535 {
		return errors.New("renderer_debug_port must be between 1 and 65535 when set")
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
