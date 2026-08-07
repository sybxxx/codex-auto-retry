//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	sharedAppServerEnvironmentName = "CODEX_APP_SERVER_WS_URL"
	sharedServerOwner              = "codex-auto-retry"
)

type sharedEnvironmentBackup struct {
	SchemaVersion   int       `json:"schema_version"`
	Name            string    `json:"name"`
	PreviousPresent bool      `json:"previous_present"`
	PreviousValue   string    `json:"previous_value"`
	InstalledValue  string    `json:"installed_value"`
	RecordedAt      time.Time `json:"recorded_at"`
}

type sharedEnvironmentResult struct {
	Changed       bool
	Restored      bool
	ChangedByUser bool
}

func enableSharedAppServer(ctx context.Context, dataDir string, config Config) error {
	manager := newSharedServerManager(config, dataDir, nil)
	if err := manager.Ensure(ctx); err != nil {
		return fmt.Errorf("shared app-server health check failed: %w", err)
	}
	if err := manager.ValidateOwned(ctx); err != nil {
		return fmt.Errorf("shared app-server ownership check failed: %w", err)
	}
	endpoint := manager.Endpoint()
	if !validSharedServerEndpoint(endpoint, config.SharedAppServerPort) {
		return errors.New("shared app-server endpoint is not the expected loopback address")
	}
	if _, err := setOwnedSharedEnvironment(dataDir, endpoint); err != nil {
		_ = manager.StopOwned(context.Background())
		return err
	}
	return nil
}

func disableSharedAppServer(ctx context.Context, dataDir string, config Config) error {
	if _, err := restoreOwnedSharedEnvironment(dataDir); err != nil {
		return err
	}
	manager := newSharedServerManager(config, dataDir, nil)
	return manager.StopOwned(ctx)
}

func (m *sharedServerManager) ValidateOwned(ctx context.Context) error {
	state, err := m.readState()
	if err != nil {
		return err
	}
	if state.Owner != sharedServerOwner || state.Version != appVersion {
		return errors.New("shared app-server state is not owned by this plugin version")
	}
	if !validSharedServerEndpoint(state.Endpoint, m.config.SharedAppServerPort) || !m.SupportsHome(state.CodexHome) {
		return errors.New("shared app-server state failed endpoint or home validation")
	}
	if info, err := os.Stat(state.Executable); err != nil || info.IsDir() {
		return errors.New("shared app-server executable is missing")
	}
	if state.ExecutableHash == "" || state.ExecutableHash != executableHash(state.Executable) {
		return errors.New("shared app-server executable hash does not match owned state")
	}
	if !m.ownsProcess(ctx, state) {
		return errors.New("shared app-server process is not owned by the plugin")
	}
	if err := m.probe(ctx); err != nil {
		return err
	}
	return nil
}

func (m *sharedServerManager) StopOwned(ctx context.Context) error {
	state, err := m.readState()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return nil
	}
	if state.Owner != sharedServerOwner || state.Version != appVersion ||
		!validSharedServerEndpoint(state.Endpoint, m.config.SharedAppServerPort) ||
		!m.SupportsHome(state.CodexHome) || state.ExecutableHash == "" ||
		state.ExecutableHash != executableHash(state.Executable) || !m.ownsProcess(ctx, state) {
		return nil
	}
	if processIsRunning(state.PID) {
		if err := terminateProcessTree(ctx, state.PID); err != nil {
			return err
		}
	}
	_ = os.Remove(filepath.Join(m.dataDir, "shared-server.json"))
	return nil
}

