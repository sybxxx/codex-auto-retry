//go:build windows

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type desktopTransportState string

const (
	desktopStopped      desktopTransportState = "stopped"
	desktopLegacyStdio  desktopTransportState = "legacy_stdio"
	desktopSharedServer desktopTransportState = "shared_server"
	desktopUnknown      desktopTransportState = "unknown"
)

type desktopTransportChecker interface {
	State(context.Context) (desktopTransportState, error)
}

type powerShellDesktopTransportChecker struct {
	configuredExecutable string
	expectedEndpoint     func() string
}

const desktopTransportScript = `$ErrorActionPreference = 'Stop'
$all = @(Get-CimInstance Win32_Process -ErrorAction Stop)
$main = @($all | Where-Object {
    $_.Name -eq 'ChatGPT.exe' -and
    $_.ExecutablePath -and
    ($_.ExecutablePath -match '(?i)\\WindowsApps\\OpenAI\.Codex_[^\\]+\\app\\ChatGPT\.exe$' -or
     $_.ExecutablePath -match '(?i)\\OpenAI\\Codex\\ChatGPT\.exe$') -and
    (-not $_.CommandLine -or $_.CommandLine -notmatch '(?:^|\s)--type=')
})
if ($main.Count -eq 0) {
    [Console]::Out.Write('stopped')
    return
}
$mainIds = @($main | ForEach-Object { [int]$_.ProcessId })
# The client is Desktop (or its Electron network process), not the listening
# app-server. Do not count arbitrary task/CLI descendants as Desktop clients.
$clientIds = @($mainIds) + @($all | Where-Object {
    $mainIds -contains [int]$_.ParentProcessId -and
    @($main | ForEach-Object { $_.ExecutablePath }) -contains $_.ExecutablePath -and
    $_.CommandLine -match '--utility-sub-type=network\.mojom\.NetworkService'
} | ForEach-Object { [int]$_.ProcessId })
$owned = @($all | Where-Object {
    $_.Name -eq 'codex.exe' -and
    $mainIds -contains [int]$_.ParentProcessId -and
    $_.CommandLine -match '(?:^|\s)app-server(?:\s|$)'
})
$legacy = @($owned | Where-Object {
    $_.CommandLine -notmatch '(?:^|\s)--listen(?:=|\s)'
})
if ($legacy.Count -gt 0) {
    [Console]::Out.Write('legacy_stdio')
} else {
    $connections = @(Get-NetTCPConnection -State Established -ErrorAction Stop | Where-Object {
        $clientIds -contains [int]$_.OwningProcess -and
        $_.RemoteAddress -eq '127.0.0.1' -and [int]$_.RemotePort -eq $expectedPort
    })
    if ($connections.Count -gt 0) { [Console]::Out.Write('shared_server') }
    else { [Console]::Out.Write('unknown') }
}`

func (c powerShellDesktopTransportChecker) State(ctx context.Context) (desktopTransportState, error) {
	if c.expectedEndpoint == nil {
		return desktopUnknown, nil
	}
	endpoint := c.expectedEndpoint()
	port := endpointPort(endpoint)
	if !validSharedServerEndpoint(endpoint, port) {
		return desktopUnknown, nil
	}
	powerShell, err := resolvePowerShellExecutable(c.configuredExecutable)
	if err != nil {
		return "", err
	}
	commandCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	command := exec.CommandContext(commandCtx, powerShell,
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", "-")
	command.Stdin = strings.NewReader(powerShellScriptInput("$expectedPort = " + strconv.Itoa(port) + "\n" + desktopTransportScript))
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
	case desktopUnknown:
		return desktopUnknown, nil
	default:
		return "", errors.New("unrecognized Codex desktop transport state")
	}
}
