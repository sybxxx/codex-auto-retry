//go:build windows

package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	sharedServerLaunchMode = "hidden_inherited_console_v1"
)

var (
	errSharedServerUnavailable       = errors.New("shared Codex app-server is unavailable")
	errSharedServerPortConflict      = errors.New("shared Codex app-server port is occupied")
	errSharedServerPortReserved      = errors.New("shared Codex app-server port is reserved by Windows")
	errSharedServerMigrationDeferred = errors.New("shared Codex app-server migration is waiting for Codex to close")
)

type sharedServerState struct {
	PID            int       `json:"pid"`
	Endpoint       string    `json:"endpoint"`
	CodexHome      string    `json:"codex_home"`
	Executable     string    `json:"executable"`
	ExecutableHash string    `json:"executable_hash"`
	MCPConfigHash  string    `json:"mcp_config_hash,omitempty"`
	Owner          string    `json:"owner"`
	Version        string    `json:"version"`
	StartedAt      time.Time `json:"started_at"`
	LaunchMode     string    `json:"launch_mode,omitempty"`
}

type sharedServerManager struct {
	config        Config
	dataDir       string
	logger        *safeLogger
	endpoint      string
	codexHome     string
	mu            sync.Mutex
	migrationOnce sync.Once
}

// A running server survives a watchdog upgrade. Its plugin ownership is
// established by the durable owner marker, loopback endpoint, Codex home,
// executable hash, and live command line, rather than by the plugin version.
// This lets the new watchdog adopt its own old server instead of reporting a
// false port conflict during login.
func (m *sharedServerManager) sharedServerStateOwnedByPlugin(state sharedServerState) bool {
	return state.Owner == sharedServerOwner && strings.TrimSpace(state.Version) != "" &&
		validSharedServerEndpoint(state.Endpoint, m.config.SharedAppServerPort) &&
		m.SupportsHome(state.CodexHome) && state.Executable != "" &&
		state.ExecutableHash != "" && state.ExecutableHash == executableHash(state.Executable)
}

func (m *sharedServerManager) adoptSharedServerState(state *sharedServerState) error {
	if state == nil || state.Version == appVersion {
		return nil
	}
	state.Version = appVersion
	return writeJSONAtomic(filepath.Join(m.dataDir, "shared-server.json"), state)
}

func newSharedServerManager(config Config, dataDir string, logger *safeLogger) *sharedServerManager {
	home, _ := os.UserHomeDir()
	codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	return &sharedServerManager{
		config:    config,
		dataDir:   dataDir,
		logger:    logger,
		endpoint:  fmt.Sprintf("ws://127.0.0.1:%d", config.SharedAppServerPort),
		codexHome: expandPath(codexHome),
	}
}

func (m *sharedServerManager) Endpoint() string {
	return m.endpoint
}

func (m *sharedServerManager) SupportsHome(value string) bool {
	return strings.EqualFold(filepath.Clean(value), m.codexHome)
}

