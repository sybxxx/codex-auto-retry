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

type appResumeRunner struct {
	controller *rendererController
	configPath string
}

var (
	errControllerTimeout       = errors.New("app controller timed out")
	errControllerInvalidResult = errors.New("app controller returned an invalid result")
)

func newAppResumeRunner(config Config, dataDir string) *appResumeRunner {
	return &appResumeRunner{
		controller: newRendererController(config),
		configPath: filepath.Join(dataDir, "config.json"),
	}
}

func (r *appResumeRunner) Resume(ctx context.Context, job RetryJob) (DispatchResult, error) {
	if !threadIDPattern.MatchString(job.ThreadID + ".jsonl") {
		return DispatchResult{}, errors.New("invalid thread id")
	}
	if job.Kind == jobGoalBlock {
		controllerCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		result, err := r.controller.BlockGoal(controllerCtx, job.ThreadID)
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
		return r.controller.config.RetryPrompt, nil
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
	switch {
	case errors.Is(err, errControllerTimeout):
		return "controller_timeout"
	case errors.Is(err, errRendererTargetNotFound):
		return "codex_background_channel_unavailable"
	case errors.Is(err, errRendererProtocol):
		return "codex_background_channel_failed"
	case errors.Is(err, errRendererEvaluation):
		return "codex_background_dispatch_failed"
	case errors.Is(err, errControllerInvalidResult):
		return "controller_invalid_result"
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
	if cfg.DelayStrategy == delayStrategyFixed {
		return delay
	}
	for i := 1; i < consecutiveRetry; i++ {
		if delay >= time.Duration(cfg.MaxDelaySeconds)*time.Second/2 {
			delay = time.Duration(cfg.MaxDelaySeconds) * time.Second
			break
		}
		delay *= 2
	}
	maximum := time.Duration(cfg.MaxDelaySeconds) * time.Second
	if delay > maximum {
		return maximum
	}
	return delay
}
