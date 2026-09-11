package main

import "testing"

func TestCapabilityForControllerState(t *testing.T) {
	tests := []struct {
		state     string
		shared    bool
		transport string
		recovery  string
		automatic bool
		reason    string
	}{
		{state: "ready", shared: true, transport: "shared_websocket", recovery: "shared_websocket", automatic: true, reason: "verified"},
		{state: "official_ipc_ready", shared: true, transport: "official_ipc", recovery: "official_ipc", automatic: true, reason: "verified"},
		{state: "codex_restart_required", shared: true, transport: "official_stdio", recovery: "safe_launcher_required", reason: "official_stdio_not_externally_controllable"},
		{state: "shared_app_server_disabled", shared: false, transport: "official_stdio", recovery: "none", reason: "shared_backend_disabled"},
		{state: "codex_not_running", transport: "stopped", recovery: "none", reason: "codex_not_running"},
		{state: "codex_app_not_ready", transport: "unknown", recovery: "none", reason: "desktop_connection_not_proven"},
	}
	for _, test := range tests {
		got := capabilityForControllerState(test.state, test.shared)
		if got.Transport != test.transport || got.RecoveryMode != test.recovery || got.Automatic != test.automatic || got.Reason != test.reason {
			t.Fatalf("capability for %q = %+v, want transport=%q recovery=%q automatic=%v reason=%q", test.state, got, test.transport, test.recovery, test.automatic, test.reason)
		}
	}
}
