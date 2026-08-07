package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type ManagedRetry struct {
	ThreadID              string       `json:"thread_id" jsonschema:"Codex task identifier"`
	Label                 string       `json:"label" jsonschema:"privacy-safe short task label"`
	State                 string       `json:"state" jsonschema:"pending, starting, or running"`
	Class                 FailureClass `json:"class" jsonschema:"provider failure category"`
	DueAt                 string       `json:"due_at,omitempty" jsonschema:"next retry time in RFC 3339 format"`
	SecondsRemaining      int64        `json:"seconds_remaining" jsonschema:"whole seconds until the retry is due"`
	RecoveryAttempt       int          `json:"recovery_attempt" jsonschema:"current attempt in this fault recovery cycle"`
	MaxRecoveryAttempts   int          `json:"max_recovery_attempts,omitempty" jsonschema:"maximum attempts in this fault recovery cycle"`
	ConsecutiveRetry      int          `json:"consecutive_retry" jsonschema:"current retry without visible assistant progress"`
	MaxConsecutiveRetries int          `json:"max_consecutive_retries,omitempty" jsonschema:"maximum retries without visible assistant progress"`
	Action                RetryAction  `json:"action,omitempty" jsonschema:"current recovery action"`
	CanRetryNow           bool         `json:"can_retry_now" jsonschema:"whether retry-now is currently available"`
	CanCancel             bool         `json:"can_cancel" jsonschema:"whether the pending retry can be cancelled"`
	CanRestart            bool         `json:"can_restart" jsonschema:"whether an exhausted retry can be restarted with a fresh budget"`
	StopReason            string       `json:"stop_reason,omitempty" jsonschema:"privacy-safe reason why retries stopped"`
}

// Stopped entries are useful immediately after a limit is reached because
// they explain why recovery stopped. They are not executable queue items,
// though, and keeping every historical stop in the queue makes an otherwise
// single-task session look like many pending jobs. Keep a short visible window
// for the explicit reason, while retaining the durable state for diagnostics.
const stoppedRetryDisplayWindow = time.Hour

type ManagementSnapshot struct {
	Version                string         `json:"version" jsonschema:"watchdog version"`
	Running                bool           `json:"running" jsonschema:"whether a fresh watchdog heartbeat exists"`
	HeartbeatStale         bool           `json:"heartbeat_stale" jsonschema:"whether the last heartbeat is too old"`
	Paused                 bool           `json:"paused" jsonschema:"whether new retry dispatches are paused"`
	RetryPrompt            string         `json:"retry_prompt" jsonschema:"fallback message used only when silent continuation is unsupported"`
	MaxConsecutiveRetries  int            `json:"max_consecutive_retries" jsonschema:"maximum retries without visible assistant progress"`
	MaxRecoveryAttempts    int            `json:"max_recovery_attempts" jsonschema:"maximum attempts in one fault recovery cycle"`
	InitialDelaySeconds    int            `json:"initial_delay_seconds" jsonschema:"delay before the first automatic retry"`
	MaxDelaySeconds        int            `json:"max_delay_seconds" jsonschema:"maximum cap for increasing retry delays"`
	DelayIncrementSeconds  int            `json:"delay_increment_seconds" jsonschema:"seconds added after each linear retry"`
	DelayStrategy          string         `json:"delay_strategy" jsonschema:"fixed, exponential, or linear retry delay"`
	ShowNotifications      bool           `json:"show_notifications" jsonschema:"whether Windows notifications are enabled"`
	SharedAppServerEnabled bool           `json:"shared_app_server_enabled" jsonschema:"whether the optional shared Codex app-server recovery mode is enabled"`
	Now                    string         `json:"now" jsonschema:"snapshot time in RFC 3339 format"`
	LastScanAt             string         `json:"last_scan_at,omitempty" jsonschema:"last session scan time in RFC 3339 format"`
	PendingRetries         int            `json:"pending_retries" jsonschema:"number of retries waiting to dispatch"`
	ActiveRetries          int            `json:"active_retries" jsonschema:"number of retries starting or running"`
	StoppedRetries         int            `json:"stopped_retries" jsonschema:"number of retry chains stopped at their attempt limit"`
	WatchedRoots           int            `json:"watched_roots" jsonschema:"number of watched Codex session roots"`
	LastError              string         `json:"last_error,omitempty" jsonschema:"privacy-safe watchdog error summary"`
	ControllerState        string         `json:"controller_state,omitempty" jsonschema:"background Codex controller state"`
	Notice                 string         `json:"notice,omitempty" jsonschema:"result of the most recent management action"`
	Retries                []ManagedRetry `json:"retries" jsonschema:"current retry queue"`
}

