[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$statusScript = Join-Path $PSScriptRoot 'status.ps1'
if (-not (Test-Path -LiteralPath $statusScript -PathType Leaf)) { throw 'status.ps1 is missing.' }

$status = & $statusScript | ConvertTo-Json -Depth 5 | ConvertFrom-Json
if (-not $status.Installed -or -not $status.ProcessRunning -or $status.HeartbeatStale) {
    throw "The installed watchdog heartbeat was not recognized as fresh: $($status | ConvertTo-Json -Compress)"
}

[pscustomobject]@{
    Status = 'passed'
    ProcessRunning = $true
    HeartbeatFresh = $true
    Version = [string]$status.Version
    StartupMode = [string]$status.StartupMode
    SharedEndpointConfigured = [bool]$status.SharedEndpointConfigured
}
