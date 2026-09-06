[CmdletBinding()]
param()

$installDir = Join-Path $env:LOCALAPPDATA 'CodexAutoRetry'
$watchdogTarget = Join-Path $installDir 'codex-auto-retry.exe'
$mcpTarget = Join-Path $installDir 'codex-auto-retry-mcp.exe'
$statusPath = Join-Path $installDir 'status.json'
$configPath = Join-Path $installDir 'config.json'
$sharedStatePath = Join-Path $installDir 'shared-server.json'
. (Join-Path $PSScriptRoot 'path-safety.ps1')
. (Join-Path $PSScriptRoot 'startup-approval.ps1')
. (Join-Path $PSScriptRoot 'shared-server-status.ps1')
$redirectedPath = Get-CodexAutoRetryRedirectedPath -Path $installDir
$runtimePathRedirected = -not [string]::IsNullOrWhiteSpace([string]$redirectedPath)

function Test-CodexAutoRetryHeartbeatFresh {
    param(
        [Parameter(Mandatory = $true)]$Value,
        [TimeSpan]$MaxAge = ([TimeSpan]::FromSeconds(15))
    )
    try {
        # ConvertFrom-Json materializes ISO timestamps as DateTime values. Cast
        # those directly so a UTC Kind is preserved; stringifying first loses
        # the trailing Z and can add the local timezone offset a second time.
        $timestamp = if ($Value -is [DateTime]) {
            [DateTimeOffset]$Value
        }
        elseif ($Value -is [DateTimeOffset]) {
            $Value
        }
        else {
            [DateTimeOffset]::Parse([string]$Value)
        }
        $age = [DateTimeOffset]::UtcNow - $timestamp.ToUniversalTime()
        return $age -ge [TimeSpan]::Zero -and $age -le $MaxAge
    }
    catch {
        return $false
    }
}

$status = $null
$statusReadFailed = $false
if (Test-Path -LiteralPath $statusPath) {
    try { $status = Get-Content -Raw -Encoding UTF8 -LiteralPath $statusPath | ConvertFrom-Json } catch { $status = $null; $statusReadFailed = $true }
}
$statusPid = if ($status -and [int]$status.pid -gt 0) { [int]$status.pid } else { 0 }
$process = if ($statusPid -gt 0) {
    Get-CimInstance Win32_Process -Filter ('ProcessId = ' + $statusPid) -ErrorAction SilentlyContinue |
        Where-Object { $_.ExecutablePath -and [string]::Equals($_.ExecutablePath, $watchdogTarget, [System.StringComparison]::OrdinalIgnoreCase) } |
        Select-Object -First 1
}
else {
    Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
        Where-Object { $_.ExecutablePath -and [string]::Equals($_.ExecutablePath, $watchdogTarget, [System.StringComparison]::OrdinalIgnoreCase) } |
        Select-Object -First 1
}
$mcpProcesses = @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
    Where-Object { $_.ExecutablePath -and [string]::Equals($_.ExecutablePath, $mcpTarget, [System.StringComparison]::OrdinalIgnoreCase) })
$heartbeatFresh = $false
if ($status -and $status.last_scan_at) {
    $heartbeatFresh = Test-CodexAutoRetryHeartbeatFresh -Value $status.last_scan_at
}
$config = $null
if (Test-Path -LiteralPath $configPath) {
    try { $config = Get-Content -Raw -Encoding UTF8 -LiteralPath $configPath | ConvertFrom-Json } catch { $config = $null }
}
$runProperty = Get-ItemProperty -Path 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run' -Name 'CodexAutoRetry' -ErrorAction SilentlyContinue
$runValue = if ($null -eq $runProperty) { '' } else { [string]$runProperty.CodexAutoRetry }
$startupMode = if ([string]::IsNullOrWhiteSpace($runValue)) { 'missing' } elseif ($runValue -match '(?i)\bsupervise\b') { 'supervise' } elseif ($runValue -match '(?i)\brun\b') { 'run' } else { 'unknown' }
$startupApproval = Get-CodexAutoRetryStartupApproval -RunName 'CodexAutoRetry'
$userEndpoint = [Environment]::GetEnvironmentVariable('CODEX_APP_SERVER_WS_URL', 'User')
$sharedState = $null
$sharedStateReadFailed = $false
if (Test-Path -LiteralPath $sharedStatePath) {
    try { $sharedState = Get-Content -Raw -Encoding UTF8 -LiteralPath $sharedStatePath | ConvertFrom-Json } catch { $sharedState = $null; $sharedStateReadFailed = $true }
}
$expectedSharedPort = if ($config -and $config.PSObject.Properties['shared_app_server_port']) { [int]$config.shared_app_server_port } else { 0 }
$statusCompatibility = Get-CodexAutoRetryStatusCompatibility -Status $status -ReadFailed $statusReadFailed
$sharedVerification = if ($sharedStateReadFailed) {
    [pscustomobject][ordered]@{ Status = 'unknown'; Reason = 'state_unreadable'; PID = $null; Endpoint = $null }
} else {
    Get-CodexAutoRetrySharedServerStatus -State $sharedState -ExpectedPort $expectedSharedPort
}
$sharedStateStatus = [string]$sharedVerification.Status
$runtimeRunning = -not $runtimePathRedirected -and $null -ne $process -and [bool]$status.running -and $heartbeatFresh
$pendingRetries = if ($runtimeRunning -and $status) { $status.pending_retries } else { 0 }
$activeRetries = if ($runtimeRunning -and $status) { $status.active_retries } else { 0 }

