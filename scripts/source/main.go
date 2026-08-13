package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

type localSettingsPayload struct {
	RetrySettings
	Paused                 bool  `json:"paused"`
	SharedAppServerEnabled *bool `json:"shared_app_server_enabled,omitempty"`
}

const (
	localSettingsExitFailure      = 1
	localSettingsExitPortReserved = 2
	localSettingsExitPortConflict = 3
)

func main() {
	arguments := os.Args[1:]
	mode := "run"
	if len(arguments) > 0 && (arguments[0] == "run" || arguments[0] == "supervise" || arguments[0] == "mcp" || arguments[0] == "save-settings" || arguments[0] == "control") {
		mode = arguments[0]
		arguments = arguments[1:]
	}
	flags := flag.NewFlagSet("codex-auto-retry", flag.ContinueOnError)
	dataDirFlag := flags.String("data-dir", "", "runtime data directory")
	settingsFileFlag := flags.String("settings-file", "", "settings payload path")
	actionFlag := flags.String("action", "", "retry control action")
	threadIDFlag := flags.String("thread-id", "", "Codex task identifier")
	noTrayFlag := flags.Bool("no-tray", false, "disable the Windows notification-area icon")
	supervisedFlag := flags.Bool("supervised", false, "run as a worker owned by the supervisor")
	_ = flags.Parse(arguments)

	dataDir := *dataDirFlag
	if dataDir == "" {
		dataDir = os.Getenv("CODEX_AUTO_RETRY_DATA_DIR")
	}
	if dataDir == "" {
		executable, err := os.Executable()
		if err != nil {
			return
		}
		dataDir = filepath.Dir(executable)
	}
	dataDir = expandPath(dataDir)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return
	}
	if mode == "save-settings" {
		if err := saveLocalSettings(dataDir, *settingsFileFlag); err != nil {
			os.Exit(localSettingsExitCode(err))
		}
		return
	}
	if mode == "control" {
		if err := runLocalControl(dataDir, *actionFlag, *threadIDFlag); err != nil {
			os.Exit(1)
		}
		return
	}
	if mode == "mcp" {
		if err := runManagementMCP(dataDir); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "Codex Auto Retry MCP server stopped:", err)
		}
		return
	}
	if mode == "supervise" {
		if err := runSupervisor(dataDir, *noTrayFlag); err != nil {
			os.Exit(1)
		}
		return
	}

	lock, err := acquireInstanceLock(filepath.Join(dataDir, "daemon.lock"))
	if err != nil {
		return
	}
	defer lock.Close()

	logger, err := newSafeLogger(filepath.Join(dataDir, "logs", "daemon.log"))
	if err != nil {
		return
	}
	defer logger.Close()
	logger.Printf("watchdog starting version=%s", appVersion)

	configPath := filepath.Join(dataDir, "config.json")
	config, err := loadOrCreateConfig(configPath)
	if err != nil {
		logger.Printf("startup failed category=config")
		return
	}
	if !config.SharedAppServerEnabled {
		// Fail-open startup also cleans an ownership record left by an older
		// release, but never guesses at an unrecorded user endpoint here.
		if _, cleanupErr := restoreOwnedSharedEnvironment(dataDir); cleanupErr != nil {
			logger.Printf("shared app-server cleanup failed category=environment")
		}
		manager := newSharedServerManager(config, dataDir, logger)
		if cleanupErr := manager.StopOwned(context.Background()); cleanupErr != nil {
			logger.Printf("shared app-server cleanup failed category=process")
		}
	}
	runner := newAppResumeRunner(config, dataDir, logger)
	var prepareErr error
	startupFailOpenReason := ""
	if config.SharedAppServerEnabled || len(discoverSessionRoots(config)) > 0 {
		prepareCtx, prepareCancel := context.WithTimeout(context.Background(), 15*time.Second)
		prepareErr = runner.Prepare(prepareCtx)
		prepareCancel()
	}
	prepareReason := controllerFailureReason(DispatchResult{}, prepareErr)
	if config.SharedAppServerEnabled && prepareErr != nil && controllerFailureNeedsFailOpen(prepareReason) {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 3*time.Second)
		if cleanupErr := disableSharedAppServer(cleanupCtx, dataDir, config); cleanupErr != nil {
			logger.Printf("shared app-server fail-open cleanup failed category=%s", prepareReason)
		}
		cleanupCancel()
		startupFailOpenReason = prepareReason
		config.SharedAppServerEnabled = false
		if writeErr := config.validate(); writeErr == nil {
			if writeErr = writeJSONAtomic(configPath, config); writeErr != nil {
				logger.Printf("shared app-server fail-open setting was not persisted category=config")
			}
		} else {
			logger.Printf("shared app-server fail-open setting was invalid category=config")
		}
		logger.Printf("shared app-server disabled after startup failure category=%s", prepareReason)
	}
	daemon, err := newDaemon(config, dataDir, logger, runner)
	if err != nil {
		logger.Printf("startup failed category=state")
		return
	}
	if !config.SharedAppServerEnabled {
		if startupFailOpenReason != "" {
			daemon.controllerState = startupFailOpenReason
			daemon.lastError = startupFailOpenReason
			logger.Printf("shared app-server disabled; Codex remains on its official backend category=%s", startupFailOpenReason)
		} else {
			daemon.controllerState = "shared_app_server_disabled"
			logger.Printf("shared app-server disabled; Codex remains on its official backend")
		}
	} else if prepareErr != nil {
		daemon.lastError = prepareReason
		daemon.controllerState = daemon.lastError
		logger.Printf("controller preparation failed category=%s", daemon.lastError)
	} else {
		daemon.controllerState = "ready"
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	trayDone := make(chan struct{})
	if *noTrayFlag {
		close(trayDone)
	} else {
		go func() {
			defer close(trayDone)
			if trayErr := runTray(ctx, cancel, dataDir, logger); trayErr != nil {
				logger.Printf("tray stopped category=tray_error")
			}
		}()
	}
	_ = daemon.writeStatus(true)
	err = daemon.run(ctx)
	cancel()
	daemon.waitForJobs()
	select {
	case <-trayDone:
	case <-time.After(3 * time.Second):
	}
	if err != nil && err != errStopRequested {
		logger.Printf("watchdog stopped category=runtime_error")
	}
	_ = daemon.writeStatus(false)
	if *supervisedFlag && (err == nil || errors.Is(err, errStopRequested)) {
		// A clean worker shutdown is intentional. Tell the supervisor not to
		// bring it back; crashes and startup errors remain restartable.
		_ = os.WriteFile(filepath.Join(dataDir, "supervisor.stop"), []byte("stop\n"), 0o600)
	}
	logger.Printf("watchdog stopped")
}