func setOwnedSharedEnvironment(dataDir, desired string) (sharedEnvironmentResult, error) {
	if !validSharedServerEndpoint(desired, endpointPort(desired)) {
		return sharedEnvironmentResult{}, errors.New("refusing an invalid shared app-server endpoint")
	}
	backupPath := filepath.Join(dataDir, "environment-backup.json")
	current, present, err := readUserEnvironment(sharedAppServerEnvironmentName)
	if err != nil {
		return sharedEnvironmentResult{}, err
	}
	backupExisted := false
	var backupBytes []byte
	var backup sharedEnvironmentBackup
	if data, readErr := os.ReadFile(backupPath); readErr == nil {
		backupExisted = true
		backupBytes = data
		if err := json.Unmarshal(data, &backup); err != nil || backup.SchemaVersion != 1 || backup.Name != sharedAppServerEnvironmentName {
			return sharedEnvironmentResult{}, errors.New("shared environment ownership record is invalid")
		}
		expected := backup.PreviousValue
		if !backup.PreviousPresent {
			expected = ""
		}
		if present && current != backup.InstalledValue && current != expected && current != desired {
			return sharedEnvironmentResult{}, errors.New("CODEX_APP_SERVER_WS_URL already has a different user value")
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return sharedEnvironmentResult{}, readErr
	} else {
		if present && current != desired {
			return sharedEnvironmentResult{}, errors.New("CODEX_APP_SERVER_WS_URL already has a different user value")
		}
		backup = sharedEnvironmentBackup{
			SchemaVersion:   1,
			Name:            sharedAppServerEnvironmentName,
			PreviousPresent: present,
			PreviousValue:   current,
		}
	}
	backup.InstalledValue = desired
	backup.RecordedAt = time.Now().UTC()
	if err := writeUserEnvironment(sharedAppServerEnvironmentName, desired); err != nil {
		return sharedEnvironmentResult{}, err
	}
	if err := writeJSONAtomic(backupPath, backup); err != nil {
		_ = restoreUserEnvironment(sharedAppServerEnvironmentName, current, present)
		if backupExisted {
			_ = os.WriteFile(backupPath, backupBytes, 0o600)
		} else {
			_ = os.Remove(backupPath)
		}
		return sharedEnvironmentResult{}, err
	}
	broadcastEnvironmentChange()
	return sharedEnvironmentResult{Changed: !present || current != desired}, nil
}

func restoreOwnedSharedEnvironment(dataDir string) (sharedEnvironmentResult, error) {
	backupPath := filepath.Join(dataDir, "environment-backup.json")
	data, err := os.ReadFile(backupPath)
	if errors.Is(err, os.ErrNotExist) {
		return sharedEnvironmentResult{}, nil
	}
	if err != nil {
		return sharedEnvironmentResult{}, err
	}
	var backup sharedEnvironmentBackup
	if err := json.Unmarshal(data, &backup); err != nil || backup.SchemaVersion != 1 || backup.Name != sharedAppServerEnvironmentName {
		return sharedEnvironmentResult{}, errors.New("shared environment ownership record is invalid")
	}
	current, present, err := readUserEnvironment(sharedAppServerEnvironmentName)
	if err != nil {
		return sharedEnvironmentResult{}, err
	}
	previous := backup.PreviousValue
	if !backup.PreviousPresent {
		previous = ""
	}
	changedByUser := present && current != backup.InstalledValue && current != previous
	if !changedByUser && (!present || current != previous) {
		if err := restoreUserEnvironment(sharedAppServerEnvironmentName, previous, backup.PreviousPresent); err != nil {
			return sharedEnvironmentResult{}, err
		}
		broadcastEnvironmentChange()
	}
	_ = os.Remove(backupPath)
	return sharedEnvironmentResult{Restored: !changedByUser, ChangedByUser: changedByUser}, nil
}

func readUserEnvironment(name string) (string, bool, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer key.Close()
	value, _, err := key.GetStringValue(name)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

func writeUserEnvironment(name, value string) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, `Environment`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	return key.SetStringValue(name, value)
}

func restoreUserEnvironment(name, value string, present bool) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		if !present {
			return nil
		}
		key, _, err = registry.CreateKey(registry.CURRENT_USER, `Environment`, registry.SET_VALUE)
	}
	if err != nil {
		return err
	}
	defer key.Close()
	if present {
		return key.SetStringValue(name, value)
	}
	if err := key.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

func broadcastEnvironmentChange() {
	user32 := windows.NewLazySystemDLL("user32.dll")
	proc := user32.NewProc("SendMessageTimeoutW")
	message, err := windows.UTF16PtrFromString("Environment")
	if err != nil {
		return
	}
	var result uintptr
	_, _, _ = proc.Call(
		uintptr(0xffff), 0x001a, 0,
		uintptr(unsafe.Pointer(message)), 0x0002, 5000,
		uintptr(unsafe.Pointer(&result)),
	)
}

func endpointPort(endpoint string) int {
	if parsed, err := url.Parse(endpoint); err == nil {
		if port, err := strconv.Atoi(parsed.Port()); err == nil {
			return port
		}
	}
	return 0
}
