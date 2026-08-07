[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'environment.ps1')

$testRoot = Join-Path $env:TEMP ('codex-auto-retry-environment-' + [guid]::NewGuid().ToString('N'))
$dataDir = Join-Path $testRoot 'data'
$conflictDir = Join-Path $testRoot 'conflict'
$configPath = Join-Path $dataDir 'config.json'
$name = 'CODEX_AUTO_RETRY_ENV_TEST_' + [guid]::NewGuid().ToString('N')
$apiKeyBefore = [Environment]::GetEnvironmentVariable('CODEX_API_KEY', 'User')
try {
    New-Item -ItemType Directory -Force -Path $dataDir, $conflictDir | Out-Null
    [System.IO.File]::WriteAllText(
        $configPath,
        '{"shared_app_server_port":51234}',
        [System.Text.UTF8Encoding]::new($false)
    )
    [Environment]::SetEnvironmentVariable($name, $null, 'User')
    $first = Set-CodexAutoRetrySharedEnvironment -DataDir $dataDir -ConfigPath $configPath -EnvironmentName $name -SkipBroadcast
    if ($first.Value -ne 'ws://127.0.0.1:51234' -or
        [Environment]::GetEnvironmentVariable($name, 'User') -ne $first.Value) {
        throw 'The shared app-server environment value was not installed.'
    }
    $backup = Get-Content -Raw -Encoding UTF8 -LiteralPath (Join-Path $dataDir 'environment-backup.json') | ConvertFrom-Json
    if ([bool]$backup.previous_present -or [string]$backup.previous_value -ne '') {
        throw 'The prior user environment value was not backed up.'
    }
    $second = Set-CodexAutoRetrySharedEnvironment -DataDir $dataDir -ConfigPath $configPath -EnvironmentName $name -SkipBroadcast
    if ($second.Changed) { throw 'An idempotent environment update was reported as changed.' }
    [System.IO.File]::WriteAllText(
        $configPath,
        '{"shared_app_server_port":51235}',
        [System.Text.UTF8Encoding]::new($false)
    )
    $third = Set-CodexAutoRetrySharedEnvironment -DataDir $dataDir -ConfigPath $configPath -EnvironmentName $name -SkipBroadcast
    if (-not $third.Changed -or $third.Value -ne 'ws://127.0.0.1:51235') {
        throw 'An owned app-server endpoint could not be updated safely.'
    }
    $restored = Restore-CodexAutoRetrySharedEnvironment -DataDir $dataDir -EnvironmentName $name -SkipBroadcast
    if (-not $restored.Restored -or $restored.ChangedByUser -or
        $null -ne [Environment]::GetEnvironmentVariable($name, 'User')) {
        throw 'The previous environment value was not restored.'
    }

    [Environment]::SetEnvironmentVariable($name, 'ws://127.0.0.1:59999', 'User')
    $conflictCaught = $false
    try {
        $null = Set-CodexAutoRetrySharedEnvironment -DataDir $conflictDir -ConfigPath $configPath -EnvironmentName $name -SkipBroadcast
    }
    catch { $conflictCaught = $true }
    if (-not $conflictCaught -or [Environment]::GetEnvironmentVariable($name, 'User') -ne 'ws://127.0.0.1:59999') {
        throw 'A different existing environment value was overwritten.'
    }

    [Environment]::SetEnvironmentVariable($name, $null, 'User')
    $invalidDataDir = Join-Path $testRoot 'not-a-directory'
    [System.IO.File]::WriteAllText($invalidDataDir, 'x', [System.Text.Encoding]::ASCII)
    $writeFailureCaught = $false
    try {
        $null = Set-CodexAutoRetrySharedEnvironment -DataDir $invalidDataDir -ConfigPath $configPath -EnvironmentName $name -SkipBroadcast
    }
    catch { $writeFailureCaught = $true }
    if (-not $writeFailureCaught -or $null -ne [Environment]::GetEnvironmentVariable($name, 'User')) {
        throw 'A failed environment installation did not roll back its user value.'
    }

    [pscustomobject]@{
        Status = 'passed'
        PreviousValueBackedUp = $true
        IdempotentUpdate = $true
        OwnedEndpointUpdated = $true
        PreviousValueRestored = $true
        ConflictingValuePreserved = $true
        FailedInstallRolledBack = $true
        ApiKeyUntouched = ([Environment]::GetEnvironmentVariable('CODEX_API_KEY', 'User') -eq $apiKeyBefore)
    }
}
finally {
    [Environment]::SetEnvironmentVariable($name, $null, 'User')
    Remove-Item -Path "Env:$name" -ErrorAction SilentlyContinue
    if (Test-Path -LiteralPath $testRoot -PathType Container) {
        $resolved = [System.IO.Path]::GetFullPath($testRoot)
        $tempPrefix = [System.IO.Path]::GetFullPath($env:TEMP).TrimEnd('\') + '\'
        if ($resolved.StartsWith($tempPrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
            Remove-Item -LiteralPath $resolved -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
    if ([Environment]::GetEnvironmentVariable('CODEX_API_KEY', 'User') -ne $apiKeyBefore) {
        throw 'Environment ownership smoke test changed CODEX_API_KEY.'
    }
}