type managementService struct {
	dataDir     string
	configPath  string
	controlPath string
	commandDir  string
	statePath   string
	statusPath  string
	mu          sync.Mutex
}

func newManagementService(dataDir string) *managementService {
	return &managementService{
		dataDir:     dataDir,
		configPath:  filepath.Join(dataDir, "config.json"),
		controlPath: filepath.Join(dataDir, "control.json"),
		commandDir:  filepath.Join(dataDir, "commands"),
		statePath:   filepath.Join(dataDir, "state.json"),
		statusPath:  filepath.Join(dataDir, "status.json"),
	}
}

func (m *managementService) snapshot(now time.Time) (ManagementSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked(now.UTC())
}

func (m *managementService) snapshotLocked(now time.Time) (ManagementSnapshot, error) {
	config, err := loadOrCreateConfig(m.configPath)
	if err != nil {
		return ManagementSnapshot{}, err
	}
	control, err := loadOrCreateControlState(m.controlPath)
	if err != nil {
		return ManagementSnapshot{}, err
	}
	state, err := loadState(m.statePath)
	if err != nil {
		return ManagementSnapshot{}, err
	}
	status, statusFound, err := loadStatusSnapshot(m.statusPath)
	if err != nil {
		return ManagementSnapshot{}, err
	}

	staleAfter := time.Duration(config.PollIntervalSeconds*4) * time.Second
	if staleAfter < 15*time.Second {
		staleAfter = 15 * time.Second
	}
	processRunning := statusFound && processOwnsRuntime(status.PID, m.dataDir)
	heartbeatStale := !statusFound || !processRunning || status.LastScanAt.IsZero() || now.Sub(status.LastScanAt) > staleAfter
	running := statusFound && status.Running && !heartbeatStale
	retries := managedRetries(state, now)
	visibleRetries := retries[:0]
	pending, active, stopped := 0, 0, 0
	for _, retry := range retries {
		if !running && retry.State != "stopped" {
			continue
		}
		visibleRetries = append(visibleRetries, retry)
		if retry.State == "pending" {
			pending++
		} else if retry.State == "stopped" {
			stopped++
		} else {
			active++
		}
	}
	retries = visibleRetries

	snapshot := ManagementSnapshot{
		Version:                appVersion,
		Running:                running,
		HeartbeatStale:         heartbeatStale,
		Paused:                 control.Paused,
		RetryPrompt:            config.RetryPrompt,
		MaxConsecutiveRetries:  config.MaxConsecutiveRetries,
		MaxRecoveryAttempts:    config.MaxRecoveryAttempts,
		InitialDelaySeconds:    config.InitialDelaySeconds,
		MaxDelaySeconds:        config.MaxDelaySeconds,
		DelayIncrementSeconds:  config.DelayIncrementSeconds,
		DelayStrategy:          config.DelayStrategy,
		ShowNotifications:      config.ShowNotifications,
		SharedAppServerEnabled: config.SharedAppServerEnabled,
		Now:                    now.Format(time.RFC3339Nano),
		PendingRetries:         pending,
		ActiveRetries:          active,
		StoppedRetries:         stopped,
		Retries:                retries,
	}
	if statusFound {
		if running {
			snapshot.Version = status.Version
		}
		snapshot.WatchedRoots = status.WatchedRoots
		snapshot.LastError = status.LastError
		snapshot.ControllerState = status.ControllerState
		if !status.LastScanAt.IsZero() {
			snapshot.LastScanAt = status.LastScanAt.Format(time.RFC3339Nano)
		}
	}
	return snapshot, nil
}

