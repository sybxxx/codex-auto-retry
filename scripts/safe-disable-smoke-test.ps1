[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$safeDisable = Join-Path $PSScriptRoot 'safe-disable.ps1'
if (-not (Test-Path -LiteralPath $safeDisable -PathType Leaf)) { throw 'safe-disable.ps1 is missing.' }
$source = Get-Content -Raw -Encoding UTF8 -LiteralPath $safeDisable
if (-not $source.Contains('Restore-CodexAutoRetrySharedEnvironment') -or
    -not $source.Contains('CODEX_API_KEY') -or -not $source.Contains('DataDeleted = $false')) {
    throw 'safe-disable.ps1 is missing its safety boundaries.'
}
if ($source -match '(?i)(Remove-Item|SetEnvironmentVariable)\s+[^\r\n]*CODEX_API_KEY' -or
    $source -match '(?i)Remove-Item[^\r\n]*(state\.json|control\.json|logs)') {
    throw 'safe-disable.ps1 contains a forbidden credential or task-data mutation.'
}
$tokens = $null
$errors = $null
[void][System.Management.Automation.Language.Parser]::ParseFile($safeDisable, [ref]$tokens, [ref]$errors)
if ($errors.Count -gt 0) { throw "PowerShell parse error: $($errors[0].Message)" }

$testRoot = Join-Path $env:TEMP ('codex-auto-retry-safe-disable-' + [guid]::NewGuid().ToString('N'))
try {
    New-Item -ItemType Directory -Force -Path $testRoot | Out-Null
    $beforeKey = [Environment]::GetEnvironmentVariable('CODEX_API_KEY', 'User')
    $beforeEndpoint = [Environment]::GetEnvironmentVariable('CODEX_APP_SERVER_WS_URL', 'User')
    & powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $safeDisable -DataDir $testRoot | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'safe-disable smoke invocation failed.' }
    if ([Environment]::GetEnvironmentVariable('CODEX_API_KEY', 'User') -ne $beforeKey -or
        [Environment]::GetEnvironmentVariable('CODEX_APP_SERVER_WS_URL', 'User') -ne $beforeEndpoint) {
        throw 'safe-disable changed a user-owned environment value.'
    }
    [pscustomobject]@{ Status = 'passed'; Parser = 'passed'; UserValuesPreserved = $true; TaskDataDeleted = $false }
}
finally {
    if (Test-Path -LiteralPath $testRoot -PathType Container) {
        Remove-Item -LiteralPath $testRoot -Recurse -Force -ErrorAction SilentlyContinue
    }
}
