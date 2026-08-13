[CmdletBinding()]
param(
    [string]$DataDir = (Join-Path $env:LOCALAPPDATA 'CodexAutoRetry')
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'environment.ps1')

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
    if ($null -eq $process -or -not [string]::Equals([string]$process.ExecutablePath, [string]$state.executable, [System.StringComparison]::OrdinalIgnoreCase) -or
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
if (-not $legacyOwnedEndpoint -and $runValue.IndexOf($watchdog, [System.StringComparison]::OrdinalIgnoreCase) -ge 0) {
    $legacyPort = Get-CodexAutoRetrySharedAppServerPort -ConfigPath (Join-Path $dataRoot 'config.json')
    $legacyOwnedEndpoint += 'ws://127.0.0.1:' + $legacyPort
    $legacyOwnedEndpoint += 'ws://127.0.0.1:49621', 'ws://127.0.0.1:49321'
}

New-Item -ItemType File -Force -Path $supervisorStop | Out-Null
Stop-ExactExecutable $watchdog
Stop-ExactExecutable $mcp
$sharedStopped = Stop-OwnedSharedServer $dataRoot

if ([string]::IsNullOrWhiteSpace($runValue) -or $runValue.IndexOf($watchdog, [System.StringComparison]::OrdinalIgnoreCase) -ge 0) {
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
    StartupRemoved = [string]::IsNullOrWhiteSpace([string](Get-ItemProperty -Path $runKey -Name $runName -ErrorAction SilentlyContinue).$runName)
    EndpointRestored = [bool]$environmentResult.Restored
    EndpointChangedByUser = [bool]$environmentResult.ChangedByUser
    ApiKeyPreserved = $true
    DataDeleted = $false
}
