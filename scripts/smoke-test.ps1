[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'

$sharedResult = & powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File (Join-Path $PSScriptRoot 'shared-app-server-smoke-test.ps1')
if ($LASTEXITCODE -ne 0) { throw 'Shared app-server recovery smoke test failed.' }

$environmentResult = & powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File (Join-Path $PSScriptRoot 'environment-smoke-test.ps1')
if ($LASTEXITCODE -ne 0) { throw 'Environment ownership smoke test failed.' }

[pscustomobject]@{
    Status = 'passed'
    SharedAppServer = ($sharedResult -join [Environment]::NewLine)
    EnvironmentOwnership = ($environmentResult -join [Environment]::NewLine)
    RealProviderUsed = $false
}
