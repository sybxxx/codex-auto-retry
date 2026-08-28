package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

const (
	supervisorInitialBackoff = time.Second
	supervisorMaxBackoff     = time.Minute
)

type supervisorLocker interface {
	Close() error
}

// runSupervisor is the process kept in the Windows sign-in entry. The worker
// owns the tray, state, retry queue, and shared app-server. Keeping those
// responsibilities in the worker means a worker crash cannot leave the
// startup entry pointing at a dead process; the supervisor simply starts a
// fresh worker after a bounded backoff.
func runSupervisor(dataDir string, noTray bool) error {
	lock, err := acquireSupervisorLock(filepath.Join(dataDir, "supervisor.lock"))
	if err != nil {
		// A second sign-in trigger is harmless: the existing supervisor already
		// owns the worker lifecycle.
		return nil
	}
	defer lock.Close()

	executable, err := os.Executable()
	if err != nil {
		return err
	}
	stopPath := filepath.Join(dataDir, "supervisor.stop")
	// A marker left by a previous clean session must not disable the next
	// Windows sign-in. During this supervisor instance the worker writes a new
	// marker, which is consumed after the worker exits.
	_ = os.Remove(stopPath)
	backoff := supervisorInitialBackoff
	for {
		args := []string{"run", "--data-dir", dataDir}
		args = append(args, "--supervised")
		if noTray {
			args = append(args, "--no-tray")
		}
		worker := exec.Command(executable, args...)
		worker.Dir = dataDir
		configureSupervisorWorker(worker)
		appendSupervisorLog(dataDir, fmt.Sprintf("worker starting version=%s", appVersion))
		if err := worker.Start(); err != nil {
			cleanupSupervisorSharedBackend(dataDir)
			appendSupervisorLog(dataDir, "worker start failed category=process_start")
			if waitSupervisorBackoff(stopPath, backoff) {
				return nil
			}
			backoff = nextSupervisorBackoff(backoff)
			continue
		}

		workerPID := worker.Process.Pid
		err = worker.Wait()
		cleanupSupervisorSharedBackend(dataDir)
		intentionalStop := consumeSupervisorStop(stopPath)
		if !intentionalStop && workerStateCleanlyStopped(dataDir, workerPID) {
			// The worker writes this marker only for an intentional shutdown when
			// it was started with --supervised. Treat the marker as authoritative
			// even if the supervisor observes the exit a moment later.
			intentionalStop = consumeSupervisorStop(stopPath)
		}
		if intentionalStop {
			appendSupervisorLog(dataDir, fmt.Sprintf("worker stopped intentionally pid=%d", workerPID))
			return nil
		}
		if err != nil {
			appendSupervisorLog(dataDir, fmt.Sprintf("worker exited unexpectedly pid=%d category=worker_exit code=%s", workerPID, supervisorExitCode(err)))
		} else {
			appendSupervisorLog(dataDir, fmt.Sprintf("worker exited unexpectedly pid=%d category=worker_exit", workerPID))
		}
		if waitSupervisorBackoff(stopPath, backoff) {
			return nil
		}
		backoff = nextSupervisorBackoff(backoff)
	}
}

// cleanupSupervisorSharedBackend closes the plugin-owned route before a dead
// worker can leave Codex pointing at an endpoint with no listener. Ownership is
// checked again by disableSharedAppServer; user-owned endpoints are preserved.
func cleanupSupervisorSharedBackend(dataDir string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := cleanupSharedBackend(ctx, dataDir); err != nil {
		appendSupervisorLog(dataDir, "shared backend cleanup deferred category=worker_exit")
	}
}

func workerStateCleanlyStopped(dataDir string, workerPID int) bool {
	statusPath := filepath.Join(dataDir, "status.json")
	data, err := os.ReadFile(statusPath)
	if err != nil {
		return false
	}
	var status StatusSnapshot
	if err := json.Unmarshal(data, &status); err != nil {
		return false
	}
	return status.PID == workerPID && !status.Running
}

func nextSupervisorBackoff(value time.Duration) time.Duration {
	if value >= supervisorMaxBackoff/2 {
		return supervisorMaxBackoff
	}
	return value * 2
}

func waitSupervisorBackoff(stopPath string, delay time.Duration) bool {
	deadline := time.NewTimer(delay)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if consumeSupervisorStop(stopPath) {
			return true
		}
		select {
		case <-deadline.C:
			return false
		case <-ticker.C:
		}
	}
}

func consumeSupervisorStop(path string) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	}
	_ = os.Remove(path)
	return true
}

func appendSupervisorLog(dataDir, message string) {
	path := filepath.Join(dataDir, "logs", "supervisor.log")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = fmt.Fprintf(file, "%s %s\n", time.Now().UTC().Format(time.RFC3339), message)
}

func supervisorExitCode(err error) string {
	if exitErr, ok := err.(*exec.ExitError); ok {
		return strconv.Itoa(exitErr.ExitCode())
	}
	return "unknown"
}
