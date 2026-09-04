[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$helper = Join-Path $PSScriptRoot 'shared-server-status.ps1'
if (-not (Test-Path -LiteralPath $helper -PathType Leaf)) { throw 'shared-server-status.ps1 is missing.' }
. $helper

$missing = Get-CodexAutoRetrySharedServerStatus -State $null
if ($missing.Status -ne 'missing') { throw "Missing state was not reported as missing: $($missing | ConvertTo-Json -Compress)" }

$invalid = [pscustomobject]@{ owner = 'other-tool'; pid = 42; endpoint = 'ws://127.0.0.1:49621' }
$invalidResult = Get-CodexAutoRetrySharedServerStatus -State $invalid
if ($invalidResult.Status -ne 'invalid') { throw "Unowned state was not rejected: $($invalidResult | ConvertTo-Json -Compress)" }

$executable = Join-Path $env:WINDIR 'System32\cmd.exe'
$state = [pscustomobject]@{
    owner = 'codex-auto-retry'
    pid = 4000000
    endpoint = 'ws://127.0.0.1:49621'
    executable = $executable
    executable_hash = (Get-FileHash -LiteralPath $executable -Algorithm SHA256).Hash
    started_at = [DateTime]::UtcNow.ToString('o')
}
$stale = Get-CodexAutoRetrySharedServerStatus -State $state -ExpectedPort 49621
if ($stale.Status -ne 'stale') { throw "Dead owned process was not reported as stale: $($stale | ConvertTo-Json -Compress)" }

[pscustomobject]@{
    Status = 'passed'
    MissingState = $missing.Status
    UnownedState = $invalidResult.Status
    DeadOwnedProcess = $stale.Status
    PIDAloneIsNotLive = $true
}
