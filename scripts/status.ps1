[CmdletBinding()]
param()

$installDir = Join-Path $env:LOCALAPPDATA 'CodexAutoRetry'
$watchdogTarget = Join-Path $installDir 'codex-auto-retry.exe'
$mcpTarget = Join-Path $installDir 'codex-auto-retry-mcp.exe'
$statusPath = Join-Path $installDir 'status.json'
$process = Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
    Where-Object { $_.ExecutablePath -and [string]::Equals($_.ExecutablePath, $watchdogTarget, [System.StringComparison]::OrdinalIgnoreCase) } |
    Select-Object -First 1
$mcpProcesses = @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
    Where-Object { $_.ExecutablePath -and [string]::Equals($_.ExecutablePath, $mcpTarget, [System.StringComparison]::OrdinalIgnoreCase) })
$status = $null
if (Test-Path -LiteralPath $statusPath) {
    try { $status = Get-Content -Raw -Encoding UTF8 -LiteralPath $statusPath | ConvertFrom-Json } catch { $status = $null }
}
$staleAfter = [TimeSpan]::FromSeconds(15)
$heartbeatFresh = $false
if ($status -and $status.last_scan_at) {
    try { $heartbeatFresh = ([DateTimeOffset]::UtcNow - [DateTimeOffset]::Parse([string]$status.last_scan_at)) -le $staleAfter } catch { $heartbeatFresh = $false }
}
$runtimeRunning = $null -ne $process -and [bool]$status.running -and $heartbeatFresh
$pendingRetries = if ($runtimeRunning -and $status) { $status.pending_retries } else { 0 }
$activeRetries = if ($runtimeRunning -and $status) { $status.active_retries } else { 0 }

[pscustomobject]@{
    Installed = Test-Path -LiteralPath $watchdogTarget
    MCPServerInstalled = Test-Path -LiteralPath $mcpTarget
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
    ControllerState = if ($status) { [string]$status.controller_state } else { $null }
    SharedAppServerEnabled = if ($status -and $status.PSObject.Properties['shared_app_server_enabled']) { [bool]$status.shared_app_server_enabled } else { $false }
    CodexRestartRequired = if ($status) { [string]$status.controller_state -eq 'codex_restart_required' } else { $false }
    LastError = if ($status) { $status.last_error } else { $null }
    LogPath = Join-Path $installDir 'logs\daemon.log'
}