func (m *managementService) setSharedAppServerEnabled(enabled bool, now time.Time) (ManagementSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	config, err := loadOrCreateConfig(m.configPath)
	if err != nil {
		return ManagementSnapshot{}, err
	}
	if config.SharedAppServerEnabled == enabled {
		snapshot, snapshotErr := m.snapshotLocked(now.UTC())
		if snapshotErr == nil {
			snapshot.Notice = "共享后台模式未改变"
		}
		return snapshot, snapshotErr
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if enabled {
		if err := enableSharedAppServer(ctx, m.dataDir, config); err != nil {
			return ManagementSnapshot{}, err
		}
		config.SharedAppServerEnabled = true
		if err := config.validate(); err != nil {
			_ = disableSharedAppServer(context.Background(), m.dataDir, config)
			return ManagementSnapshot{}, err
		}
		if err := writeJSONAtomic(m.configPath, config); err != nil {
			_ = disableSharedAppServer(context.Background(), m.dataDir, config)
			return ManagementSnapshot{}, fmt.Errorf("save shared app-server setting: %w", err)
		}
	} else {
		config.SharedAppServerEnabled = false
		if err := config.validate(); err != nil {
			return ManagementSnapshot{}, err
		}
		// Persist the fail-open setting before tearing down the endpoint. If the
		// cleanup is interrupted, the next watchdog tick still cannot take over
		// Codex's backend.
		if err := writeJSONAtomic(m.configPath, config); err != nil {
			return ManagementSnapshot{}, fmt.Errorf("save shared app-server setting: %w", err)
		}
		if err := disableSharedAppServer(ctx, m.dataDir, config); err != nil {
			return ManagementSnapshot{}, err
		}
	}
	snapshot, err := m.snapshotLocked(now.UTC())
	if err == nil {
		if enabled {
			snapshot.Notice = "共享后台模式已启用；重启 Codex 后生效"
		} else {
			snapshot.Notice = "共享后台模式已关闭，Codex 将使用官方后台"
		}
	}
	return snapshot, err
}

type RetrySettings struct {
	RetryPrompt           string `json:"retry_prompt"`
	MaxConsecutiveRetries int    `json:"max_consecutive_retries"`
	MaxRecoveryAttempts   int    `json:"max_recovery_attempts"`
	InitialDelaySeconds   int    `json:"initial_delay_seconds"`
	MaxDelaySeconds       int    `json:"max_delay_seconds"`
	DelayIncrementSeconds int    `json:"delay_increment_seconds"`
	DelayStrategy         string `json:"delay_strategy"`
	ShowNotifications     bool   `json:"show_notifications"`
}

func (m *managementService) setRetrySettings(settings RetrySettings, now time.Time) (ManagementSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	config, err := loadOrCreateConfig(m.configPath)
	if err != nil {
		return ManagementSnapshot{}, err
	}
	applyRetrySettings(&config, settings)
	if err := config.validate(); err != nil {
		return ManagementSnapshot{}, err
	}
	if err := writeJSONAtomic(m.configPath, config); err != nil {
		return ManagementSnapshot{}, fmt.Errorf("save retry settings: %w", err)
	}
	snapshot, err := m.snapshotLocked(now.UTC())
	if err == nil {
		snapshot.Notice = "自动重试设置已保存"
	}
	return snapshot, err
}

func (m *managementService) setLocalSettings(settings RetrySettings, paused bool, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	config, err := loadOrCreateConfig(m.configPath)
	if err != nil {
		return err
	}
	originalConfig := config
	applyRetrySettings(&config, settings)
	if err := config.validate(); err != nil {
		return err
	}
	if err := writeJSONAtomic(m.configPath, config); err != nil {
		return fmt.Errorf("save local retry settings: %w", err)
	}
	if _, err := saveControlState(m.controlPath, paused, now); err != nil {
		if rollbackErr := writeJSONAtomic(m.configPath, originalConfig); rollbackErr != nil {
			return fmt.Errorf("save local pause state: %v; restore retry settings: %w", err, rollbackErr)
		}
		return fmt.Errorf("save local pause state: %w", err)
	}
	return nil
}

func applyRetrySettings(config *Config, settings RetrySettings) {
	config.RetryPrompt = settings.RetryPrompt
	config.MaxConsecutiveRetries = settings.MaxConsecutiveRetries
	config.MaxRecoveryAttempts = settings.MaxRecoveryAttempts
	config.InitialDelaySeconds = settings.InitialDelaySeconds
	config.MaxDelaySeconds = settings.MaxDelaySeconds
	if settings.DelayIncrementSeconds != 0 || settings.DelayStrategy == delayStrategyLinear {
		config.DelayIncrementSeconds = settings.DelayIncrementSeconds
	}
	config.DelayStrategy = settings.DelayStrategy
	config.ShowNotifications = settings.ShowNotifications
}

func (m *managementService) setRetryPrompt(prompt string, now time.Time) (ManagementSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	config, err := loadOrCreateConfig(m.configPath)
	if err != nil {
		return ManagementSnapshot{}, err
	}
	config.RetryPrompt = prompt
	if err := config.validate(); err != nil {
		return ManagementSnapshot{}, err
	}
	if err := writeJSONAtomic(m.configPath, config); err != nil {
		return ManagementSnapshot{}, fmt.Errorf("save retry prompt: %w", err)
	}
	snapshot, err := m.snapshotLocked(now.UTC())
	if err == nil {
		snapshot.Notice = "普通对话的重试文字已保存"
	}
	return snapshot, err
}

func (m *managementService) setPaused(paused bool, now time.Time) (ManagementSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := saveControlState(m.controlPath, paused, now); err != nil {
		return ManagementSnapshot{}, fmt.Errorf("save pause state: %w", err)
	}
	snapshot, err := m.snapshotLocked(now.UTC())
	if err == nil {
		if paused {
			snapshot.Notice = "自动重试已暂停"
		} else {
			snapshot.Notice = "自动重试已恢复"
		}
	}
	return snapshot, err
}

func (m *managementService) retryNow(threadID string, now time.Time) (ManagementSnapshot, error) {
	return m.queueThreadCommand(commandRetryNow, threadID, now)
}

func (m *managementService) cancelRetry(threadID string, now time.Time) (ManagementSnapshot, error) {
	return m.queueThreadCommand(commandCancelRetry, threadID, now)
}

func (m *managementService) restartRetry(threadID string, now time.Time) (ManagementSnapshot, error) {
	return m.queueThreadCommand(commandRestartRetry, threadID, now)
}

func (m *managementService) queueThreadCommand(action ControlCommandAction, threadID string, now time.Time) (ManagementSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	threadID = strings.ToLower(strings.TrimSpace(threadID))
	state, err := loadState(m.statePath)
	if err != nil {
		return ManagementSnapshot{}, err
	}
	thread, found := state.Threads[threadID]
	if !found || (action == commandRestartRetry && thread.Stopped == nil) ||
		(action != commandRestartRetry && thread.Pending == nil) {
		return ManagementSnapshot{}, errors.New("该任务当前没有可执行的重试操作")
	}
	if _, err := queueControlCommand(m.commandDir, action, threadID, now); err != nil {
		return ManagementSnapshot{}, err
	}
	snapshot, err := m.snapshotLocked(now.UTC())
	if err == nil {
		if action == commandRetryNow {
			snapshot.Notice = "已请求立即重试"
		} else if action == commandRestartRetry {
			snapshot.Notice = "已重新开始计数并请求重试"
		} else {
			snapshot.Notice = "已请求取消这次重试"
		}
	}
	return snapshot, err
}

func loadStatusSnapshot(path string) (StatusSnapshot, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return StatusSnapshot{}, false, nil
	}
	if err != nil {
		return StatusSnapshot{}, false, err
	}
	var status StatusSnapshot
	if err := json.Unmarshal(data, &status); err != nil {
		return StatusSnapshot{}, false, fmt.Errorf("parse status: %w", err)
	}
	return status, true, nil
}

