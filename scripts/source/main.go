package main

import (
	"context"
	"encoding/json"
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
	Paused bool `json:"paused"`
}

func main() {
	arguments := os.Args[1:]
	mode := "run"
	if len(arguments) > 0 && (arguments[0] == "run" || arguments[0] == "mcp" || arguments[0] == "save-settings" || arguments[0] == "control") {
		mode = arguments[0]
		arguments = arguments[1:]
	}
	flags := flag.NewFlagSet("codex-auto-retry", flag.ContinueOnError)
	dataDirFlag := flags.String("data-dir", "", "runtime data directory")
	settingsFileFlag := flags.String("settings-file", "", "settings payload path")
	actionFlag := flags.String("action", "", "retry control action")
	threadIDFlag := flags.String("thread-id", "", "Codex task identifier")
	noTrayFlag := flags.Bool("no-tray", false, "disable the Windows notification-area icon")
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
			os.Exit(1)
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

	config, err := loadOrCreateConfig(filepath.Join(dataDir, "config.json"))
	if err != nil {
		logger.Printf("startup failed category=config")
		return
	}
	daemon, err := newDaemon(config, dataDir, logger, newAppResumeRunner(config, dataDir))
	if err != nil {
		logger.Printf("startup failed category=state")
		return
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
	logger.Printf("watchdog stopped")
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
	return service.setLocalSettings(payload.RetrySettings, payload.Paused, time.Now().UTC())
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
