//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
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
		{endpoint: "ws://127.0.0.1:0", port: 0, valid: false},
		{endpoint: "ws://127.0.0.1:65536", port: 65536, valid: false},
		{endpoint: fmt.Sprintf("ws://user@127.0.0.1:%d", port), port: port, valid: false},
		{endpoint: fmt.Sprintf("ws://127.0.0.1:%d/path", port), port: port, valid: false},
	} {
		if got := validSharedServerEndpoint(test.endpoint, test.port); got != test.valid {
			t.Fatalf("validSharedServerEndpoint(%q, %d) = %v, want %v", test.endpoint, test.port, got, test.valid)
		}
	}
}

func TestSharedServerSelectsAlternatePortForUnownedResponsivePort(t *testing.T) {
	fake := newFakeAppServer(t, "019fa94e-0103-7183-b405-36bd307b6db7")
	parsed, err := url.Parse(fake.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	selected, err := selectAvailableSharedServerPort(port)
	if err != nil {
		t.Fatalf("responsive unowned port blocked safe alternate selection: %v", err)
	}
	if selected == port {
		t.Fatalf("preferred occupied port was retained: port=%d", selected)
	}
}

func TestSharedServerDoesNotAutoMigratePreferredPortDuringStartup(t *testing.T) {
	fake := newFakeAppServer(t, "019fa94e-0103-7183-b405-36bd307b6db8")
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
	dataDir := t.TempDir()
	if err := writeJSONAtomic(filepath.Join(dataDir, "config.json"), config); err != nil {
		t.Fatal(err)
	}
	manager := newSharedServerManager(config, dataDir, nil)
	if err := manager.Ensure(context.Background()); !errors.Is(err, errSharedServerPortConflict) {
		t.Fatalf("startup unexpectedly migrated around an unowned port: %v", err)
	}
	if manager.config.SharedAppServerPort != port {
		t.Fatalf("startup changed the configured port after an unowned conflict: %d", manager.config.SharedAppServerPort)
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

func TestSelectAvailableSharedServerPortSkipsOccupiedPort(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	preferred := listener.Addr().(*net.TCPAddr).Port
	selected, err := selectAvailableSharedServerPort(preferred)
	if err != nil {
		t.Fatal(err)
	}
	if selected == preferred {
		t.Fatalf("occupied preferred port was selected: %d", selected)
	}
	if err := checkSharedServerPort(selected); err != nil {
		t.Fatalf("selected alternate port is not available: %v", err)
	}
}

func TestSelectAvailableSharedServerPortKeepsFreePreferredPort(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	selected, err := selectAvailableSharedServerPort(port)
	if err != nil {
		t.Fatal(err)
	}
	if selected != port {
		t.Fatalf("free preferred port was unnecessarily changed: got %d want %d", selected, port)
	}
}

func TestStopOwnedCleansAlternatePortWhenConfigRetainsPreferredPort(t *testing.T) {
	dataDir := t.TempDir()
	config := defaultConfig()
	config.SharedAppServerPort = 49621
	manager := newSharedServerManager(config, dataDir, nil)
	manager.desktopRunning = func() bool { return false }
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	state := sharedServerState{
		PID:            4_000_000,
		Endpoint:       "ws://127.0.0.1:49622",
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
		t.Fatalf("alternate-port state was not removed: %v", err)
	}
}

func TestStopOwnedDoesNotDependOnCurrentCodexHome(t *testing.T) {
	dataDir := t.TempDir()
	manager := newSharedServerManager(defaultConfig(), dataDir, nil)
	manager.desktopRunning = func() bool { return false }
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	state := sharedServerState{
		PID:            4_000_000,
		Endpoint:       manager.Endpoint(),
		CodexHome:      filepath.Join(t.TempDir(), ".codex"),
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
		t.Fatalf("owned state tied to a previous Codex home was not removed: %v", err)
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

func TestOwnedSharedEnvironmentRestoresMissingEndpoint(t *testing.T) {
	name := fmt.Sprintf("CODEX_AUTO_RETRY_TEST_%d", time.Now().UnixNano())
	dataDir := t.TempDir()
	previous, present, err := readUserEnvironment(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = restoreUserEnvironment(name, previous, present)
	})
	if err := restoreUserEnvironment(name, "", false); err != nil {
		t.Fatal(err)
	}

	endpoint := fmt.Sprintf("ws://127.0.0.1:%d", defaultConfig().SharedAppServerPort)
	result, err := setOwnedSharedEnvironmentNamed(dataDir, name, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Fatal("missing shared endpoint was not reported as changed")
	}
	value, present, err := readUserEnvironment(name)
	if err != nil || !present || value != endpoint {
		t.Fatalf("missing shared endpoint was not restored: value=%q present=%v err=%v", value, present, err)
	}
	var backup sharedEnvironmentBackup
	data, err := os.ReadFile(filepath.Join(dataDir, "environment-backup.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &backup); err != nil {
		t.Fatal(err)
	}
	if backup.Name != name || backup.InstalledValue != endpoint || backup.PreviousPresent {
		t.Fatalf("ownership backup did not describe the repaired endpoint: %+v", backup)
	}
}

func TestOwnedSharedEnvironmentRefusesDifferentUserValue(t *testing.T) {
	name := fmt.Sprintf("CODEX_AUTO_RETRY_TEST_%d", time.Now().UnixNano())
	dataDir := t.TempDir()
	previous, present, err := readUserEnvironment(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = restoreUserEnvironment(name, previous, present)
	})
	if err := writeUserEnvironment(name, "ws://127.0.0.1:1"); err != nil {
		t.Fatal(err)
	}
	desired := fmt.Sprintf("ws://127.0.0.1:%d", defaultConfig().SharedAppServerPort)
	if _, err := setOwnedSharedEnvironmentNamed(dataDir, name, desired); !errors.Is(err, errSharedAppServerEnvironmentConflict) {
		t.Fatalf("different user endpoint was not rejected safely: %v", err)
	}
	value, stillPresent, readErr := readUserEnvironment(name)
	if readErr != nil || !stillPresent || value != "ws://127.0.0.1:1" {
		t.Fatalf("conflicting user endpoint was changed: value=%q present=%v err=%v", value, stillPresent, readErr)
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

func TestSharedServerProcessCreationTimePreventsPIDReuse(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	if !processCreationTimeMatches(now, now.Add(5*time.Second).Format(time.RFC3339Nano)) {
		t.Fatal("a process created within the ownership tolerance was rejected")
	}
	if !processCreationTimeMatches(now, now.Add(-5*time.Second).Format(time.RFC3339Nano)) {
		t.Fatal("a process created within the negative ownership tolerance was rejected")
	}
	for _, creation := range []string{
		now.Add(3 * time.Minute).Format(time.RFC3339Nano),
		now.Add(-3 * time.Minute).Format(time.RFC3339Nano),
		"not-a-timestamp",
	} {
		if processCreationTimeMatches(now, creation) {
			t.Fatalf("PID reuse candidate was accepted: %q", creation)
		}
	}
	if processCreationTimeMatches(time.Time{}, now.Format(time.RFC3339Nano)) {
		t.Fatal("legacy state without a creation timestamp was treated as owned")
	}
}

func TestSharedServerAdoptsStateFromOlderPluginRelease(t *testing.T) {
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
		Version:        "0.7.3",
	}
	if !manager.sharedServerStateOwnedByPlugin(state) {
		t.Fatal("a prior plugin release was rejected despite matching ownership evidence")
	}
	if err := manager.adoptSharedServerState(&state); err != nil {
		t.Fatal(err)
	}
	if state.Version != appVersion {
		t.Fatalf("shared server state was not adopted: %q", state.Version)
	}
	var written sharedServerState
	data, err := os.ReadFile(filepath.Join(dataDir, "shared-server.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &written); err != nil {
		t.Fatal(err)
	}
	if written.Version != appVersion {
		t.Fatalf("adopted state was not persisted: %q", written.Version)
	}
}

func TestStopOwnedRemovesStaleStateAfterOwnedProcessExits(t *testing.T) {
	dataDir := t.TempDir()
	manager := newSharedServerManager(defaultConfig(), dataDir, nil)
	manager.desktopRunning = func() bool { return false }
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

func TestStopOwnedRefusesLiveLegacyStateWithoutCreationTime(t *testing.T) {
	dataDir := t.TempDir()
	manager := newSharedServerManager(defaultConfig(), dataDir, nil)
	manager.desktopRunning = func() bool { return false }
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(dataDir, "shared-server.json")
	if err := writeJSONAtomic(statePath, sharedServerState{
		PID:            os.Getpid(),
		Endpoint:       manager.Endpoint(),
		CodexHome:      manager.codexHome,
		Executable:     executable,
		ExecutableHash: executableHash(executable),
		Owner:          sharedServerOwner,
		Version:        appVersion,
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.StopOwned(context.Background()); !errors.Is(err, errSharedServerOwnershipUnknown) {
		t.Fatalf("live legacy state was not held for manual ownership verification: %v", err)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("unknown live state was removed instead of remaining visible: %v", err)
	}
}

func TestCleanupIfUnusedDefersLiveOwnedServerWhileDesktopRuns(t *testing.T) {
	dataDir := t.TempDir()
	manager := newSharedServerManager(defaultConfig(), dataDir, nil)
	manager.desktopRunning = func() bool { return true }
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	state := sharedServerState{
		PID:            os.Getpid(),
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
	if err := manager.CleanupIfUnused(context.Background()); !errors.Is(err, errSharedServerMigrationDeferred) {
		t.Fatalf("live owned server was cleaned while Desktop was active: %v", err)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("deferred cleanup removed live ownership state: %v", err)
	}
}

func TestStopOwnedDefersWhileDesktopIsRunning(t *testing.T) {
	dataDir := t.TempDir()
	manager := newSharedServerManager(defaultConfig(), dataDir, nil)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	manager.desktopRunning = func() bool { return true }
	state := sharedServerState{
		PID:            os.Getpid(),
		Endpoint:       manager.Endpoint(),
		CodexHome:      manager.codexHome,
		Executable:     executable,
		ExecutableHash: executableHash(executable),
		Owner:          sharedServerOwner,
		Version:        appVersion,
	}
	if err := writeJSONAtomic(filepath.Join(dataDir, "shared-server.json"), state); err != nil {
		t.Fatal(err)
	}
	if err := manager.StopOwned(context.Background()); !errors.Is(err, errSharedServerMigrationDeferred) {
		t.Fatalf("owned endpoint was torn down while Desktop was active: %v", err)
	}
}

func TestSharedServerDoesNotGuessUnrecordedLegacyEndpointOwnership(t *testing.T) {
	manager := newSharedServerManager(defaultConfig(), t.TempDir(), nil)
	if endpoints := manager.ownedLegacyEndpoints(context.Background()); len(endpoints) != 0 {
		t.Fatalf("unrecorded endpoint was treated as plugin-owned: %v", endpoints)
	}
}

func TestDetachOwnedSharedEnvironmentClearsPreviousEndpointBeforeStartup(t *testing.T) {
	dataDir := t.TempDir()
	previous, present, err := readUserEnvironment(sharedAppServerEnvironmentName)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restoreUserEnvironment(sharedAppServerEnvironmentName, previous, present) })
	if err := restoreUserEnvironment(sharedAppServerEnvironmentName, "", false); err != nil {
		t.Fatal(err)
	}
	endpoint := fmt.Sprintf("ws://127.0.0.1:%d", defaultConfig().SharedAppServerPort)
	if _, err := setOwnedSharedEnvironment(dataDir, endpoint); err != nil {
		t.Fatal(err)
	}
	result, err := restoreOwnedSharedEnvironment(dataDir)
	if err != nil || !result.Restored {
		t.Fatalf("owned endpoint was not detached before startup: result=%+v err=%v", result, err)
	}
	value, stillPresent, err := readUserEnvironment(sharedAppServerEnvironmentName)
	if err != nil || stillPresent || value != "" {
		t.Fatalf("detached endpoint remained in the user environment: value=%q present=%v err=%v", value, stillPresent, err)
	}
}

func TestFailOpenCleanupRestoresOwnedLegacyEndpointWithoutBackup(t *testing.T) {
	dataDir := t.TempDir()
	previous, present, err := readUserEnvironment(sharedAppServerEnvironmentName)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restoreUserEnvironment(sharedAppServerEnvironmentName, previous, present) })
	if err := restoreUserEnvironment(sharedAppServerEnvironmentName, "", false); err != nil {
		t.Fatal(err)
	}
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
	if err := writeJSONAtomic(filepath.Join(dataDir, "shared-server.json"), state); err != nil {
		t.Fatal(err)
	}
	if _, err := setOwnedSharedEnvironment(dataDir, manager.Endpoint()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dataDir, "environment-backup.json")); err != nil {
		t.Fatal(err)
	}
	endpoints := manager.ownedLegacyEndpoints(context.Background())
	if len(endpoints) != 1 || endpoints[0] != manager.Endpoint() {
		t.Fatalf("owned legacy endpoint was not discovered: %v", endpoints)
	}
	result, err := restoreOwnedSharedEnvironment(dataDir, endpoints...)
	if err != nil || !result.Restored {
		t.Fatalf("owned legacy endpoint was not restored: result=%+v err=%v", result, err)
	}
	value, stillPresent, err := readUserEnvironment(sharedAppServerEnvironmentName)
	if err != nil || stillPresent || value != "" {
		t.Fatalf("legacy endpoint remained after fail-open cleanup: value=%q present=%v err=%v", value, stillPresent, err)
	}
}

func TestCleanupSharedBackendFallsBackWhenConfigIsCorrupt(t *testing.T) {
	previousDesktopProbe := desktopRunningProbe
	desktopRunningProbe = func() bool { return false }
	t.Cleanup(func() { desktopRunningProbe = previousDesktopProbe })
	dataDir := t.TempDir()
	previous, present, err := readUserEnvironment(sharedAppServerEnvironmentName)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restoreUserEnvironment(sharedAppServerEnvironmentName, previous, present) })
	if err := restoreUserEnvironment(sharedAppServerEnvironmentName, "", false); err != nil {
		t.Fatal(err)
	}

	// Use the legacy default to prove cleanup reads the actual endpoint from
	// shared-server.json instead of assuming the current default port.
	config := defaultConfig()
	config.SharedAppServerPort = legacyDefaultSharedAppServerPort
	manager := newSharedServerManager(config, dataDir, nil)
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
	if err := writeJSONAtomic(filepath.Join(dataDir, "shared-server.json"), state); err != nil {
		t.Fatal(err)
	}
	if _, err := setOwnedSharedEnvironment(dataDir, manager.Endpoint()); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dataDir, "config.json")
	corrupt := []byte("{ this is not valid json\n")
	if err := os.WriteFile(configPath, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := cleanupSharedBackend(context.Background(), dataDir); err != nil {
		t.Fatalf("fallback cleanup failed: %v", err)
	}
	if data, err := os.ReadFile(configPath); err != nil || string(data) != string(corrupt) {
		t.Fatalf("fallback cleanup changed the corrupt config: data=%q err=%v", data, err)
	}
	value, stillPresent, err := readUserEnvironment(sharedAppServerEnvironmentName)
	if err != nil || stillPresent || value != "" {
		t.Fatalf("fallback cleanup left the owned endpoint installed: value=%q present=%v err=%v", value, stillPresent, err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "environment-backup.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("environment ownership backup was not consumed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "shared-server.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned shared-server state was not removed: %v", err)
	}
}

func TestCleanupSharedBackendRestoresEndpointWhenOnlyBackupExists(t *testing.T) {
	previousDesktopProbe := desktopRunningProbe
	desktopRunningProbe = func() bool { return true }
	t.Cleanup(func() { desktopRunningProbe = previousDesktopProbe })
	dataDir := t.TempDir()
	previous, present, err := readUserEnvironment(sharedAppServerEnvironmentName)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restoreUserEnvironment(sharedAppServerEnvironmentName, previous, present) })
	if err := restoreUserEnvironment(sharedAppServerEnvironmentName, "", false); err != nil {
		t.Fatal(err)
	}
	endpoint := "ws://127.0.0.1:51234"
	if _, err := setOwnedSharedEnvironment(dataDir, endpoint); err != nil {
		t.Fatal(err)
	}
	config := defaultConfig()
	config.SharedAppServerEnabled = true
	if err := writeJSONAtomic(filepath.Join(dataDir, "config.json"), config); err != nil {
		t.Fatal(err)
	}
	if err := disableSharedAppServer(context.Background(), dataDir, config); err != nil {
		t.Fatalf("backup-only cleanup failed while Desktop was running: %v", err)
	}
	value, stillPresent, err := readUserEnvironment(sharedAppServerEnvironmentName)
	if err != nil || stillPresent || value != "" {
		t.Fatalf("backup-only cleanup left endpoint installed: value=%q present=%v err=%v", value, stillPresent, err)
	}
}

func TestStartupSharedServerStateFailsOpenForDeadOwnedProcess(t *testing.T) {
	previousRunning := startupSharedServerProcessIsRunning
	previousOwnership := startupSharedServerOwnershipProbe
	previousProbe := startupSharedServerEndpointProbe
	startupSharedServerProcessIsRunning = func(int) bool { return false }
	startupSharedServerOwnershipProbe = func(context.Context, *sharedServerManager, sharedServerState) bool { return false }
	startupSharedServerEndpointProbe = func(context.Context, string) error { return nil }
	t.Cleanup(func() {
		startupSharedServerProcessIsRunning = previousRunning
		startupSharedServerOwnershipProbe = previousOwnership
		startupSharedServerEndpointProbe = previousProbe
	})

	dataDir := t.TempDir()
	config := defaultConfig()
	config.SharedAppServerEnabled = true
	configPath := filepath.Join(dataDir, "config.json")
	if err := writeJSONAtomic(configPath, config); err != nil {
		t.Fatal(err)
	}
	manager := newSharedServerManager(config, dataDir, nil)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(dataDir, "shared-server.json")
	if err := writeJSONAtomic(statePath, sharedServerState{
		PID:            4_000_000,
		Endpoint:       manager.Endpoint(),
		CodexHome:      manager.codexHome,
		Executable:     executable,
		ExecutableHash: executableHash(executable),
		Owner:          sharedServerOwner,
		Version:        appVersion,
	}); err != nil {
		t.Fatal(err)
	}
	result := prepareSharedBackendAtStartup(context.Background(), configPath, dataDir, config)
	if result.Reason != "codex_background_channel_unavailable" || !result.CanContinue || result.Config.SharedAppServerEnabled {
		t.Fatalf("startup did not fail open through the main startup gate: %+v", result)
	}
	if result.CleanupErr != nil {
		t.Fatalf("startup fail-open cleanup failed: %v", result.CleanupErr)
	}
	loaded, err := loadOrCreateConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SharedAppServerEnabled {
		t.Fatal("startup fail-open did not persist shared mode disabled")
	}
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dead owned state was not removed: %v", err)
	}
}

