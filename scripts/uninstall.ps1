[CmdletBinding()]
param(
    [switch]$KeepData
)

$ErrorActionPreference = 'Stop'
$installDir = Join-Path $env:LOCALAPPDATA 'CodexAutoRetry'
$watchdogTarget = Join-Path $installDir 'codex-auto-retry.exe'
$mcpTarget = Join-Path $installDir 'codex-auto-retry-mcp.exe'
$stopSignal = Join-Path $installDir 'stop.signal'
$runKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'

if (Test-Path -LiteralPath $installDir) {
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

Remove-ItemProperty -Path $runKey -Name 'CodexAutoRetry' -ErrorAction SilentlyContinue
if (Test-Path -LiteralPath $installDir) {
    if ($KeepData) {
        foreach ($runtimeFile in @(
            $watchdogTarget,
            $mcpTarget,
            $stopSignal,
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
}
