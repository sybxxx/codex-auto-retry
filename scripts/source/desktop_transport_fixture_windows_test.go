//go:build windows

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDesktopTransportClientEvidence(t *testing.T) {
	exe := `C:\Program Files\WindowsApps\OpenAI.Codex_test\app\ChatGPT.exe`
	process := func(id, parent int, name, path, cmd string) map[string]any {
		return map[string]any{"ProcessId": id, "ParentProcessId": parent, "Name": name, "ExecutablePath": path, "CommandLine": cmd}
	}
	main := process(10, 1, "ChatGPT.exe", exe, `"`+exe+`"`)
	network := process(11, 10, "ChatGPT.exe", exe, "--type=utility --utility-sub-type=network.mojom.NetworkService")
	for _, tc := range []struct {
		name   string
		owner  int
		legacy bool
		want   string
	}{
		{"listener_only", 0, false, "unknown"},
		{"official_plus_unrelated_listener", 0, true, "legacy_stdio"},
		{"desktop_connected", 10, false, "shared_server"},
		{"network_child_connected", 11, false, "shared_server"},
		{"arbitrary_task_client", 99, false, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			processes := []map[string]any{main, network, process(200, 500, "codex.exe", `C:\codex.exe`, "app-server --listen ws://127.0.0.1:49622")}
			if tc.legacy {
				processes = append(processes, process(201, 10, "codex.exe", `C:\codex.exe`, "app-server"))
			}
			connections := []map[string]any{}
			if tc.owner != 0 {
				connections = append(connections, map[string]any{"OwningProcess": tc.owner, "RemoteAddress": "127.0.0.1", "RemotePort": 49622})
			}
			fixture, _ := json.Marshal(map[string]any{"processes": processes, "connections": connections})
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			ps, err := resolvePowerShellExecutable("")
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(ctx, ps, "-NoProfile", "-NonInteractive", "-Command", "-")
			cmd.Env = append(os.Environ(), "TRANSPORT_FIXTURE="+string(fixture))
			cmd.Stdin = strings.NewReader(powerShellScriptInput(`$fixture = $env:TRANSPORT_FIXTURE | ConvertFrom-Json
function Get-CimInstance { param($ClassName,$ErrorAction) return $fixture.processes }
function Get-NetTCPConnection { param($State,$ErrorAction) return $fixture.connections }
$expectedPort=49622
` + desktopTransportScript))
			cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
			out, err := cmd.CombinedOutput()
			if err != nil || strings.TrimSpace(string(out)) != tc.want {
				t.Fatalf("got %q %v want %s", out, err, tc.want)
			}
		})
	}
}
