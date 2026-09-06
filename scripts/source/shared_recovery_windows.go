//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

type sharedRecoveryRequest struct {
	ID        string    `json:"id"`
	ExpiresAt time.Time `json:"expires_at"`
}

type sharedAvailability struct {
	Reason                   string    `json:"reason"`
	AutomaticRecoveryAllowed bool      `json:"automatic_recovery_allowed"`
	UpdatedAt                time.Time `json:"updated_at"`
}

var sharedRecoveryID = regexp.MustCompile(`^[a-f0-9]{32}$`)
var recoverSharedBackend = enableSharedAppServer

func recordSharedUnavailable(dataDir, reason string) error {
	return writeJSONAtomic(filepath.Join(dataDir, "shared-availability.json"), sharedAvailability{
		Reason: reason, UpdatedAt: time.Now().UTC(),
		AutomaticRecoveryAllowed: reason != "shared_app_server_memory_limit_exceeded" && reason != "memory_limit_exceeded",
	})
}

// Only the worker performs automatic preparation, serialized with its normal
// cleanup tick. Requests expire so a launch that fell back cannot wake later.
func (d *daemon) processSharedRecovery(ctx context.Context, now time.Time) {
	path := filepath.Join(d.dataDir, "shared-recovery-request.json")
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	if info.Size() > 4096 {
		_ = os.Remove(path)
		return
	}
	claimed := path + ".processing"
	// A singleton worker can only see a claimed file left by its previous process.
	_ = os.Remove(claimed)
	if os.Rename(path, claimed) != nil {
		return
	}
	defer os.Remove(claimed)
	data, err := os.ReadFile(claimed)
	var request sharedRecoveryRequest
	if err != nil || json.Unmarshal(data, &request) != nil || !sharedRecoveryID.MatchString(request.ID) {
		return
	}
	reason := "request_expired"
	defer func() {
		_ = writeJSONAtomic(filepath.Join(d.dataDir, "shared-recovery-result.json"), map[string]any{
			"id": request.ID, "reason": reason, "completed_at": time.Now().UTC(),
		})
		d.logger.Printf("shared launch recovery category=%s", reason)
	}()
	if !request.ExpiresAt.After(now) || request.ExpiresAt.Sub(now) > 15*time.Second {
		return
	}
	if desktopRunningProbe() {
		reason = "desktop_already_running"
		return
	}
	d.mu.Lock()
	busy := len(d.active) != 0
	d.mu.Unlock()
	if busy {
		reason = "worker_busy"
		return
	}
	configPath := filepath.Join(d.dataDir, "config.json")
	config, err := loadOrCreateConfig(configPath)
	if err != nil {
		reason = "config_unavailable"
		return
	}
	if !config.SharedAppServerRequested {
		reason = "preference_disabled"
		return
	}
	var availability sharedAvailability
	availabilityData, readErr := os.ReadFile(filepath.Join(d.dataDir, "shared-availability.json"))
	if readErr == nil {
		if json.Unmarshal(availabilityData, &availability) != nil || !availability.AutomaticRecoveryAllowed {
			reason = "manual_recovery_required"
			return
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		reason = "availability_unreadable"
		return
	}
	deadline := now.Add(6 * time.Second)
	if request.ExpiresAt.Before(deadline) {
		deadline = request.ExpiresAt
	}
	recoveryCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if err = failOpenSharedBackendCleanup(recoveryCtx, d.dataDir, config); err != nil {
		reason = "cleanup_not_safe"
		return
	}
	prepared, err := recoverSharedBackend(recoveryCtx, d.dataDir, config)
	if err != nil {
		reason = "backend_prepare_failed"
		_ = recordSharedUnavailable(d.dataDir, reason)
		return
	}
	_, err = updateConfigFile(configPath, func(latest *Config) error {
		if !latest.SharedAppServerRequested || recoveryCtx.Err() != nil || desktopRunningProbe() {
			return errors.New("shared preparation was superseded")
		}
		latest.SharedAppServerEnabled = true
		latest.SharedAppServerPort = prepared.SharedAppServerPort
		return nil
	})
	if err != nil {
		reason = "recovery_cancelled"
		cleanupSharedServer(newSharedServerManager(prepared, d.dataDir, d.logger))
		return
	}
	_ = os.Remove(filepath.Join(d.dataDir, sharedFailOpenMarkerName))
	_ = os.Remove(filepath.Join(d.dataDir, "shared-availability.json"))
	d.mu.Lock()
	d.reloadConfigLocked()
	d.lastControllerProbe = time.Time{}
	d.mu.Unlock()
	reason = "prepared"
}
