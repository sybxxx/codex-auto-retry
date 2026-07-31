//go:build windows

package main

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"testing"
)

func TestValidSharedServerEndpointAcceptsOnlyExpectedLoopbackPort(t *testing.T) {
	for _, test := range []struct {
		endpoint string
		port     int
		valid    bool
	}{
		{endpoint: "ws://127.0.0.1:49321", port: 49321, valid: true},
		{endpoint: "ws://localhost:49321", port: 49321, valid: false},
		{endpoint: "wss://127.0.0.1:49321", port: 49321, valid: false},
		{endpoint: "ws://127.0.0.1:49322", port: 49321, valid: false},
		{endpoint: "ws://user@127.0.0.1:49321", port: 49321, valid: false},
		{endpoint: "ws://127.0.0.1:49321/path", port: 49321, valid: false},
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
