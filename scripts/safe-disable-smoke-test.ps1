[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$environmentScript = Join-Path $PSScriptRoot 'environment.ps1'
if (-not (Test-Path -LiteralPath $environmentScript -PathType Leaf)) { throw 'environment.ps1 is missing.' }
. $environmentScript
$safeDisable = Join-Path $PSScriptRoot 'safe-disable.ps1'
if (-not (Test-Path -LiteralPath $safeDisable -PathType Leaf)) { throw 'safe-disable.ps1 is missing.' }
$source = Get-Content -Raw -Encoding UTF8 -LiteralPath $safeDisable
if (-not $source.Contains('Restore-CodexAutoRetrySharedEnvironment') -or
    -not $source.Contains('Disable-CodexAutoRetrySharedMode') -or
    -not $source.Contains('CODEX_API_KEY') -or -not $source.Contains('DataDeleted = $false')) {
    throw 'safe-disable.ps1 is missing its safety boundaries.'
}
if (-not $source.Contains('[string]$RunName') -or
    -not $source.Contains('final registry state was not fully removed')) {
    throw 'safe-disable.ps1 does not expose isolated startup cleanup with a final-state check.'
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
    $runName = 'CodexAutoRetrySafeDisableSmoke_' + [guid]::NewGuid().ToString('N')
    $beforeRun = Get-ItemProperty -Path $runKey -Name $runName -ErrorAction SilentlyContinue
    . (Join-Path $PSScriptRoot 'startup-approval.ps1')
    $beforeApproval = Get-CodexAutoRetryStartupApproval -RunName $runName
    $configPath = Join-Path $testRoot 'config.json'
    $statePath = Join-Path $testRoot 'shared-server.json'
    [System.IO.File]::WriteAllText($configPath, (@{ shared_app_server_enabled = $true } | ConvertTo-Json), [System.Text.UTF8Encoding]::new($false))
    $state = @{ pid = 4000000; endpoint = 'ws://127.0.0.1:49621'; owner = 'codex-auto-retry'; version = '0.7.6'; executable = 'C:\Windows\System32\cmd.exe' }
    [System.IO.File]::WriteAllText($statePath, ($state | ConvertTo-Json), [System.Text.UTF8Encoding]::new($false))
    Set-ItemProperty -Path $runKey -Name $runName -Value ('"' + (Join-Path $testRoot 'codex-auto-retry.exe') + '" supervise')
    Restore-CodexAutoRetryStartupApproval -RunName $runName -Bytes ([byte[]](3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0))
    & powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $safeDisable -DataDir $testRoot -RunName $runName | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'safe-disable smoke invocation failed.' }
    if ([Environment]::GetEnvironmentVariable('CODEX_API_KEY', 'User') -ne $beforeKey -or
        -not [string]::Equals([string][Environment]::GetEnvironmentVariable('CODEX_APP_SERVER_WS_URL', 'User'), [string]$beforeEndpoint, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw 'safe-disable changed a user-owned environment value.'
    }
    $afterConfig = Get-Content -Raw -Encoding UTF8 -LiteralPath $configPath | ConvertFrom-Json
    if ([bool]$afterConfig.shared_app_server_enabled -or (Test-Path -LiteralPath $statePath)) {
        throw 'safe-disable did not close shared mode or remove stale owned state.'
    }
    if ((Get-ItemProperty -Path $runKey -Name $runName -ErrorAction SilentlyContinue) -or
        (Get-CodexAutoRetryStartupApproval -RunName $runName).Present) {
        throw 'safe-disable did not remove the owned startup entry and its disabled approval marker.'
    }

    # A damaged config must not prevent the break-glass path from removing
    # the startup route and restoring the official backend.
    $corruptRoot = Join-Path $testRoot 'corrupt-config'
    New-Item -ItemType Directory -Force -Path $corruptRoot | Out-Null
    $corruptConfigPath = Join-Path $corruptRoot 'config.json'
    $corruptConfig = '{ this is not valid json'
    [System.IO.File]::WriteAllText($corruptConfigPath, $corruptConfig, [System.Text.UTF8Encoding]::new($false))
    & powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $safeDisable -DataDir $corruptRoot -RunName $runName | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'safe-disable failed when config.json was corrupt.' }
    if ((Get-Content -Raw -Encoding UTF8 -LiteralPath $corruptConfigPath) -ne $corruptConfig) {
        throw 'safe-disable replaced a corrupt config instead of preserving it.'
    }
    [pscustomobject]@{ Status = 'passed'; Parser = 'passed'; UserValuesPreserved = $true; SharedModeDisabled = $true; StaleStateRemoved = $true; TaskDataDeleted = $false }
}
finally {
    if ($null -eq $beforeEndpoint) {
        Remove-CodexAutoRetryUserEnvironmentValue -Name 'CODEX_APP_SERVER_WS_URL'
    }
    else {
        [Environment]::SetEnvironmentVariable('CODEX_APP_SERVER_WS_URL', $beforeEndpoint, 'User')
    }
    if ($null -eq $beforeRun) {
        Remove-ItemProperty -Path $runKey -Name $runName -ErrorAction SilentlyContinue
    }
    else {
        $runRegistryKey = Open-CodexAutoRetryRunKey -Writable $true
        if ($null -eq $runRegistryKey) { throw 'The current-user startup registry key could not be opened while restoring the smoke-test value.' }
        try { $runRegistryKey.SetValue($runName, [string]$beforeRun.$runName, [Microsoft.Win32.RegistryValueKind]::String) }
        finally { $runRegistryKey.Close() }
    }
    if ($beforeApproval.Present) { Restore-CodexAutoRetryStartupApproval -RunName $runName -Bytes ([byte[]]$beforeApproval.Bytes) }
    else { $null = Remove-CodexAutoRetryStartupApproval -RunName $runName }
    if (Test-Path -LiteralPath $testRoot -PathType Container) {
        Remove-Item -LiteralPath $testRoot -Recurse -Force -ErrorAction SilentlyContinue
    }
}