func (m *sharedServerManager) Ensure(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, desiredMCPConfigHash, err := loadCodexAppMCPOverride()
	if err != nil {
		return fmt.Errorf("%w: codex_app MCP configuration: %v", errSharedAppServerConfigInvalid, err)
	}
	if m.probe(ctx) == nil {
		state, err := m.readState()
		if err == nil && m.sharedServerStateOwnedByPlugin(state) && m.ownsProcess(ctx, state) {
			if err := m.adoptSharedServerState(&state); err != nil {
				return fmt.Errorf("adopt shared app-server state: %w", err)
			}
			if m.sharedServerNeedsMigration(state, desiredMCPConfigHash) {
				m.scheduleLaunchMigration()
			}
			return nil
		}
		return errSharedServerPortConflict
	}
	address := fmt.Sprintf("127.0.0.1:%d", m.config.SharedAppServerPort)
	connection, err := net.DialTimeout("tcp", address, 300*time.Millisecond)
	if err == nil {
		_ = connection.Close()
		state, stateErr := m.readState()
		if stateErr == nil && m.sharedServerStateOwnedByPlugin(state) && m.ownsProcess(ctx, state) {
			if err := m.adoptSharedServerState(&state); err != nil {
				return fmt.Errorf("adopt shared app-server state: %w", err)
			}
			// The listener may exist a little before the WebSocket handshake is
			// ready. Give our own process a bounded chance to become healthy;
			// if it does not, restart only that verified owned process.
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
				probeErr := m.probe(probeCtx)
				cancel()
				if probeErr == nil {
					if m.sharedServerNeedsMigration(state, desiredMCPConfigHash) {
						m.scheduleLaunchMigration()
					}
					return nil
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(100 * time.Millisecond):
				}
			}
			if stopErr := m.StopOwned(ctx); stopErr != nil {
				return stopErr
			}
		} else {
			return errSharedServerPortConflict
		}
	}
	if err := checkSharedServerPort(m.config.SharedAppServerPort); err != nil {
		return err
	}
	executable, err := findCodexExecutable()
	if err != nil {
		return fmt.Errorf("%w: %v", errSharedServerUnavailable, err)
	}
	if err := m.start(executable); err != nil {
		return fmt.Errorf("%w: %v", errSharedServerUnavailable, err)
	}
	started := true
	cleanup := func(err error) error {
		if started {
			cleanupSharedServer(m)
		}
		return err
	}
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := m.probe(probeCtx)
		cancel()
		if err == nil {
			state, stateErr := m.readState()
			if stateErr == nil && m.sharedServerStateOwnedByPlugin(state) && m.ownsProcess(ctx, state) {
				if err := m.adoptSharedServerState(&state); err != nil {
					return cleanup(fmt.Errorf("adopt shared app-server state: %w", err))
				}
				return nil
			}
			return cleanup(errSharedServerUnavailable)
		}
		select {
		case <-ctx.Done():
			return cleanup(ctx.Err())
		case <-time.After(150 * time.Millisecond):
		}
	}
	return cleanup(errSharedServerUnavailable)
}

func checkSharedServerPort(port int) error {
	address := fmt.Sprintf("127.0.0.1:%d", port)
	listener, err := net.Listen("tcp4", address)
	if err == nil {
		return listener.Close()
	}
	if isWindowsPortReservedError(err) {
		return fmt.Errorf("%w: port=%d", errSharedServerPortReserved, port)
	}
	if isWindowsPortOccupiedError(err) {
		return fmt.Errorf("%w: port=%d", errSharedServerPortConflict, port)
	}
	return fmt.Errorf("%w: %v", errSharedServerUnavailable, err)
}

func isWindowsPortReservedError(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) && errno == syscall.Errno(10013) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "access permissions") || strings.Contains(message, "access is denied")
}

func isWindowsPortOccupiedError(err error) bool {
	var errno syscall.Errno
	return errors.As(err, &errno) && errno == syscall.Errno(10048)
}

func (m *sharedServerManager) scheduleLaunchMigration() {
	m.migrationOnce.Do(func() {
		if m.logger != nil {
			m.logger.Printf("shared app-server console migration scheduled")
		}
		go m.waitForDesktopExitAndMigrate()
	})
}

