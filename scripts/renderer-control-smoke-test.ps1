[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$sourceDir = Join-Path $PSScriptRoot 'source'
$previous = $env:CODEX_AUTO_RETRY_LIVE_PROBE
try {
    $env:CODEX_AUTO_RETRY_LIVE_PROBE = '1'
    Push-Location $sourceDir
    try {
        & go test -run '^TestLiveRendererControllerProbe$' -count=1
        if ($LASTEXITCODE -ne 0) {
            throw 'Read-only Codex background-channel probe failed.'
        }
    }
    finally {
        Pop-Location
    }
    [pscustomobject]@{
        Status = 'passed'
        Mode = 'read-only'
        BackgroundChannelFound = $true
        AppStateSnapshotRead = $true
        LoadedThreadListRead = $true
        TaskNavigationUsed = $false
        UIChanged = $false
    }
}
finally {
    $env:CODEX_AUTO_RETRY_LIVE_PROBE = $previous
}
