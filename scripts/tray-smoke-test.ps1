[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$binary = Join-Path $PSScriptRoot 'bin\codex-auto-retry.exe'
$testRoot = Join-Path $env:TEMP ("codex-auto-retry-tray-" + [guid]::NewGuid().ToString('N'))
$dataDir = Join-Path $testRoot 'data'
$process = $null
$oldSettingsSmoke = $env:CODEX_AUTO_RETRY_SETTINGS_SMOKE

Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class CodexAutoRetryTrayProbe {
    [DllImport("user32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    public static extern IntPtr FindWindow(string className, string windowName);
    [DllImport("user32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    public static extern bool PostMessage(IntPtr hWnd, uint message, IntPtr wParam, IntPtr lParam);
}
'@

try {
    if (-not (Test-Path -LiteralPath $binary -PathType Leaf)) {
        throw "Built watchdog not found: $binary"
    }
    New-Item -ItemType Directory -Force -Path $dataDir | Out-Null
    $config = [ordered]@{
        config_version = 3
        poll_interval_seconds = 1
        initial_delay_seconds = 2
        max_delay_seconds = 30
        max_retry_attempts = 5
        max_parallel_retries = 2
        start_ack_timeout_seconds = 10
        auth_max_attempts = 3
        unknown_max_attempts = 2
        session_roots = @()
        include_default_home = $false
        include_cockpit_homes = $false
        retry_prompt = 'Continue tray smoke test.'
        show_notifications = $false
    }
    [System.IO.File]::WriteAllText(
        (Join-Path $dataDir 'config.json'),
        ($config | ConvertTo-Json -Depth 5),
        [System.Text.UTF8Encoding]::new($false)
    )
    $env:CODEX_AUTO_RETRY_SETTINGS_SMOKE = '1'
    $process = Start-Process -FilePath $binary -ArgumentList @('run', '--data-dir', $dataDir) -WindowStyle Hidden -PassThru
    $env:CODEX_AUTO_RETRY_SETTINGS_SMOKE = $oldSettingsSmoke
    $className = "CodexAutoRetryTray-$($process.Id)"
    $deadline = (Get-Date).AddSeconds(15)
    $trayWindow = [IntPtr]::Zero
    do {
        Start-Sleep -Milliseconds 200
        $process.Refresh()
        if ($process.HasExited) { throw "Watchdog exited before creating the tray icon (exit=$($process.ExitCode))." }
        $trayWindow = [CodexAutoRetryTrayProbe]::FindWindow($className, $className)
    } while ($trayWindow -eq [IntPtr]::Zero -and (Get-Date) -lt $deadline)
    if ($trayWindow -eq [IntPtr]::Zero) {
        throw 'Tray window was not created.'
    }
    $statusPath = Join-Path $dataDir 'status.json'
    $deadline = (Get-Date).AddSeconds(10)
    do {
        Start-Sleep -Milliseconds 200
        $status = $null
        if (Test-Path -LiteralPath $statusPath) {
            try { $status = Get-Content -Raw -Encoding UTF8 -LiteralPath $statusPath | ConvertFrom-Json } catch { }
        }
    } while ((-not $status -or -not $status.running -or $status.pid -ne $process.Id) -and (Get-Date) -lt $deadline)
    if (-not $status -or -not $status.running -or $status.pid -ne $process.Id) {
        throw 'Tray watchdog did not publish a valid heartbeat.'
    }
    if (-not [CodexAutoRetryTrayProbe]::PostMessage($trayWindow, 0x0111, [IntPtr]1001, [IntPtr]::Zero)) {
        throw 'Could not request the graphical settings window through the tray controller.'
    }
    $settingsMarker = Join-Path $dataDir 'settings-smoke.ok'
    $deadline = (Get-Date).AddSeconds(10)
    do { Start-Sleep -Milliseconds 200 } while (-not (Test-Path -LiteralPath $settingsMarker) -and (Get-Date) -lt $deadline)
    if (-not (Test-Path -LiteralPath $settingsMarker)) {
        throw 'The tray settings window did not initialize.'
    }
    $logPath = Join-Path $dataDir 'logs\daemon.log'
    $log = if (Test-Path -LiteralPath $logPath) { Get-Content -Raw -Encoding UTF8 -LiteralPath $logPath } else { '' }
    if ($log -match 'category=tray_error') {
        throw 'Tray initialization reported an error.'
    }
    New-Item -ItemType File -Force -Path (Join-Path $dataDir 'stop.signal') | Out-Null
    if (-not $process.WaitForExit(10000)) {
        throw 'Tray watchdog did not exit cleanly.'
    }
    $status = Get-Content -Raw -Encoding UTF8 -LiteralPath $statusPath | ConvertFrom-Json
    if ($status.running -or $status.pending_retries -ne 0 -or $status.active_retries -ne 0) {
        throw 'Final status retained stale running or retry state.'
    }
    [pscustomobject]@{
        Status = 'passed'
        TrayWindowCreated = $true
        SettingsWindowInitialized = $true
        HeartbeatPublished = $true
        FinalStateClean = $true
    }
}
finally {
    if ($null -ne $oldSettingsSmoke) { $env:CODEX_AUTO_RETRY_SETTINGS_SMOKE = $oldSettingsSmoke }
    else { Remove-Item Env:CODEX_AUTO_RETRY_SETTINGS_SMOKE -ErrorAction SilentlyContinue }
    if ($process -and -not $process.HasExited) {
        Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
    }
    if (Test-Path -LiteralPath $testRoot) {
        $resolvedTestRoot = [System.IO.Path]::GetFullPath($testRoot)
        $resolvedTemp = [System.IO.Path]::GetFullPath($env:TEMP).TrimEnd('\') + '\'
        if (-not $resolvedTestRoot.StartsWith($resolvedTemp, [System.StringComparison]::OrdinalIgnoreCase)) {
            throw "Refusing to remove unexpected tray smoke-test path: $resolvedTestRoot"
        }
        Remove-Item -LiteralPath $resolvedTestRoot -Recurse -Force
    }
}