func localSettingsExitCode(err error) int {
	switch {
	case errors.Is(err, errSharedServerPortReserved):
		return localSettingsExitPortReserved
	case errors.Is(err, errSharedServerPortConflict):
		return localSettingsExitPortConflict
	default:
		return localSettingsExitFailure
	}
}

func saveLocalSettings(dataDir, settingsPath string) error {
	if settingsPath == "" {
		settingsPath = os.Getenv("CODEX_AUTO_RETRY_SETTINGS_FILE")
	}
	if settingsPath == "" {
		return fmt.Errorf("settings file is required")
	}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return err
	}
	var payload localSettingsPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}
	service := newManagementService(dataDir)
	now := time.Now().UTC()
	if payload.SharedAppServerEnabled != nil {
		if _, err := service.setSharedAppServerEnabled(*payload.SharedAppServerEnabled, now); err != nil {
			return err
		}
	}
	return service.setLocalSettings(payload.RetrySettings, payload.Paused, now)
}

func runLocalControl(dataDir, action, threadID string) error {
	if action == "" {
		action = os.Getenv("CODEX_AUTO_RETRY_ACTION")
	}
	if threadID == "" {
		threadID = os.Getenv("CODEX_AUTO_RETRY_THREAD_ID")
	}
	service := newManagementService(dataDir)
	now := time.Now().UTC()
	switch action {
	case string(commandRetryNow):
		_, err := service.retryNow(threadID, now)
		return err
	case string(commandCancelRetry):
		_, err := service.cancelRetry(threadID, now)
		return err
	case string(commandRestartRetry):
		_, err := service.restartRetry(threadID, now)
		return err
	default:
		return fmt.Errorf("unsupported control action")
	}
}
