[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$watchdogSource = Join-Path $PSScriptRoot 'bin\codex-auto-retry.exe'
$mcpSource = Join-Path $PSScriptRoot 'bin\codex-auto-retry-mcp.exe'
$installDir = Join-Path $env:LOCALAPPDATA 'CodexAutoRetry'
$watchdogTarget = Join-Path $installDir 'codex-auto-retry.exe'
$mcpTarget = Join-Path $installDir 'codex-auto-retry-mcp.exe'
$stopSignal = Join-Path $installDir 'stop.signal'
$statusPath = Join-Path $installDir 'status.json'
$runKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'
$runName = 'CodexAutoRetry'

if (-not (Test-Path -LiteralPath $watchdogSource)) {
    throw "Built watchdog not found: $watchdogSource"
}
if (-not (Test-Path -LiteralPath $mcpSource)) {
    throw "Built MCP server not found: $mcpSource"
}

New-Item -ItemType Directory -Force -Path $installDir | Out-Null

$existing = Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
    Where-Object { $_.ExecutablePath -and [string]::Equals($_.ExecutablePath, $watchdogTarget, [System.StringComparison]::OrdinalIgnoreCase) }
if ($existing) {
    New-Item -ItemType File -Force -Path $stopSignal | Out-Null
    $deadline = (Get-Date).AddSeconds(12)
    do {
        Start-Sleep -Milliseconds 250
        $existing = Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
            Where-Object { $_.ExecutablePath -and [string]::Equals($_.ExecutablePath, $watchdogTarget, [System.StringComparison]::OrdinalIgnoreCase) }
    } while ($existing -and (Get-Date) -lt $deadline)
    if ($existing) {
        $existing | ForEach-Object { Stop-Process -Id $_.ProcessId -Force }
    }
}

$mcpProcesses = Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
    Where-Object { $_.ExecutablePath -and [string]::Equals($_.ExecutablePath, $mcpTarget, [System.StringComparison]::OrdinalIgnoreCase) }
if ($mcpProcesses) {
    $mcpProcesses | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
    Start-Sleep -Milliseconds 300
}

Copy-Item -LiteralPath $watchdogSource -Destination $watchdogTarget -Force
Copy-Item -LiteralPath $mcpSource -Destination $mcpTarget -Force
New-Item -Path $runKey -Force | Out-Null
Set-ItemProperty -Path $runKey -Name $runName -Value ('"{0}" run' -f $watchdogTarget)
Remove-Item -LiteralPath $stopSignal -Force -ErrorAction SilentlyContinue
$startedProcess = Start-Process -FilePath $watchdogTarget -ArgumentList 'run' -WindowStyle Hidden -PassThru

$deadline = (Get-Date).AddSeconds(15)
$status = $null
do {
    Start-Sleep -Milliseconds 300
    if (Test-Path -LiteralPath $statusPath) {
        try { $status = Get-Content -Raw -Encoding UTF8 -LiteralPath $statusPath | ConvertFrom-Json } catch { $status = $null }
    }
} while ((-not $status -or -not $status.running -or $status.pid -ne $startedProcess.Id) -and (Get-Date) -lt $deadline)

if (-not $status -or -not $status.running -or $status.pid -ne $startedProcess.Id) {
    throw "Watchdog did not publish a running heartbeat. Check $installDir\logs\daemon.log"
}

[pscustomobject]@{
    Installed = $true
    Running = $status.running
    Version = $status.version
    PID = $status.pid
    Paused = [bool]$status.paused
    MCPServerInstalled = Test-Path -LiteralPath $mcpTarget
    InstallDirectory = $installDir
    Startup = 'Current user sign-in'
}