func (m *sharedServerManager) waitForDesktopExitAndMigrate() {
	for {
		if codexDesktopRunning() {
			time.Sleep(250 * time.Millisecond)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		err := m.migrateLegacyLaunch(ctx)
		cancel()
		if err == nil {
			if m.logger != nil {
				m.logger.Printf("shared app-server console migration completed")
			}
			return
		}
		if !errors.Is(err, errSharedServerMigrationDeferred) && m.logger != nil {
			m.logger.Printf("shared app-server console migration delayed category=server_migration")
		}
		time.Sleep(2 * time.Second)
	}
}

func (m *sharedServerManager) migrateLegacyLaunch(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, err := m.readState()
	if err != nil {
		return err
	}
	_, desiredMCPConfigHash, err := loadCodexAppMCPOverride()
	if err != nil {
		return fmt.Errorf("%w: %v", errSharedAppServerConfigInvalid, err)
	}
	if !m.sharedServerNeedsMigration(state, desiredMCPConfigHash) {
		return nil
	}
	if codexDesktopRunning() {
		return errSharedServerMigrationDeferred
	}
	if m.probe(ctx) == nil {
		if !validSharedServerEndpoint(state.Endpoint, m.config.SharedAppServerPort) ||
			!m.SupportsHome(state.CodexHome) || !m.ownsProcess(ctx, state) {
			return errSharedServerPortConflict
		}
		stopCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		err = terminateProcessTree(stopCtx, state.PID)
		cancel()
		if err != nil {
			return err
		}
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		connection, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", m.config.SharedAppServerPort), 200*time.Millisecond)
		if dialErr != nil {
			break
		}
		_ = connection.Close()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	connection, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", m.config.SharedAppServerPort), 200*time.Millisecond)
	if dialErr == nil {
		_ = connection.Close()
		return errSharedServerPortConflict
	}
	executable := state.Executable
	if info, statErr := os.Stat(executable); statErr != nil || info.IsDir() {
		executable, err = findCodexExecutable()
		if err != nil {
			return err
		}
	}
	hash := executableHash(executable)
	if hash == "" {
		return errors.New("Codex executable hash could not be verified")
	}
	return m.start(executable)
}

func (m *sharedServerManager) readState() (sharedServerState, error) {
	data, err := os.ReadFile(filepath.Join(m.dataDir, "shared-server.json"))
	if err != nil {
		return sharedServerState{}, err
	}
	var state sharedServerState
	if err := json.Unmarshal(data, &state); err != nil {
		return sharedServerState{}, err
	}
	if state.PID <= 0 || state.Executable == "" || state.Endpoint == "" {
		return sharedServerState{}, errors.New("invalid shared app-server state")
	}
	return state, nil
}

func (m *sharedServerManager) ownsProcess(ctx context.Context, state sharedServerState) bool {
	powerShell, err := resolvePowerShellExecutable(m.config.PowerShellExecutable)
	if err != nil {
		return false
	}
	commandCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	command := exec.CommandContext(commandCtx, powerShell,
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", "-",
	)
	command.Stdin = strings.NewReader(powerShellScriptInput(fmt.Sprintf(`
$process = Get-CimInstance Win32_Process -Filter "ProcessId = %d" -ErrorAction Stop
if ($process) {
  [Console]::Out.Write(($process | Select-Object ExecutablePath,CommandLine | ConvertTo-Json -Compress))
}
`, state.PID)))
	command.Stderr = nil
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	data, err := command.Output()
	if err != nil {
		return false
	}
	var process struct {
		ExecutablePath string `json:"ExecutablePath"`
		CommandLine    string `json:"CommandLine"`
	}
	if err := json.Unmarshal(data, &process); err != nil {
		return false
	}
	return strings.EqualFold(filepath.Clean(process.ExecutablePath), filepath.Clean(state.Executable)) &&
		strings.Contains(strings.ToLower(process.CommandLine), "app-server") &&
		strings.Contains(strings.ToLower(process.CommandLine), strings.ToLower(state.Endpoint))
}

func (m *sharedServerManager) probe(ctx context.Context) error {
	client, err := dialAppServerRPC(ctx, m.endpoint)
	if err != nil {
		return err
	}
	return client.Close()
}

func (m *sharedServerManager) start(executable string) error {
	if info, err := os.Stat(executable); err != nil || info.IsDir() {
		return errors.New("Codex executable is missing")
	}
	hash := executableHash(executable)
	if hash == "" {
		return errors.New("Codex executable hash could not be verified")
	}
	mcpOverride, mcpConfigHash, err := loadCodexAppMCPOverride()
	if err != nil {
		return fmt.Errorf("%w: %v", errSharedAppServerConfigInvalid, err)
	}
	arguments := sharedServerStartArguments(m.endpoint, mcpOverride)
	command := exec.Command(executable, arguments...)
	command.Env = replaceEnvironmentValue(os.Environ(), "CODEX_HOME", m.codexHome)
	command.Stdin = nil
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devNull.Close()
	command.Stdout = devNull
	command.Stderr = devNull
	command.SysProcAttr = hiddenInheritedConsoleAttributes()
	if err := command.Start(); err != nil {
		return err
	}
	pid := command.Process.Pid
	if err := command.Process.Release(); err != nil {
		return err
	}
	state := sharedServerState{
		PID: pid, Endpoint: m.endpoint, CodexHome: m.codexHome,
		Executable: executable, Owner: sharedServerOwner, Version: appVersion,
		ExecutableHash: hash, MCPConfigHash: mcpConfigHash,
		StartedAt: time.Now().UTC(), LaunchMode: sharedServerLaunchMode,
	}
	if err := writeJSONAtomic(filepath.Join(m.dataDir, "shared-server.json"), state); err != nil {
		_ = terminateProcessTree(context.Background(), pid)
		return err
	}
	if m.logger != nil {
		m.logger.Printf("shared app-server starting pid=%d port=%d", pid, m.config.SharedAppServerPort)
	}
	return nil
}

// sharedServerStartArguments keeps the app-server command line in one place so
// its ordering can be tested without launching a second Codex process. The
// Desktop app uses the same global -c override after the app-server command.
func sharedServerStartArguments(endpoint, mcpOverride string) []string {
	arguments := []string{
		"-c", "features.code_mode_host=true",
		"app-server", "--analytics-default-enabled",
	}
	if strings.TrimSpace(mcpOverride) != "" {
		arguments = append(arguments, "-c", mcpOverride)
	}
	return append(arguments, "--listen", endpoint)
}

func (m *sharedServerManager) sharedServerNeedsMigration(state sharedServerState, desiredMCPConfigHash string) bool {
	return state.LaunchMode != sharedServerLaunchMode || state.MCPConfigHash != desiredMCPConfigHash
}

func executableHash(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return ""
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func findCodexExecutable() (string, error) {
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData != "" {
		root := filepath.Join(localAppData, "OpenAI", "Codex", "bin")
		var candidates []string
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err == nil && !entry.IsDir() && strings.EqualFold(entry.Name(), "codex.exe") {
				candidates = append(candidates, path)
			}
			return nil
		})
		sort.Slice(candidates, func(i, j int) bool {
			left, _ := os.Stat(candidates[i])
			right, _ := os.Stat(candidates[j])
			if left == nil || right == nil {
				return candidates[i] > candidates[j]
			}
			return left.ModTime().After(right.ModTime())
		})
		if len(candidates) > 0 {
			return candidates[0], nil
		}
	}
	if appData := os.Getenv("APPDATA"); appData != "" {
		root := filepath.Join(appData, "npm", "node_modules", "@openai", "codex")
		var candidate string
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if candidate == "" && err == nil && !entry.IsDir() && strings.EqualFold(entry.Name(), "codex.exe") {
				candidate = path
			}
			return nil
		})
		if candidate != "" {
			return candidate, nil
		}
	}
	return "", errors.New("Codex CLI executable was not found")
}

func replaceEnvironmentValue(environment []string, key, value string) []string {
	prefix := strings.ToLower(key) + "="
	result := make([]string, 0, len(environment)+1)
	for _, item := range environment {
		if strings.HasPrefix(strings.ToLower(item), prefix) {
			continue
		}
		result = append(result, item)
	}
	return append(result, key+"="+value)
}

func validSharedServerEndpoint(raw string, expectedPort int) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "ws" || parsed.User != nil || parsed.Path != "" {
		return false
	}
	return parsed.Hostname() == "127.0.0.1" && parsed.Port() == fmt.Sprint(expectedPort)
}
