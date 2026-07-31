//go:build windows

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

type desktopTransportState string

const (
	desktopStopped      desktopTransportState = "stopped"
	desktopLegacyStdio  desktopTransportState = "legacy_stdio"
	desktopSharedServer desktopTransportState = "shared_server"
)

type desktopTransportChecker interface {
	State(context.Context) (desktopTransportState, error)
}

type powerShellDesktopTransportChecker struct {
	configuredExecutable string
}

const desktopTransportScript = `$ErrorActionPreference = 'Stop'
$all = @(Get-CimInstance Win32_Process -ErrorAction Stop)
$main = @($all | Where-Object {
    $_.Name -eq 'ChatGPT.exe' -and
    (-not $_.CommandLine -or $_.CommandLine -notmatch '(?:^|\s)--type=')
})
if ($main.Count -eq 0) {
    [Console]::Out.Write('stopped')
    return
}
$mainIds = @($main | ForEach-Object { [int]$_.ProcessId })
$owned = @($all | Where-Object {
    $_.Name -eq 'codex.exe' -and
    $mainIds -contains [int]$_.ParentProcessId -and
    $_.CommandLine -match '(?:^|\s)app-server(?:\s|$)'
})
if ($owned.Count -gt 0) {
    [Console]::Out.Write('legacy_stdio')
} else {
    [Console]::Out.Write('shared_server')
}`

func (c powerShellDesktopTransportChecker) State(ctx context.Context) (desktopTransportState, error) {
	powerShell, err := resolvePowerShellExecutable(c.configuredExecutable)
	if err != nil {
		return "", err
	}
	commandCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	command := exec.CommandContext(commandCtx, powerShell,
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", "-")
	command.Stdin = strings.NewReader(powerShellScriptInput(desktopTransportScript))
	var stdout bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = io.Discard
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	if err := command.Run(); err != nil {
		return "", err
	}
	switch desktopTransportState(strings.TrimSpace(stdout.String())) {
	case desktopStopped:
		return desktopStopped, nil
	case desktopLegacyStdio:
		return desktopLegacyStdio, nil
	case desktopSharedServer:
		return desktopSharedServer, nil
	default:
		return "", errors.New("unrecognized Codex desktop transport state")
	}
}
