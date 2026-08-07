package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestResolvePowerShellExecutable(t *testing.T) {
	path, err := resolvePowerShellExecutable("")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		t.Fatalf("resolved PowerShell executable is invalid: %s", path)
	}
}

func TestValidateDispatchResult(t *testing.T) {
	result, err := validateDispatchResult(DispatchResult{
		Outcome: outcomeDispatched,
		Action:  actionGoalResume,
		Reason:  "goal_resumed_in_background",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != actionGoalResume {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestValidateDispatchResultRejectsInvalidAction(t *testing.T) {
	_, err := validateDispatchResult(DispatchResult{Outcome: outcomeDispatched, Action: "unknown"})
	if err == nil {
		t.Fatal("invalid controller action was accepted")
	}
}

func TestValidateDispatchResultRejectsUnsafeReason(t *testing.T) {
	_, err := validateDispatchResult(DispatchResult{
		Outcome: outcomeRetryLater,
		Reason:  "provider response body must not be logged",
	})
	if err == nil {
		t.Fatal("unsafe result reason was accepted")
	}
}

func TestValidateDispatchResultAcceptsParentOwnedSubagentRecovery(t *testing.T) {
	result, err := validateDispatchResult(DispatchResult{
		Outcome: outcomeDispatched, Action: actionSubagentContinue,
		Reason: "subagent_resumed_in_background", ParentNotified: true,
	})
	if err != nil || result.Action != actionSubagentContinue || !result.ParentNotified {
		t.Fatalf("valid subagent recovery result was rejected: result=%+v err=%v", result, err)
	}
}

func TestValidateDispatchResultAcceptsGoalBlock(t *testing.T) {
	result, err := validateDispatchResult(DispatchResult{
		Outcome: outcomeDispatched, Action: actionGoalBlock,
		Reason: "goal_blocked_after_empty_response_limit",
	})
	if err != nil || result.Action != actionGoalBlock {
		t.Fatalf("valid goal-block result was rejected: result=%+v err=%v", result, err)
	}
}

func TestValidateDispatchResultRejectsActionFromWrongJobKind(t *testing.T) {
	goalResume := DispatchResult{Outcome: outcomeDispatched, Action: actionGoalResume, Reason: "goal_resumed_in_background"}
	if _, err := validateDispatchResultForJob(jobGoalBlock, goalResume); err == nil {
		t.Fatal("goal-stop job accepted a recovery action")
	}
	goalBlock := DispatchResult{Outcome: outcomeDispatched, Action: actionGoalBlock, Reason: "goal_blocked_after_empty_response_limit"}
	if _, err := validateDispatchResultForJob(jobRecovery, goalBlock); err == nil {
		t.Fatal("recovery job accepted a goal-stop action")
	}
}

func TestPowerShellScriptInputEndsWithBlankLine(t *testing.T) {
	input := powerShellScriptInput("try {\n  Write-Output 'ok'\n}\n")
	if !strings.HasSuffix(input, "\r\n\r\n") {
		t.Fatalf("PowerShell input does not end with a submission blank line: %q", input[len(input)-8:])
	}
}

func TestWindowsPowerShellExecutesFinalCompoundStatementFromStdin(t *testing.T) {
	powerShell, err := resolvePowerShellExecutable("")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, powerShell,
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", "-")
	cmd.Stdin = strings.NewReader(powerShellScriptInput("try {\n  Write-Output 'submitted'\n} catch {\n  exit 1\n}\n"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("PowerShell stdin execution failed: %v", err)
	}
	if strings.TrimSpace(string(output)) != "submitted" {
		t.Fatalf("final PowerShell compound statement was not submitted: %q", output)
	}
}

func TestControllerFailureReasonUsesSafeCodes(t *testing.T) {
	if got := controllerFailureReason(DispatchResult{}, errSharedServerUnavailable); got != "codex_background_channel_unavailable" {
		t.Fatalf("unexpected shared-server reason: %s", got)
	}
	if got := controllerFailureReason(DispatchResult{}, errSharedServerPortReserved); got != "shared_app_server_port_reserved" || !controllerFailureNeedsAction(got) {
		t.Fatalf("reserved shared-server port was not exposed as an actionable reason: %s", got)
	}
	if got := localSettingsExitCode(errSharedServerPortReserved); got != localSettingsExitPortReserved {
		t.Fatalf("reserved shared-server port did not get a distinct settings exit code: %d", got)
	}
	result := DispatchResult{Outcome: outcomeRetryLater, Reason: "thread_active"}
	if got := controllerFailureReason(result, nil); got != result.Reason {
		t.Fatalf("safe controller reason was not preserved: %s", got)
	}
	if got := controllerFailureReason(DispatchResult{Outcome: outcomeRetryLater, Reason: "unsafe value"}, errors.New("details")); got != "controller_unavailable" {
		t.Fatalf("unsafe controller reason was logged: %s", got)
	}
	if got := controllerFailureReason(DispatchResult{}, errResumeSettingsUnavailable); got != "thread_settings_unavailable" {
		t.Fatalf("unexpected settings reason: %s", got)
	}
	if got := controllerFailureReason(DispatchResult{}, &controllerReasonError{reason: "codex_not_running"}); got != "codex_not_running" {
		t.Fatalf("explicit controller reason was lost: %s", got)
	}
	if got := controllerFailureReason(DispatchResult{}, errSharedAppServerDisabled); got != "shared_app_server_disabled" {
		t.Fatalf("disabled shared mode error was not classified: %s", got)
	}
	if got := controllerFailureReason(DispatchResult{Outcome: outcomeRetryLater, Reason: "shared_app_server_disabled"}, nil); got != "shared_app_server_disabled" || !controllerFailureNeedsAction(got) {
		t.Fatalf("disabled shared mode was not treated as a terminal fail-open condition: %s", got)
	}
}
