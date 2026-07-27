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

func TestValidateDispatchResultAcceptsParentOwnedSubagent(t *testing.T) {
	result, err := validateDispatchResult(DispatchResult{
		Outcome: outcomeNotApplicable,
		Reason:  "subagent_owned_by_parent",
	})
	if err != nil || result.Outcome != outcomeNotApplicable {
		t.Fatalf("valid not-applicable result was rejected: result=%+v err=%v", result, err)
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
	if got := controllerFailureReason(DispatchResult{}, errRendererTargetNotFound); got != "codex_background_channel_unavailable" {
		t.Fatalf("unexpected target reason: %s", got)
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
}

func TestParseDebugPorts(t *testing.T) {
	ports := parseDebugPorts("62482\r\n62482\r\n9222\r\n99999\r\n")
	if len(ports) != 2 || ports[0] != 9222 || ports[1] != 62482 {
		t.Fatalf("unexpected debug ports: %v", ports)
	}
}

func TestRendererDispatchUsesOnlyBackgroundAppServerMethods(t *testing.T) {
	required := []string{
		"hydrate-background-threads",
		"send-cli-request-for-host",
		"thread/loaded/list",
		"thread/resume",
		"thread/goal/get",
		"thread/goal/set",
		"turn/start",
	}
	if !strings.Contains(rendererDispatchExpression, "subagent_owned_by_parent") {
		t.Fatal("background controller does not delegate subagent recovery to its parent task")
	}
	for _, value := range required {
		if !strings.Contains(rendererDispatchExpression, value) {
			t.Fatalf("background controller is missing %q", value)
		}
	}
	forbidden := []string{
		"codex://threads/",
		"codex exec",
		"UIAutomation",
		"window.open",
		"location.assign",
		"location.replace",
	}
	for _, value := range forbidden {
		if strings.Contains(rendererDispatchExpression, value) {
			t.Fatalf("background controller contains foreground mechanism %q", value)
		}
	}
}
