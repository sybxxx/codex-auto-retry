package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type resumeRunner interface {
	Resume(context.Context, RetryJob) (DispatchResult, error)
}

type recoveryController interface {
	Prepare(context.Context) error
	Readiness(context.Context) (string, error)
	Dispatch(
		context.Context, string, string, ResumeSettings, time.Time, time.Time,
		string, bool, bool, FailureClass, string,
	) (DispatchResult, error)
	BlockGoal(context.Context, string, *ResumeSettings, string) (DispatchResult, error)
}

type appResumeRunner struct {
	controller    recoveryController
	configPath    string
	defaultPrompt string
}

type controllerStateRunner interface {
	ControllerState(context.Context) string
}

// sharedBackendFailureHandler is optional so the daemon can fail open when a
// shared endpoint becomes unhealthy after startup. A runner that does not own
// the shared backend keeps the existing test and fallback behavior.
type sharedBackendFailureHandler interface {
	FailOpenSharedBackend(context.Context) error
}

// retryLifecycleReader is optional so the daemon can safely inspect a turn
// that was acknowledged by Codex but never produced a matching completion.
// The probe is read-only and deliberately kept out of resumeRunner's required
// dispatch contract so test and fallback runners remain compatible.
type retryLifecycleReader interface {
	RetryThreadStatus(context.Context, string, string) (string, error)
}

var (
	errControllerTimeout       = errors.New("app controller timed out")
	errControllerInvalidResult = errors.New("app controller returned an invalid result")
)

type controllerReasonError struct {
	reason string
}

func (e *controllerReasonError) Error() string {
	return e.reason
}

func newAppResumeRunner(config Config, dataDir string, logger *safeLogger) *appResumeRunner {
	return &appResumeRunner{
		controller:    newSharedAppServerController(config, dataDir, logger),
		configPath:    filepath.Join(dataDir, "config.json"),
		defaultPrompt: config.RetryPrompt,
	}
}

func (r *appResumeRunner) Prepare(ctx context.Context) error {
	return r.controller.Prepare(ctx)
}

func (r *appResumeRunner) ControllerState(ctx context.Context) string {
	state, err := r.controller.Readiness(ctx)
	if err != nil {
		return controllerFailureReason(DispatchResult{}, err)
	}
	return state
}

func (r *appResumeRunner) FailOpenSharedBackend(ctx context.Context) error {
	if r.configPath == "" {
		return nil
	}
	config, err := loadOrCreateConfig(r.configPath)
	if err != nil {
		// Runtime config corruption must not strand Codex on a dead shared
		// endpoint. The fallback cleanup uses only ownership-verified state and
		// leaves the damaged config untouched for explicit repair.
		if cleanupErr := cleanupSharedBackend(ctx, filepath.Dir(r.configPath)); cleanupErr != nil {
			return fmt.Errorf("load config: %w; cleanup shared backend: %v", err, cleanupErr)
		}
		return nil
	}
	config.SharedAppServerEnabled = false
	if err := config.validate(); err != nil {
		return err
	}
	// Persist the fail-open decision before restoring the endpoint. If cleanup
	// is interrupted, a later startup still cannot take over Codex's backend.
	if err := writeJSONAtomic(r.configPath, config); err != nil {
		cleanupErr := disableSharedAppServer(ctx, filepath.Dir(r.configPath), config)
		if cleanupErr != nil {
			return fmt.Errorf("save fail-open setting: %w; cleanup shared backend: %v", err, cleanupErr)
		}
		return err
	}
	return disableSharedAppServer(ctx, filepath.Dir(r.configPath), config)
}

func (r *appResumeRunner) RetryThreadStatus(ctx context.Context, threadID, codexHome string) (string, error) {
	reader, ok := r.controller.(retryLifecycleReader)
	if !ok {
		return "", errors.New("retry lifecycle reader is unavailable")
	}
	return reader.RetryThreadStatus(ctx, threadID, codexHome)
}

func (r *appResumeRunner) Resume(ctx context.Context, job RetryJob) (DispatchResult, error) {
	if !threadIDPattern.MatchString(job.ThreadID + ".jsonl") {
		return DispatchResult{}, errors.New("invalid thread id")
	}
	if job.Kind == jobGoalBlock {
		var settings *ResumeSettings
		if loaded, err := loadLatestResumeSettings(job.RolloutPath); err == nil {
			settings = &loaded
		}
		controllerCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		result, err := r.controller.BlockGoal(controllerCtx, job.ThreadID, settings, job.CodexHome)
		if err != nil && controllerCtx.Err() != nil {
			return DispatchResult{}, errControllerTimeout
		}
		if err != nil {
			return DispatchResult{}, err
		}
		return validateDispatchResultForJob(job.Kind, result)
	}
	settings, err := loadLatestResumeSettings(job.RolloutPath)
	if err != nil {
		return DispatchResult{}, err
	}
	controllerCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	retryPrompt, err := r.retryPrompt()
	if err != nil {
		return DispatchResult{}, err
	}
	result, err := r.controller.Dispatch(
		controllerCtx,
		job.ThreadID,
		retryPrompt,
		settings,
		job.FailedAt,
		job.OriginTurnStartedAt,
		job.RecoveryEventID,
		job.ParentNotified,
		job.GoalLimitRestart,
		job.Class,
		job.CodexHome,
	)
	if err != nil && controllerCtx.Err() != nil {
		return DispatchResult{}, errControllerTimeout
	}
	if err != nil {
		return DispatchResult{}, err
	}
	return validateDispatchResultForJob(job.Kind, result)
}

