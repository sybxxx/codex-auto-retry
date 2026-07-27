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

[pscustomobject]@{
    Installed = Test-Path -LiteralPath $watchdogTarget
    MCPServerInstalled = Test-Path -LiteralPath $mcpTarget
    MCPServerProcesses = $mcpProcesses.Count
    ProcessRunning = $null -ne $process
    PID = if ($process) { $process.ProcessId } else { $null }
    Version = if ($status) { $status.version } else { $null }
    LastScanAt = if ($status) { $status.last_scan_at } else { $null }
    WatchedRoots = if ($status) { $status.watched_roots } else { 0 }
    PendingRetries = if ($status) { $status.pending_retries } else { 0 }
    ActiveRetries = if ($status) { $status.active_retries } else { 0 }
    Paused = if ($status) { [bool]$status.paused } else { $false }
    LastError = if ($status) { $status.last_error } else { $null }
    LogPath = Join-Path $installDir 'logs\daemon.log'
}
