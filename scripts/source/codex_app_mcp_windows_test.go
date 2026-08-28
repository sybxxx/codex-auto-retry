//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestCodexAppMCPOverrideNormalizesPluginTransportAndCwd(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "desktop-mcp.json")
	document := map[string]any{
		"mcpServers": map[string]any{
			"codex_app": map[string]any{
				"command": "cmd.exe",
				"args":    []string{"/d", "/s", "/c", "call", "server.cmd"},
				"cwd":     ".",
				"enabled": true,
				"type":    "stdio",
				"tools": map[string]any{
					"send_message_to_thread": map[string]any{"approval_mode": "prompt"},
				},
				"env_vars": []string{"CODEX_APP_TOOLS_PIPE_PATH", "PATH"},
			},
		},
	}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	override, err := codexAppMCPOverrideFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(override, "mcp_servers.codex_app=") {
		t.Fatalf("unexpected override prefix: %s", override)
	}
	if strings.Contains(override, "type") {
		t.Fatalf("plugin-only transport field leaked into app-server override: %s", override)
	}
	if !strings.Contains(override, "tools") || !strings.Contains(override, "env_vars") {
		t.Fatalf("Desktop MCP behavior metadata was lost: %s", override)
	}
	if !strings.Contains(override, strconv.Quote(filepath.Clean(root))) {
		t.Fatalf("relative plugin cwd was not normalized: %s", override)
	}
}

func TestCodexAppMCPOverrideRejectsMissingTransport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "desktop-mcp.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":{"codex_app":{"enabled":true}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := codexAppMCPOverrideFromFile(path); err == nil {
		t.Fatal("missing command/url was accepted")
	}
}

func TestLoadCodexAppMCPOverrideReturnsEmptyWhenDesktopFileIsAbsent(t *testing.T) {
	t.Setenv("CODEX_ELECTRON_BUNDLED_PLUGINS_RESOURCES_PATH", filepath.Join(t.TempDir(), "missing"))
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	override, hash, err := loadCodexAppMCPOverride()
	if err != nil {
		t.Fatal(err)
	}
	if override != "" || hash != "" {
		t.Fatalf("missing Desktop MCP file produced an override: override=%q hash=%q", override, hash)
	}
}

func TestSharedServerMigrationIncludesMCPConfigHash(t *testing.T) {
	manager := &sharedServerManager{}
	state := sharedServerState{LaunchMode: sharedServerLaunchMode, MCPConfigHash: "old"}
	if !manager.sharedServerNeedsMigration(state, "new") {
		t.Fatal("changed MCP configuration was not scheduled for migration")
	}
	state.MCPConfigHash = "new"
	if manager.sharedServerNeedsMigration(state, "new") {
		t.Fatal("unchanged launch and MCP configuration still require migration")
	}
}

func TestSharedServerStartArgumentsKeepCodexOverrideBeforeListen(t *testing.T) {
	endpoint := "ws://127.0.0.1:49621"
	override := `mcp_servers.codex_app={"command"="cmd.exe"}`
	args := sharedServerStartArguments(endpoint, override)
	want := []string{
		"-c", "features.code_mode_host=true",
		"app-server", "--analytics-default-enabled",
		"-c", override,
		"--listen", endpoint,
	}
	if len(args) != len(want) {
		t.Fatalf("unexpected shared-server argument count: got=%v want=%v", args, want)
	}
	for index := range want {
		if args[index] != want[index] {
			t.Fatalf("argument %d = %q, want %q; args=%v", index, args[index], want[index], args)
		}
	}
	withoutOverride := sharedServerStartArguments(endpoint, " \t")
	if len(withoutOverride) != 6 || withoutOverride[4] != "--listen" || withoutOverride[5] != endpoint {
		t.Fatalf("blank MCP override changed the base command: %v", withoutOverride)
	}
}

func TestJsonToTOMLInlineRejectsNullAndTrailingValues(t *testing.T) {
	if _, err := jsonToTOMLInline([]byte(`null`)); err == nil {
		t.Fatal("null was accepted as a TOML value")
	}
	if _, err := jsonToTOMLInline([]byte(`{"a":true} {"b":false}`)); err == nil {
		t.Fatal("trailing JSON value was accepted")
	}
}