func managedRetries(state RuntimeState, now time.Time) []ManagedRetry {
	retries := make([]ManagedRetry, 0)
	for threadID, thread := range state.Threads {
		if thread.Pending != nil {
			remaining := int64(0)
			if duration := thread.Pending.DueAt.Sub(now); duration > 0 {
				remaining = int64((duration + time.Second - 1) / time.Second)
			}
			retries = append(retries, ManagedRetry{
				ThreadID:              threadID,
				Label:                 "任务 " + shortThreadID(threadID),
				State:                 "pending",
				Class:                 thread.Pending.Class,
				DueAt:                 thread.Pending.DueAt.Format(time.RFC3339Nano),
				SecondsRemaining:      remaining,
				RecoveryAttempt:       thread.Pending.Attempt,
				MaxRecoveryAttempts:   thread.Pending.MaxAttempts,
				ConsecutiveRetry:      thread.Pending.ConsecutiveRetry,
				MaxConsecutiveRetries: thread.Pending.MaxConsecutive,
				CanRetryNow:           true,
				CanCancel:             true,
			})
		}
		if thread.Awaiting != nil {
			stateName := "starting"
			if thread.Awaiting.RetryTurnID != "" {
				stateName = "running"
			}
			retries = append(retries, ManagedRetry{
				ThreadID:              threadID,
				Label:                 "任务 " + shortThreadID(threadID),
				State:                 stateName,
				Class:                 thread.Awaiting.Class,
				RecoveryAttempt:       thread.Awaiting.Attempt,
				MaxRecoveryAttempts:   thread.Awaiting.MaxAttempts,
				ConsecutiveRetry:      thread.Awaiting.ConsecutiveRetry,
				MaxConsecutiveRetries: thread.Awaiting.MaxConsecutive,
				Action:                thread.Awaiting.Action,
			})
		}
		if thread.Stopped != nil && stoppedRetryIsVisible(thread.Stopped, now) {
			retries = append(retries, ManagedRetry{
				ThreadID:              threadID,
				Label:                 "任务 " + shortThreadID(threadID),
				State:                 "stopped",
				Class:                 thread.Stopped.Class,
				RecoveryAttempt:       thread.Stopped.Attempts,
				MaxRecoveryAttempts:   thread.Stopped.MaxAttempts,
				ConsecutiveRetry:      thread.Stopped.ConsecutiveRetries,
				MaxConsecutiveRetries: thread.Stopped.MaxConsecutive,
				CanRestart:            true,
				StopReason:            thread.Stopped.Reason,
			})
		}
	}
	sort.SliceStable(retries, func(i, j int) bool {
		if retries[i].State != retries[j].State {
			if retries[i].State == "pending" {
				return false
			}
			if retries[j].State == "pending" {
				return true
			}
		}
		if retries[i].DueAt != retries[j].DueAt {
			return retries[i].DueAt < retries[j].DueAt
		}
		return retries[i].ThreadID < retries[j].ThreadID
	})
	return retries
}

func stoppedRetryIsVisible(stopped *StoppedRetry, now time.Time) bool {
	if stopped == nil || stopped.StoppedAt.IsZero() {
		return stopped != nil
	}
	if stopped.Historical {
		return false
	}
	if !stopped.FailedAt.IsZero() && !stopped.FailedAt.After(now) && now.Sub(stopped.FailedAt) > stoppedRetryDisplayWindow {
		return false
	}
	if stopped.StoppedAt.After(now) {
		return true
	}
	return now.Sub(stopped.StoppedAt) <= stoppedRetryDisplayWindow
}
