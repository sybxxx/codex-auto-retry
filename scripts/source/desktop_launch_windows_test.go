//go:build windows

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestSafeLauncherRequiresRPCInitialization(t *testing.T) {
	powerShell, err := resolvePowerShellExecutable("")
	if err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs(filepath.Join("..", "launch-codex.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"healthy", "stalled", "error", "oversized"} {
		t.Run(scenario, func(t *testing.T) {
			upgrader := websocket.Upgrader{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				var request map[string]json.RawMessage
				if conn.ReadJSON(&request) != nil {
					return
				}
				switch scenario {
				case "healthy":
					_ = conn.WriteJSON(map[string]any{"id": 1, "result": map[string]any{"userAgent": "test"}})
				case "error":
					_ = conn.WriteJSON(map[string]any{"id": 1, "error": map[string]any{"code": -1}})
				case "oversized":
					_ = conn.WriteMessage(websocket.TextMessage, []byte(strings.Repeat("x", 70000)))
				}
				_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
				_, _, _ = conn.ReadMessage()
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, powerShell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", ". $env:LAUNCH_PROBE_SCRIPT; [Console]::Write((Test-CodexLaunchWebSocket $env:LAUNCH_PROBE_ENDPOINT))")
			cmd.Env = append(os.Environ(), "LAUNCH_PROBE_SCRIPT="+script, "LAUNCH_PROBE_ENDPOINT=ws"+strings.TrimPrefix(server.URL, "http"))
			cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
			output, err := cmd.CombinedOutput()
			want := "False"
			if scenario == "healthy" {
				want = "True"
			}
			if err != nil || strings.TrimSpace(string(output)) != want {
				t.Fatalf("probe %s: %s %v", scenario, output, err)
			}
		})
	}
}

func TestReadinessRetiresLegacyRouteWithoutKillingActiveBackend(t *testing.T) {
	for _, poisoned := range []bool{false, true} {
		dataDir := t.TempDir()
		manager := newSharedServerManager(defaultConfig(), dataDir, nil)
		manager.desktopRunning = func() bool { return true }
		backup := sharedEnvironmentBackup{SchemaVersion: 1, Name: sharedAppServerEnvironmentName, InstalledValue: manager.Endpoint()}
		if poisoned {
			backup.PreviousPresent = true
			backup.PreviousValue = backup.InstalledValue
		}
		if err := writeJSONAtomic(filepath.Join(dataDir, "environment-backup.json"), backup); err != nil {
			t.Fatal(err)
		}
		if err := writeUserEnvironment(sharedAppServerEnvironmentName, manager.Endpoint()); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if err := manager.EnsureOwnedEnvironment(context.Background()); err != nil {
				t.Fatal(err)
			}
			value, present, err := readUserEnvironment(sharedAppServerEnvironmentName)
			if err != nil || present || value != "" {
				t.Fatalf("route resurrected: %s %v %v", value, present, err)
			}
		}
	}
}

func TestUnknownDesktopTransportCannotDispatch(t *testing.T) {
	controller := &sharedAppServerController{server: staticSharedServer{}, checker: staticDesktopChecker{state: desktopUnknown}}
	_, allowed, _, err := controller.preflight(context.Background(), false)
	if allowed || err != nil {
		t.Fatalf("unproven connection allowed dispatch: %v %v", allowed, err)
	}
	state, err := controller.Readiness(context.Background())
	if err != nil || state != "codex_app_not_ready" {
		t.Fatalf("unexpected readiness: %s %v", state, err)
	}
}