[pscustomobject]@{
    Installed = -not $runtimePathRedirected -and (Test-Path -LiteralPath $watchdogTarget)
    RuntimePath = $installDir
    RuntimePathRedirected = $runtimePathRedirected
    RedirectedPath = if ($runtimePathRedirected) { $redirectedPath } else { $null }
    MCPServerInstalled = -not $runtimePathRedirected -and (Test-Path -LiteralPath $mcpTarget)
    MCPServerProcesses = $mcpProcesses.Count
    ProcessRunning = $runtimeRunning
    HeartbeatStale = -not $runtimeRunning
    PID = if ($process) { $process.ProcessId } else { $null }
    Version = if ($status) { $status.version } else { $null }
    LastScanAt = if ($status) { $status.last_scan_at } else { $null }
    WatchedRoots = if ($status) { $status.watched_roots } else { 0 }
    PendingRetries = $pendingRetries
    ActiveRetries = $activeRetries
    Paused = if ($status) { [bool]$status.paused } else { $false }
    ControllerState = if ($runtimePathRedirected) { 'runtime_path_redirected' } elseif (-not $runtimeRunning) { 'backend_service_not_running' } elseif ($status) { [string]$status.controller_state } else { $null }
    SharedAppServerEnabled = if ($runtimePathRedirected) { $false } elseif ($config -and $config.PSObject.Properties['shared_app_server_enabled']) { [bool]$config.shared_app_server_enabled } else { $false }
    SharedAppServerRequested = if ($config -and $config.PSObject.Properties['shared_app_server_requested']) { [bool]$config.shared_app_server_requested } elseif ($config -and $config.PSObject.Properties['shared_app_server_enabled']) { [bool]$config.shared_app_server_enabled } else { $false }
    StartupMode = $startupMode
    StartupEntry = if ([string]::IsNullOrWhiteSpace($runValue)) { $null } else { $runValue }
    StartupApproved = $startupApproval.Status
    SharedEndpointConfigured = -not [string]::IsNullOrWhiteSpace($userEndpoint)
    DesktopLaunchMode = Get-CodexAutoRetryStatusProperty -Status $status -Name 'desktop_launch_mode' -Default 'legacy_unprotected'
    BuildSourceHash = Get-CodexAutoRetryStatusProperty -Status $status -Name 'build_source_hash' -Default 'unverified_legacy_build'
    SafeLauncher = Join-Path $PSScriptRoot 'launch-codex.ps1'
    SharedServerState = $sharedStateStatus
    SharedServerVerification = [string]$sharedVerification.Reason
    SharedAppServerMemoryUsageMB = Get-CodexAutoRetryStatusProperty -Status $status -Name 'shared_app_server_memory_usage_mb' -Default 0
    SharedAppServerMemoryLimitMB = Get-CodexAutoRetryStatusProperty -Status $status -Name 'shared_app_server_memory_limit_mb' -Default $null
    SharedAppServerMemoryGuardTriggered = [bool](Get-CodexAutoRetryStatusProperty -Status $status -Name 'shared_app_server_memory_guard_triggered' -Default $false)
    RetrySafetyWarning = [string](Get-CodexAutoRetryStatusProperty -Status $status -Name 'retry_safety_warning' -Default '')
    StatusCompatibility = $statusCompatibility
    StatusCompatibilityMessage = Get-CodexAutoRetryStatusCompatibilityMessage -Status $statusCompatibility
    CodexRestartRequired = if ($runtimePathRedirected) { $false } elseif ($status) { [string]$status.controller_state -eq 'codex_restart_required' } else { $false }
    LastError = if ($runtimePathRedirected) { 'runtime_path_redirected' } elseif ($status) { $status.last_error } else { $null }
    LogPath = Join-Path $installDir 'logs\daemon.log'
}
