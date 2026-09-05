//go:build windows

package main

import (
	"context"
	"strings"
	"testing"
)

func TestDesktopTransportScriptDistinguishesListenServerFromLegacyStdio(t *testing.T) {
	if !strings.Contains(desktopTransportScript, "$legacy = @($owned | Where-Object") {
		t.Fatal("desktop transport probe does not distinguish legacy app-server processes")
	}
	if !strings.Contains(desktopTransportScript, "--listen(?:=|\\s)") {
		t.Fatal("desktop transport probe does not recognize the shared app-server listen flag")
	}
}

func TestDesktopTransportRequiresActualTargetConnection(t *testing.T) {
	for _, required := range []string{"$_.ExecutablePath", "Get-NetTCPConnection -State Established", "$mainIds -contains [int]$_.OwningProcess", "[int]$_.RemotePort -eq $expectedPort", "Write('unknown')"} {
		if !strings.Contains(desktopTransportScript, required) {
			t.Fatalf("transport proof missing: %s", required)
		}
	}
	for _, checker := range []powerShellDesktopTransportChecker{
		{},
		{expectedEndpoint: func() string { return "ws://example.com:49622" }},
	} {
		state, err := checker.State(context.Background())
		if err != nil || state != desktopUnknown {
			t.Fatalf("unverified endpoint was treated as connected: %s %v", state, err)
		}
	}
}
