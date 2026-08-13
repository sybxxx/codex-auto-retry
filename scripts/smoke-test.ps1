[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'

$startupResult = & powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File (Join-Path $PSScriptRoot 'startup-fail-open-smoke-test.ps1')
if ($LASTEXITCODE -ne 0) { throw 'Startup fail-open smoke test failed.' }

$sharedResult = & powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File (Join-Path $PSScriptRoot 'shared-app-server-smoke-test.ps1')
if ($LASTEXITCODE -ne 0) { throw 'Shared app-server recovery smoke test failed.' }

$environmentResult = & powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File (Join-Path $PSScriptRoot 'environment-smoke-test.ps1')
if ($LASTEXITCODE -ne 0) { throw 'Environment ownership smoke test failed.' }

$safeDisableResult = & powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File (Join-Path $PSScriptRoot 'safe-disable-smoke-test.ps1')
if ($LASTEXITCODE -ne 0) { throw 'Safe-disable smoke test failed.' }

$supervisorResult = & powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File (Join-Path $PSScriptRoot 'supervisor-smoke-test.ps1')
if ($LASTEXITCODE -ne 0) { throw 'Supervisor smoke test failed.' }

[pscustomobject]@{
    Status = 'passed'
    StartupFailOpen = ($startupResult -join [Environment]::NewLine)
    SharedAppServer = ($sharedResult -join [Environment]::NewLine)
    EnvironmentOwnership = ($environmentResult -join [Environment]::NewLine)
    SafeDisable = ($safeDisableResult -join [Environment]::NewLine)
    Supervisor = ($supervisorResult -join [Environment]::NewLine)
    RealProviderUsed = $false
}