func TestStartupSharedServerStateFailsOpenForMissingOwnedState(t *testing.T) {
	previousRunning := startupSharedServerProcessIsRunning
	previousOwnership := startupSharedServerOwnershipProbe
	previousProbe := startupSharedServerEndpointProbe
	startupSharedServerProcessIsRunning = func(int) bool { return false }
	startupSharedServerOwnershipProbe = func(context.Context, *sharedServerManager, sharedServerState) bool { return false }
	startupSharedServerEndpointProbe = func(context.Context, string) error { return nil }
	t.Cleanup(func() {
		startupSharedServerProcessIsRunning = previousRunning
		startupSharedServerOwnershipProbe = previousOwnership
		startupSharedServerEndpointProbe = previousProbe
	})

	dataDir := t.TempDir()
	config := defaultConfig()
	config.SharedAppServerEnabled = true
	configPath := filepath.Join(dataDir, "config.json")
	if err := writeJSONAtomic(configPath, config); err != nil {
		t.Fatal(err)
	}
	previous, present, err := readUserEnvironment(sharedAppServerEnvironmentName)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restoreUserEnvironment(sharedAppServerEnvironmentName, previous, present) })
	if err := restoreUserEnvironment(sharedAppServerEnvironmentName, "", false); err != nil {
		t.Fatal(err)
	}
	manager := newSharedServerManager(config, dataDir, nil)
	if _, err := setOwnedSharedEnvironment(dataDir, manager.Endpoint()); err != nil {
		t.Fatal(err)
	}
	if err := manager.checkSharedServerStartupState(context.Background()); !errors.Is(err, errSharedServerUnavailable) {
		t.Fatalf("missing owned state was not rejected while its endpoint backup remained: %v", err)
	}
	if _, err := failOpenSharedAppServer(context.Background(), configPath, dataDir, config); err != nil {
		t.Fatalf("missing-state fail-open failed: %v", err)
	}
	value, stillPresent, err := readUserEnvironment(sharedAppServerEnvironmentName)
	if err != nil || stillPresent || value != "" {
		t.Fatalf("plugin endpoint was not cleared after missing-state fail-open: value=%q present=%v err=%v", value, stillPresent, err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "environment-backup.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("environment ownership backup was not consumed: %v", err)
	}
}

