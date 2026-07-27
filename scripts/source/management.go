package main

import (
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
	ThreadID         string       `json:"thread_id" jsonschema:"Codex task identifier"`
	Label            string       `json:"label" jsonschema:"privacy-safe short task label"`
	State            string       `json:"state" jsonschema:"pending, starting, or running"`
	Class            FailureClass `json:"class" jsonschema:"provider failure category"`
	DueAt            string       `json:"due_at,omitempty" jsonschema:"next retry time in RFC 3339 format"`
	SecondsRemaining int64        `json:"seconds_remaining" jsonschema:"whole seconds until the retry is due"`
	Attempt          int          `json:"attempt" jsonschema:"current provider retry attempt"`
	MaxAttempts      int          `json:"max_attempts,omitempty" jsonschema:"retry limit, or zero when unlimited"`
	Action           RetryAction  `json:"action,omitempty" jsonschema:"current recovery action"`
	CanRetryNow      bool         `json:"can_retry_now" jsonschema:"whether retry-now is currently available"`
	CanCancel        bool         `json:"can_cancel" jsonschema:"whether the pending retry can be cancelled"`
}

type ManagementSnapshot struct {
	Version        string         `json:"version" jsonschema:"watchdog version"`
	Running        bool           `json:"running" jsonschema:"whether a fresh watchdog heartbeat exists"`
	HeartbeatStale bool           `json:"heartbeat_stale" jsonschema:"whether the last heartbeat is too old"`
	Paused         bool           `json:"paused" jsonschema:"whether new retry dispatches are paused"`
	RetryPrompt    string         `json:"retry_prompt" jsonschema:"message used for normal conversation retries"`
	Now            string         `json:"now" jsonschema:"snapshot time in RFC 3339 format"`
	LastScanAt     string         `json:"last_scan_at,omitempty" jsonschema:"last session scan time in RFC 3339 format"`
	PendingRetries int            `json:"pending_retries" jsonschema:"number of retries waiting to dispatch"`
	ActiveRetries  int            `json:"active_retries" jsonschema:"number of retries starting or running"`
	WatchedRoots   int            `json:"watched_roots" jsonschema:"number of watched Codex session roots"`
	LastError      string         `json:"last_error,omitempty" jsonschema:"privacy-safe watchdog error summary"`
	Notice         string         `json:"notice,omitempty" jsonschema:"result of the most recent management action"`
	Retries        []ManagedRetry `json:"retries" jsonschema:"current retry queue"`
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

	retries := managedRetries(state, now)
	pending, active := 0, 0
	for _, retry := range retries {
		if retry.State == "pending" {
			pending++
		} else {
			active++
		}
	}
	staleAfter := time.Duration(config.PollIntervalSeconds*4) * time.Second
	if staleAfter < 15*time.Second {
		staleAfter = 15 * time.Second
	}
	heartbeatStale := !statusFound || status.LastScanAt.IsZero() || now.Sub(status.LastScanAt) > staleAfter
	running := statusFound && status.Running && !heartbeatStale

	snapshot := ManagementSnapshot{
		Version:        appVersion,
		Running:        running,
		HeartbeatStale: heartbeatStale,
		Paused:         control.Paused,
		RetryPrompt:    config.RetryPrompt,
		Now:            now.Format(time.RFC3339Nano),
		PendingRetries: pending,
		ActiveRetries:  active,
		Retries:        retries,
	}
	if statusFound {
		snapshot.Version = status.Version
		snapshot.WatchedRoots = status.WatchedRoots
		snapshot.LastError = status.LastError
		if !status.LastScanAt.IsZero() {
			snapshot.LastScanAt = status.LastScanAt.Format(time.RFC3339Nano)
		}
	}
	return snapshot, nil
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

func (m *managementService) queueThreadCommand(action ControlCommandAction, threadID string, now time.Time) (ManagementSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	threadID = strings.ToLower(strings.TrimSpace(threadID))
	state, err := loadState(m.statePath)
	if err != nil {
		return ManagementSnapshot{}, err
	}
	thread, found := state.Threads[threadID]
	if !found || thread.Pending == nil {
		return ManagementSnapshot{}, errors.New("该任务当前没有等待中的重试")
	}
	if _, err := queueControlCommand(m.commandDir, action, threadID, now); err != nil {
		return ManagementSnapshot{}, err
	}
	snapshot, err := m.snapshotLocked(now.UTC())
	if err == nil {
		if action == commandRetryNow {
			snapshot.Notice = "已请求立即重试"
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
				ThreadID:         threadID,
				Label:            "任务 " + shortThreadID(threadID),
				State:            "pending",
				Class:            thread.Pending.Class,
				DueAt:            thread.Pending.DueAt.Format(time.RFC3339Nano),
				SecondsRemaining: remaining,
				Attempt:          thread.Pending.Attempt,
				MaxAttempts:      thread.Pending.MaxAttempts,
				CanRetryNow:      true,
				CanCancel:        true,
			})
		}
		if thread.Awaiting != nil {
			stateName := "starting"
			if thread.Awaiting.RetryTurnID != "" {
				stateName = "running"
			}
			retries = append(retries, ManagedRetry{
				ThreadID:    threadID,
				Label:       "任务 " + shortThreadID(threadID),
				State:       stateName,
				Class:       thread.Awaiting.Class,
				Attempt:     thread.Awaiting.Attempt,
				MaxAttempts: thread.Awaiting.MaxAttempts,
				Action:      thread.Awaiting.Action,
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
