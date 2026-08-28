[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$safeDisable = Join-Path $PSScriptRoot 'safe-disable.ps1'
if (-not (Test-Path -LiteralPath $safeDisable -PathType Leaf)) { throw 'safe-disable.ps1 is missing.' }
$source = Get-Content -Raw -Encoding UTF8 -LiteralPath $safeDisable
if (-not $source.Contains('Restore-CodexAutoRetrySharedEnvironment') -or
    -not $source.Contains('Disable-CodexAutoRetrySharedMode') -or
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
    $runKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'
    $beforeRun = Get-ItemProperty -Path $runKey -Name CodexAutoRetry -ErrorAction SilentlyContinue
    $configPath = Join-Path $testRoot 'config.json'
    $statePath = Join-Path $testRoot 'shared-server.json'
    [System.IO.File]::WriteAllText($configPath, (@{ shared_app_server_enabled = $true } | ConvertTo-Json), [System.Text.UTF8Encoding]::new($false))
    $state = @{ pid = 4000000; endpoint = 'ws://127.0.0.1:49621'; owner = 'codex-auto-retry'; version = '0.7.6'; executable = 'C:\Windows\System32\cmd.exe' }
    [System.IO.File]::WriteAllText($statePath, ($state | ConvertTo-Json), [System.Text.UTF8Encoding]::new($false))
    & powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $safeDisable -DataDir $testRoot | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'safe-disable smoke invocation failed.' }
    if ([Environment]::GetEnvironmentVariable('CODEX_API_KEY', 'User') -ne $beforeKey -or
        [Environment]::GetEnvironmentVariable('CODEX_APP_SERVER_WS_URL', 'User') -ne $beforeEndpoint) {
        throw 'safe-disable changed a user-owned environment value.'
    }
    $afterConfig = Get-Content -Raw -Encoding UTF8 -LiteralPath $configPath | ConvertFrom-Json
    if ([bool]$afterConfig.shared_app_server_enabled -or (Test-Path -LiteralPath $statePath)) {
        throw 'safe-disable did not close shared mode or remove stale owned state.'
    }

    # A damaged config must not prevent the break-glass path from removing
    # the startup route and restoring the official backend.
    $corruptRoot = Join-Path $testRoot 'corrupt-config'
    New-Item -ItemType Directory -Force -Path $corruptRoot | Out-Null
    $corruptConfigPath = Join-Path $corruptRoot 'config.json'
    $corruptConfig = '{ this is not valid json'
    [System.IO.File]::WriteAllText($corruptConfigPath, $corruptConfig, [System.Text.UTF8Encoding]::new($false))
    & powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $safeDisable -DataDir $corruptRoot | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'safe-disable failed when config.json was corrupt.' }
    if ((Get-Content -Raw -Encoding UTF8 -LiteralPath $corruptConfigPath) -ne $corruptConfig) {
        throw 'safe-disable replaced a corrupt config instead of preserving it.'
    }
    [pscustomobject]@{ Status = 'passed'; Parser = 'passed'; UserValuesPreserved = $true; SharedModeDisabled = $true; StaleStateRemoved = $true; TaskDataDeleted = $false }
}
finally {
    if ($null -eq $beforeRun) {
        Remove-ItemProperty -Path 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run' -Name CodexAutoRetry -ErrorAction SilentlyContinue
    }
    else {
        New-Item -Path 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run' -Force | Out-Null
        Set-ItemProperty -Path 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run' -Name CodexAutoRetry -Value ([string]$beforeRun.CodexAutoRetry)
    }
    if (Test-Path -LiteralPath $testRoot -PathType Container) {
        Remove-Item -LiteralPath $testRoot -Recurse -Force -ErrorAction SilentlyContinue
    }
}