func TestStartupFailOpenStopsWhenEndpointOwnershipIsUnknown(t *testing.T) {
	dataDir := t.TempDir()
	config := defaultConfig()
	config.SharedAppServerEnabled = true
	configPath := filepath.Join(dataDir, "config.json")
	if err := writeJSONAtomic(configPath, config); err != nil {
		t.Fatal(err)
	}
	previous, present, err := readUserEnvironment(sharedAppServerEnvironmentName)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restoreUserEnvironment(sharedAppServerEnvironmentName, previous, present) })
	unknownEndpoint := "ws://127.0.0.1:49621"
	if err := writeUserEnvironment(sharedAppServerEnvironmentName, unknownEndpoint); err != nil {
		t.Fatal(err)
	}

	result := completeStartupFailOpen(context.Background(), configPath, dataDir, config)
	if result.CanContinue || !errors.Is(result.CleanupErr, errSharedAppServerEnvironmentConflict) {
		t.Fatalf("startup continued despite an endpoint with unknown ownership: %+v", result)
	}
	loaded, err := loadOrCreateConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SharedAppServerEnabled {
		t.Fatal("fail-open did not persist shared mode disabled before stopping")
	}
	value, stillPresent, err := readUserEnvironment(sharedAppServerEnvironmentName)
	if err != nil || !stillPresent || value != unknownEndpoint {
		t.Fatalf("unknown endpoint was overwritten instead of preserved: value=%q present=%v err=%v", value, stillPresent, err)
	}
}

