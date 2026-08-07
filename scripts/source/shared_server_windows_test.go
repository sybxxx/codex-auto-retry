//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

func TestValidSharedServerEndpointAcceptsOnlyExpectedLoopbackPort(t *testing.T) {
	port := defaultConfig().SharedAppServerPort
	for _, test := range []struct {
		endpoint string
		port     int
		valid    bool
	}{
		{endpoint: fmt.Sprintf("ws://127.0.0.1:%d", port), port: port, valid: true},
		{endpoint: fmt.Sprintf("ws://localhost:%d", port), port: port, valid: false},
		{endpoint: fmt.Sprintf("wss://127.0.0.1:%d", port), port: port, valid: false},
		{endpoint: fmt.Sprintf("ws://127.0.0.1:%d", port+1), port: port, valid: false},
		{endpoint: fmt.Sprintf("ws://user@127.0.0.1:%d", port), port: port, valid: false},
		{endpoint: fmt.Sprintf("ws://127.0.0.1:%d/path", port), port: port, valid: false},
	} {
		if got := validSharedServerEndpoint(test.endpoint, test.port); got != test.valid {
			t.Fatalf("validSharedServerEndpoint(%q, %d) = %v, want %v", test.endpoint, test.port, got, test.valid)
		}
	}
}

func TestSharedServerRefusesUnownedResponsivePort(t *testing.T) {
	fake := newFakeAppServer(t, "019fa94e-0103-7183-b405-36bd307b6db7")
	parsed, err := url.Parse(fake.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	config := defaultConfig()
	config.SharedAppServerPort = port
	manager := newSharedServerManager(config, t.TempDir(), nil)
	if err := manager.Ensure(context.Background()); !errors.Is(err, errSharedServerPortConflict) {
		t.Fatalf("responsive unowned port was accepted: %v", err)
	}
}

func TestSharedServerPortPreflightClassifiesWindowsErrors(t *testing.T) {
	if !isWindowsPortReservedError(fmt.Errorf("bind: %w", syscall.Errno(10013))) {
		t.Fatal("WSAEACCES was not classified as a Windows-reserved port")
	}
	if !isWindowsPortOccupiedError(fmt.Errorf("bind: %w", syscall.Errno(10048))) {
		t.Fatal("WSAEADDRINUSE was not classified as an occupied port")
	}
}

func TestSharedServerPortPreflightDetectsOccupiedPort(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	if err := checkSharedServerPort(port); !errors.Is(err, errSharedServerPortConflict) {
		t.Fatalf("occupied port was not rejected: %v", err)
	}
}

func TestReplaceEnvironmentValueReplacesCaseInsensitively(t *testing.T) {
	result := replaceEnvironmentValue([]string{"Path=a", "codex_home=old", "OTHER=b"}, "CODEX_HOME", "new")
	found := 0
	for _, item := range result {
		if item == "CODEX_HOME=new" {
			found++
		}
		if item == "codex_home=old" {
			t.Fatal("old environment value was retained")
		}
	}
	if found != 1 {
		t.Fatalf("replacement was not added exactly once: %v", result)
	}
}

func TestSharedServerUsesConfiguredCodexHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	manager := newSharedServerManager(defaultConfig(), t.TempDir(), nil)
	if !manager.SupportsHome(home) {
		t.Fatalf("shared server ignored CODEX_HOME: %s", manager.codexHome)
	}
}

func TestSharedServerStateMarksHiddenInheritedConsole(t *testing.T) {
	state := sharedServerState{LaunchMode: sharedServerLaunchMode, Owner: sharedServerOwner, Version: appVersion}
	if state.LaunchMode != "hidden_inherited_console_v1" {
		t.Fatalf("unexpected shared-server launch mode: %s", state.LaunchMode)
	}
	if state.Owner != sharedServerOwner || state.Version != appVersion {
		t.Fatalf("shared-server ownership marker is incomplete: %+v", state)
	}
}

func TestStopOwnedRemovesStaleStateAfterOwnedProcessExits(t *testing.T) {
	dataDir := t.TempDir()
	manager := newSharedServerManager(defaultConfig(), dataDir, nil)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	state := sharedServerState{
		PID:            4_000_000,
		Endpoint:       manager.Endpoint(),
		CodexHome:      manager.codexHome,
		Executable:     executable,
		ExecutableHash: executableHash(executable),
		Owner:          sharedServerOwner,
		Version:        appVersion,
	}
	statePath := filepath.Join(dataDir, "shared-server.json")
	if err := writeJSONAtomic(statePath, state); err != nil {
		t.Fatal(err)
	}
	if err := manager.StopOwned(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale owned state was not removed: %v", err)
	}
}
