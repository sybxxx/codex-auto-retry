[CmdletBinding()]
param(
    [string]$DataDir = (Join-Path $env:LOCALAPPDATA 'CodexAutoRetry')
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'environment.ps1')

function Test-OwnedStartupValue {
    param([AllowNull()][string]$Value)
    if ([string]::IsNullOrWhiteSpace($Value)) { return $false }
    $trimmed = $Value.Trim()
    if ($trimmed.StartsWith('"')) {
        $closingQuote = $trimmed.IndexOf('"', 1)
        if ($closingQuote -le 1) { return $false }
        $executable = $trimmed.Substring(1, $closingQuote - 1)
    }
    else {
        $executable = ($trimmed -split '[\s\t]', 2)[0]
    }
    return [string]::Equals($executable, $watchdog, [System.StringComparison]::OrdinalIgnoreCase)
}

function Stop-ExactExecutable {
    param([Parameter(Mandatory = $true)][string]$Path)
    $fullPath = [System.IO.Path]::GetFullPath($Path)
    @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
        Where-Object { $_.ExecutablePath -and [string]::Equals($_.ExecutablePath, $fullPath, [System.StringComparison]::OrdinalIgnoreCase) }) |
        ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
}

function Stop-OwnedSharedServer {
    param([Parameter(Mandatory = $true)][string]$Root)
    $statePath = Join-Path $Root 'shared-server.json'
    if (-not (Test-Path -LiteralPath $statePath -PathType Leaf)) { return $false }
    try { $state = Get-Content -Raw -Encoding UTF8 -LiteralPath $statePath | ConvertFrom-Json } catch { return $false }
    if ([string]$state.owner -ne 'codex-auto-retry' -or [string]::IsNullOrWhiteSpace([string]$state.version) -or
        [int]$state.pid -le 0 -or [string]$state.endpoint -notmatch '^ws://127\.0\.0\.1:\d+$' -or
        [string]::IsNullOrWhiteSpace([string]$state.executable)) { return $false }
    $process = Get-CimInstance Win32_Process -Filter ("ProcessId = " + [int]$state.pid) -ErrorAction SilentlyContinue
    if ($null -eq $process) {
        Remove-Item -LiteralPath $statePath -Force -ErrorAction SilentlyContinue
        return $true
    }
    $desktop = @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Where-Object {
        $_.Name -eq 'ChatGPT.exe' -and (-not $_.CommandLine -or $_.CommandLine -notmatch '(?:^|\s)--type=')
    })
    if ($desktop.Count -gt 0) {
        # Never terminate a shared app-server while Codex Desktop may still
        # have an inherited connection. Keep the ownership record so the next
        # safe-disable or service start can finish cleanup after Desktop exits.
        return $false
    }
    if (-not [string]::Equals([string]$process.ExecutablePath, [string]$state.executable, [System.StringComparison]::OrdinalIgnoreCase) -or
        [string]$process.CommandLine -notmatch '(?i)(^|\s)app-server(\s|$)' -or
        [string]$process.CommandLine.IndexOf([string]$state.endpoint, [System.StringComparison]::OrdinalIgnoreCase) -lt 0) { return $false }
    Stop-Process -Id ([int]$state.pid) -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $statePath -Force -ErrorAction SilentlyContinue
    return $true
}

$dataRoot = [System.IO.Path]::GetFullPath($DataDir)
$watchdog = Join-Path $dataRoot 'codex-auto-retry.exe'
$mcp = Join-Path $dataRoot 'codex-auto-retry-mcp.exe'
$runKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'
$runName = 'CodexAutoRetry'
$supervisorStop = Join-Path $dataRoot 'supervisor.stop'
$beforeKey = [Environment]::GetEnvironmentVariable('CODEX_API_KEY', 'User')
$beforeEndpoint = [Environment]::GetEnvironmentVariable('CODEX_APP_SERVER_WS_URL', 'User')
$stateEndpoint = $null
$statePath = Join-Path $dataRoot 'shared-server.json'
if (Test-Path -LiteralPath $statePath -PathType Leaf) {
    try {
        $state = Get-Content -Raw -Encoding UTF8 -LiteralPath $statePath | ConvertFrom-Json
        if ([string]$state.owner -eq 'codex-auto-retry' -and [string]$state.endpoint -match '^ws://127\.0\.0\.1:\d+$') {
            $stateEndpoint = [string]$state.endpoint
        }
    } catch { $stateEndpoint = $null }
}

$runProperty = Get-ItemProperty -Path $runKey -Name $runName -ErrorAction SilentlyContinue
$runValue = if ($null -eq $runProperty) { '' } else { [string]$runProperty.$runName }
$legacyOwnedEndpoint = @()
if ($stateEndpoint) { $legacyOwnedEndpoint += $stateEndpoint }
if (-not $legacyOwnedEndpoint -and (Test-OwnedStartupValue $runValue)) {
    $legacyPort = Get-CodexAutoRetrySharedAppServerPort -ConfigPath (Join-Path $dataRoot 'config.json')
    $legacyOwnedEndpoint += 'ws://127.0.0.1:' + $legacyPort
    $legacyOwnedEndpoint += 'ws://127.0.0.1:49621', 'ws://127.0.0.1:49321'
}

# Persist fail-open before removing the endpoint. A later startup must not
# re-enable the shared route if this cleanup is interrupted halfway through.
$sharedModeDisabled = Disable-CodexAutoRetrySharedMode -DataDir $dataRoot
New-Item -ItemType File -Force -Path $supervisorStop | Out-Null
Stop-ExactExecutable $watchdog
Stop-ExactExecutable $mcp
$sharedStopped = Stop-OwnedSharedServer $dataRoot

if ([string]::IsNullOrWhiteSpace($runValue) -or (Test-OwnedStartupValue $runValue)) {
    Remove-ItemProperty -Path $runKey -Name $runName -ErrorAction SilentlyContinue
}

$environmentResult = Restore-CodexAutoRetrySharedEnvironment -DataDir $dataRoot -LegacyOwnedEndpoint $legacyOwnedEndpoint
Send-CodexAutoRetryEnvironmentChange

$afterEndpoint = [Environment]::GetEnvironmentVariable('CODEX_APP_SERVER_WS_URL', 'User')
$afterKey = [Environment]::GetEnvironmentVariable('CODEX_API_KEY', 'User')
if ($beforeKey -ne $afterKey) { throw 'safe-disable changed CODEX_API_KEY unexpectedly.' }
if ($stateEndpoint -and $afterEndpoint -eq $stateEndpoint -and $stateEndpoint -match '^ws://127\.0\.0\.1:(\d+)$') {
    $listener = @(Get-NetTCPConnection -LocalPort ([int]$matches[1]) -State Listen -ErrorAction SilentlyContinue)
    if ($listener.Count -eq 0) { throw 'Codex still points at a dead plugin endpoint.' }
}

[pscustomobject]@{
    Status = 'disabled'
    WatchdogStopped = $true
    SharedServerStopped = [bool]$sharedStopped
    SharedServerCleanupDeferred = (-not [bool]$sharedStopped -and (Test-Path -LiteralPath $statePath -PathType Leaf))
    StartupRemoved = [string]::IsNullOrWhiteSpace([string](Get-ItemProperty -Path $runKey -Name $runName -ErrorAction SilentlyContinue).$runName)
    SharedModeDisabled = [bool]$sharedModeDisabled
    EndpointRestored = [bool]$environmentResult.Restored
    EndpointChangedByUser = [bool]$environmentResult.ChangedByUser
    ApiKeyPreserved = $true
    DataDeleted = $false
}