func TestStartupAllowsFirstEnablementWhenStateAndEndpointAreMissing(t *testing.T) {
	dataDir := t.TempDir()
	config := defaultConfig()
	config.SharedAppServerEnabled = true
	configPath := filepath.Join(dataDir, "config.json")
	if err := writeJSONAtomic(configPath, config); err != nil {
		t.Fatal(err)
	}

	result := prepareSharedBackendAtStartup(context.Background(), configPath, dataDir, config)
	if result.Reason != "" || !result.CanContinue || !result.Config.SharedAppServerEnabled {
		t.Fatalf("first enablement without stale endpoint evidence was not allowed: %+v", result)
	}
	loaded, err := loadOrCreateConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.SharedAppServerEnabled {
		t.Fatal("first enablement unexpectedly changed the shared-mode preference")
	}
}

func TestStartupSharedServerStateKeepsHealthyOwnedBackendForAdoption(t *testing.T) {
	previousRunning := startupSharedServerProcessIsRunning
	previousOwnership := startupSharedServerOwnershipProbe
	previousProbe := startupSharedServerEndpointProbe
	startupSharedServerProcessIsRunning = func(pid int) bool { return pid == 42 }
	startupSharedServerOwnershipProbe = func(_ context.Context, _ *sharedServerManager, state sharedServerState) bool { return state.PID == 42 }
	startupSharedServerEndpointProbe = func(_ context.Context, endpoint string) error {
		if endpoint == "" {
			return errors.New("missing endpoint")
		}
		return nil
	}
	t.Cleanup(func() {
		startupSharedServerProcessIsRunning = previousRunning
		startupSharedServerOwnershipProbe = previousOwnership
		startupSharedServerEndpointProbe = previousProbe
	})

	dataDir := t.TempDir()
	config := defaultConfig()
	config.SharedAppServerEnabled = true
	manager := newSharedServerManager(config, dataDir, nil)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(dataDir, "shared-server.json")
	state := sharedServerState{
		PID:            42,
		Endpoint:       manager.Endpoint(),
		CodexHome:      manager.codexHome,
		Executable:     executable,
		ExecutableHash: executableHash(executable),
		Owner:          sharedServerOwner,
		Version:        appVersion,
	}
	if err := writeJSONAtomic(statePath, state); err != nil {
		t.Fatal(err)
	}
	if err := manager.checkSharedServerStartupState(context.Background()); err != nil {
		t.Fatalf("healthy owned backend was rejected before adoption: %v", err)
	}
	var retained sharedServerState
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &retained); err != nil {
		t.Fatal(err)
	}
	if retained.PID != state.PID || retained.Endpoint != state.Endpoint {
		t.Fatalf("healthy owned state changed during startup check: %+v", retained)
	}
}