func (r *appResumeRunner) retryPrompt() (string, error) {
	if r.configPath == "" {
		return r.defaultPrompt, nil
	}
	config, err := loadOrCreateConfig(r.configPath)
	if err != nil {
		return "", fmt.Errorf("reload retry prompt: %w", err)
	}
	return config.RetryPrompt, nil
}

func validateDispatchResult(result DispatchResult) (DispatchResult, error) {
	switch result.Outcome {
	case outcomeDispatched:
		if result.Action != actionGoalResume && result.Action != actionConversationContinue &&
			result.Action != actionSubagentContinue && result.Action != actionGoalBlock {
			return DispatchResult{}, fmt.Errorf("%w: dispatched action", errControllerInvalidResult)
		}
	case outcomeAwaitingStart:
		if result.Action != actionGoalActive {
			return DispatchResult{}, fmt.Errorf("%w: awaiting action", errControllerInvalidResult)
		}
	case outcomeUserActive, outcomeRetryLater:
	case outcomeNotApplicable:
		if result.Action != "" || result.Reason == "" {
			return DispatchResult{}, fmt.Errorf("%w: not applicable result", errControllerInvalidResult)
		}
	default:
		return DispatchResult{}, fmt.Errorf("%w: outcome", errControllerInvalidResult)
	}
	if result.Reason != "" && safeReasonCode(result.Reason) == "" {
		return DispatchResult{}, fmt.Errorf("%w: reason", errControllerInvalidResult)
	}
	return result, nil
}

func validateDispatchResultForJob(kind RetryJobKind, result DispatchResult) (DispatchResult, error) {
	validated, err := validateDispatchResult(result)
	if err != nil {
		return DispatchResult{}, err
	}
	if validated.Outcome != outcomeDispatched {
		return validated, nil
	}
	if kind == jobGoalBlock && validated.Action != actionGoalBlock {
		return DispatchResult{}, fmt.Errorf("%w: goal block action", errControllerInvalidResult)
	}
	if kind != jobGoalBlock && validated.Action == actionGoalBlock {
		return DispatchResult{}, fmt.Errorf("%w: recovery action", errControllerInvalidResult)
	}
	return validated, nil
}

func controllerFailureReason(result DispatchResult, err error) string {
	if result.Outcome == outcomeRetryLater || result.Outcome == outcomeUserActive {
		if reason := safeReasonCode(result.Reason); reason != "" {
			return reason
		}
	}
	var reasonError *controllerReasonError
	if errors.As(err, &reasonError) {
		if reason := safeReasonCode(reasonError.reason); reason != "" {
			return reason
		}
	}
	switch {
	case errors.Is(err, errControllerTimeout):
		return "controller_timeout"
	case errors.Is(err, errSharedServerPortReserved):
		return "shared_app_server_port_reserved"
	case errors.Is(err, errSharedServerPortConflict):
		return "shared_app_server_port_conflict"
	case errors.Is(err, errSharedAppServerEnvironmentConflict):
		return "shared_app_server_environment_conflict"
	case errors.Is(err, errSharedServerUnavailable):
		return "codex_background_channel_unavailable"
	case errors.Is(err, errAppServerRequest):
		return "codex_background_dispatch_failed"
	case errors.Is(err, errControllerInvalidResult):
		return "controller_invalid_result"
	case errors.Is(err, errCodexRestartRequired):
		return "codex_restart_required"
	case errors.Is(err, errSharedAppServerDisabled):
		return "shared_app_server_disabled"
	case errors.Is(err, errResumeSettingsUnavailable):
		return "thread_settings_unavailable"
	default:
		return "controller_unavailable"
	}
}

func safeReasonCode(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 {
		return ""
	}
	for _, character := range value {
		if character != '_' && character != '-' &&
			(character < 'a' || character > 'z') &&
			(character < '0' || character > '9') {
			return ""
		}
	}
	return value
}

func resolvePowerShellExecutable(configured string) (string, error) {
	configured = strings.TrimSpace(configured)
	if configured != "" {
		configured = expandPath(configured)
		if info, err := os.Stat(configured); err == nil && !info.IsDir() {
			return configured, nil
		}
		return "", fmt.Errorf("configured PowerShell executable does not exist: %s", configured)
	}
	if executable, err := exec.LookPath("powershell.exe"); err == nil {
		return executable, nil
	}
	if windows := os.Getenv("WINDIR"); windows != "" {
		candidate := filepath.Join(windows, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", errors.New("could not locate Windows PowerShell")
}

func powerShellScriptInput(script string) string {
	// Windows PowerShell treats `-Command -` as interactive stdin. The final
	// blank line submits the last compound statement before EOF.
	return strings.TrimRight(script, "\r\n") + "\r\n\r\n"
}

func retryDelay(consecutiveRetry int, cfg Config) time.Duration {
	if consecutiveRetry < 1 {
		consecutiveRetry = 1
	}
	delay := time.Duration(cfg.InitialDelaySeconds) * time.Second
	maximum := time.Duration(cfg.MaxDelaySeconds) * time.Second
	if cfg.DelayStrategy == delayStrategyFixed {
		return delay
	}
	if cfg.DelayStrategy == delayStrategyLinear {
		increment := time.Duration(cfg.DelayIncrementSeconds) * time.Second
		for attempt := 1; attempt < consecutiveRetry && delay < maximum; attempt++ {
			if increment >= maximum-delay {
				return maximum
			}
			delay += increment
		}
		return delay
	}
	for i := 1; i < consecutiveRetry; i++ {
		if delay >= maximum/2 {
			delay = maximum
			break
		}
		delay *= 2
	}
	if delay > maximum {
		return maximum
	}
	return delay
}
