//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSharedRecoveryRespectsConsentAndExpiry(t *testing.T) {
	oldProbe, oldPrepare := desktopRunningProbe, recoverSharedBackend
	t.Cleanup(func() { desktopRunningProbe = oldProbe; recoverSharedBackend = oldPrepare })
	for _, scenario := range []string{"success", "expired", "disabled", "desktop", "memory", "failed", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := isolatedConfig(t.TempDir())
			cfg.SharedAppServerEnabled = false
			cfg.SharedAppServerRequested = scenario != "disabled"
			d := newTestDaemon(t, cfg, successfulRunner())
			now := time.Now().UTC()
			desktopRunningProbe = func() bool { return scenario == "desktop" }
			calls := 0
			recoverSharedBackend = func(ctx context.Context, dir string, c Config) (Config, error) {
				calls++
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("unbounded recovery")
				}
				if scenario == "failed" {
					return c, errors.New("fake unavailable")
				}
				if scenario == "cancelled" {
					_, _ = updateConfigFile(filepath.Join(dir, "config.json"), func(c *Config) error { c.SharedAppServerRequested = false; return nil })
				}
				return c, nil
			}
			if scenario == "memory" {
				_ = recordSharedUnavailable(d.dataDir, "shared_app_server_memory_limit_exceeded")
			}
			expiry := now.Add(10 * time.Second)
			if scenario == "expired" {
				expiry = now.Add(-time.Second)
			}
			request := sharedRecoveryRequest{ID: strings.Repeat("a", 32), ExpiresAt: expiry}
			path := filepath.Join(d.dataDir, "shared-recovery-request.json")
			if err := writeJSONAtomic(path, request); err != nil {
				t.Fatal(err)
			}
			d.processSharedRecovery(context.Background(), now)
			d.processSharedRecovery(context.Background(), now)
			resultBytes, err := os.ReadFile(filepath.Join(d.dataDir, "shared-recovery-result.json"))
			if err != nil {
				t.Fatal(err)
			}
			var result map[string]any
			_ = json.Unmarshal(resultBytes, &result)
			expected := map[string]string{"success": "prepared", "expired": "request_expired", "disabled": "preference_disabled", "desktop": "desktop_already_running", "memory": "manual_recovery_required", "failed": "backend_prepare_failed", "cancelled": "recovery_cancelled"}[scenario]
			if result["reason"] != expected {
				t.Fatalf("result %v expected %s", result, expected)
			}
			loaded, err := loadOrCreateConfig(filepath.Join(d.dataDir, "config.json"))
			if err != nil {
				t.Fatal(err)
			}
			if loaded.SharedAppServerEnabled != (scenario == "success") {
				t.Fatal("runtime gate inconsistent")
			}
			if scenario == "failed" && !loaded.SharedAppServerRequested {
				t.Fatal("temporary failure erased preference")
			}
			if calls > 1 {
				t.Fatal("request replayed")
			}
			if _, err := os.Stat(path + ".processing"); !os.IsNotExist(err) {
				t.Fatal("claimed request left behind")
			}
		})
	}
}