func TestStartupSharedServerStateFailsOpenWhenOwnedEndpointStopsListening(t *testing.T) {
	previousRunning := startupSharedServerProcessIsRunning
	previousOwnership := startupSharedServerOwnershipProbe
	previousProbe := startupSharedServerEndpointProbe
	startupSharedServerProcessIsRunning = func(pid int) bool { return pid == 42 }
	startupSharedServerOwnershipProbe = func(_ context.Context, _ *sharedServerManager, state sharedServerState) bool { return state.PID == 42 }
	startupSharedServerEndpointProbe = func(context.Context, string) error {
		return errors.New("endpoint is not listening")
	}
	t.Cleanup(func() {
		startupSharedServerProcessIsRunning = previousRunning
		startupSharedServerOwnershipProbe = previousOwnership
		startupSharedServerEndpointProbe = previousProbe
	})

	dataDir := t.TempDir()
	config := defaultConfig()
	config.SharedAppServerEnabled = true
	manager := newSharedServerManager(config, dataDir, nil)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(filepath.Join(dataDir, "shared-server.json"), sharedServerState{
		PID:            42,
		Endpoint:       manager.Endpoint(),
		CodexHome:      manager.codexHome,
		Executable:     executable,
		ExecutableHash: executableHash(executable),
		Owner:          sharedServerOwner,
		Version:        appVersion,
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.checkSharedServerStartupState(context.Background()); !errors.Is(err, errSharedServerUnavailable) {
		t.Fatalf("unresponsive owned endpoint was not rejected before Ensure: %v", err)
	}
}

func TestStartupFailOpenStopsBeforeRecoveryWhenConfigCannotBePersisted(t *testing.T) {
	previousConfigWriter := writeSharedFailOpenConfig
	writeSharedFailOpenConfig = func(string, any) error { return errors.New("config is locked") }
	t.Cleanup(func() { writeSharedFailOpenConfig = previousConfigWriter })

	dataDir := t.TempDir()
	config := defaultConfig()
	config.SharedAppServerEnabled = true
	configPath := filepath.Join(dataDir, "config.json")
	if err := writeJSONAtomic(configPath, config); err != nil {
		t.Fatal(err)
	}
	result := completeStartupFailOpen(context.Background(), configPath, dataDir, config)
	if result.CanContinue {
		t.Fatal("startup continued after the disabled preference could not be persisted")
	}
	loaded, err := loadOrCreateConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.SharedAppServerEnabled {
		t.Fatal("test did not preserve the enabled setting after the simulated write failure")
	}
	if _, err := os.Stat(filepath.Join(dataDir, sharedFailOpenMarkerName)); err != nil {
		t.Fatalf("incomplete fail-open did not retain its recovery marker: %v", err)
	}
}

func TestStartupFailOpenContinuesWithOfficialBackendWhenMarkerWriteFails(t *testing.T) {
	previousMarkerWriter := writeSharedFailOpenMarker
	writeSharedFailOpenMarker = func(string, any) error { return errors.New("marker is locked") }
	t.Cleanup(func() { writeSharedFailOpenMarker = previousMarkerWriter })

	dataDir := t.TempDir()
	config := defaultConfig()
	config.SharedAppServerEnabled = true
	configPath := filepath.Join(dataDir, "config.json")
	if err := writeJSONAtomic(configPath, config); err != nil {
		t.Fatal(err)
	}
	result := completeStartupFailOpen(context.Background(), configPath, dataDir, config)
	if !result.CanContinue || result.CleanupErr == nil {
		t.Fatalf("marker failure did not preserve official-backend startup: %+v", result)
	}
	loaded, err := loadOrCreateConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SharedAppServerEnabled {
		t.Fatal("marker failure prevented the disabled preference from being persisted")
	}
}

func TestStartupFailOpenContinuesAndDefersCleanupWhenEnvironmentRestoreFails(t *testing.T) {
	previousRestore := restoreSharedEnvironmentForFailOpen
	restoreSharedEnvironmentForFailOpen = func(string, ...string) (sharedEnvironmentResult, error) {
		return sharedEnvironmentResult{}, errors.New("environment restore failed")
	}
	t.Cleanup(func() { restoreSharedEnvironmentForFailOpen = previousRestore })

	dataDir := t.TempDir()
	config := defaultConfig()
	config.SharedAppServerEnabled = true
	configPath := filepath.Join(dataDir, "config.json")
	if err := writeJSONAtomic(configPath, config); err != nil {
		t.Fatal(err)
	}
	result := completeStartupFailOpen(context.Background(), configPath, dataDir, config)
	if !result.CanContinue || result.CleanupErr == nil {
		t.Fatalf("environment cleanup failure did not become deferred cleanup: %+v", result)
	}
	loaded, err := loadOrCreateConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SharedAppServerEnabled {
		t.Fatal("environment cleanup failure prevented the disabled preference from being persisted")
	}
}
