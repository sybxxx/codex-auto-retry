[CmdletBinding()]
param(
    [switch]$KeepData
)

$ErrorActionPreference = 'Stop'
$installDir = Join-Path $env:LOCALAPPDATA 'CodexAutoRetry'
$watchdogTarget = Join-Path $installDir 'codex-auto-retry.exe'
$mcpTarget = Join-Path $installDir 'codex-auto-retry-mcp.exe'
$settingsTarget = Join-Path $installDir 'settings.ps1'
$stopSignal = Join-Path $installDir 'stop.signal'
$supervisorStop = Join-Path $installDir 'supervisor.stop'
$runKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'
$runName = 'CodexAutoRetry'
. (Join-Path $PSScriptRoot 'environment.ps1')

$runProperty = Get-ItemProperty -Path $runKey -Name $runName -ErrorAction SilentlyContinue
$runValue = if ($null -eq $runProperty) { '' } else { [string]$runProperty.$runName }
$stateEndpoint = $null
$statePath = Join-Path $installDir 'shared-server.json'
if (Test-Path -LiteralPath $statePath -PathType Leaf) {
    try {
        $state = Get-Content -Raw -Encoding UTF8 -LiteralPath $statePath | ConvertFrom-Json
        if ([string]$state.owner -eq 'codex-auto-retry' -and [string]$state.endpoint -match '^ws://127\.0\.0\.1:\d+$') {
            $stateEndpoint = [string]$state.endpoint
        }
    } catch { }
}
$legacyOwnedEndpoint = @()
if ($stateEndpoint) { $legacyOwnedEndpoint += $stateEndpoint }
if (-not $legacyOwnedEndpoint -and $runValue.IndexOf((Join-Path $installDir 'codex-auto-retry.exe'), [System.StringComparison]::OrdinalIgnoreCase) -ge 0) {
    $legacyPort = Get-CodexAutoRetrySharedAppServerPort -ConfigPath (Join-Path $installDir 'config.json')
    $legacyOwnedEndpoint += 'ws://127.0.0.1:' + $legacyPort
    $legacyOwnedEndpoint += 'ws://127.0.0.1:49621', 'ws://127.0.0.1:49321'
}

if (Test-Path -LiteralPath $installDir) {
    New-Item -ItemType File -Force -Path $supervisorStop | Out-Null
    New-Item -ItemType File -Force -Path $stopSignal | Out-Null
}
$deadline = (Get-Date).AddSeconds(12)
do {
    $process = Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
        Where-Object { $_.ExecutablePath -and [string]::Equals($_.ExecutablePath, $watchdogTarget, [System.StringComparison]::OrdinalIgnoreCase) }
    if ($process) { Start-Sleep -Milliseconds 250 }
} while ($process -and (Get-Date) -lt $deadline)
if ($process) {
    $process | ForEach-Object { Stop-Process -Id $_.ProcessId -Force }
}
$mcpProcesses = Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
    Where-Object { $_.ExecutablePath -and [string]::Equals($_.ExecutablePath, $mcpTarget, [System.StringComparison]::OrdinalIgnoreCase) }
if ($mcpProcesses) {
    $mcpProcesses | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
}
$settingsProcesses = Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
    Where-Object {
        $_.CommandLine -and
        $_.CommandLine.IndexOf($settingsTarget, [System.StringComparison]::OrdinalIgnoreCase) -ge 0
    }
if ($settingsProcesses) {
    $settingsProcesses | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
}

Remove-ItemProperty -Path $runKey -Name $runName -ErrorAction SilentlyContinue
$environmentResult = Restore-CodexAutoRetrySharedEnvironment -DataDir $installDir -LegacyOwnedEndpoint $legacyOwnedEndpoint
$sharedServerStopped = Stop-CodexAutoRetrySharedServerIfUnused -DataDir $installDir
if (Test-Path -LiteralPath $installDir) {
    if ($KeepData) {
        foreach ($runtimeFile in @(
            $watchdogTarget,
            $mcpTarget,
            $stopSignal,
            $supervisorStop,
            (Join-Path $installDir 'daemon.lock'),
            (Join-Path $installDir 'status.json'),
            (Join-Path $installDir 'settings.ps1')
        )) {
            Remove-Item -LiteralPath $runtimeFile -Force -ErrorAction SilentlyContinue
        }
    }
    else {
        Remove-Item -LiteralPath $installDir -Recurse -Force
    }
}

[pscustomobject]@{
    Installed = $false
    DataPreserved = [bool]$KeepData
    EnvironmentRestored = [bool]$environmentResult.Restored
    EnvironmentChangedByUser = [bool]$environmentResult.ChangedByUser
    SharedServerStopped = [bool]$sharedServerStopped
}
