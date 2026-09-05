[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'environment.ps1')
$installSource = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'install.ps1')
$tokens = $null
$parseErrors = $null
$ast = [System.Management.Automation.Language.Parser]::ParseInput($installSource, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count -gt 0) { throw 'Installer parse failed.' }
# Static invariants cover success, catch rollback, and interrupted-journal paths.
if ($installSource -match 'Set-CodexAutoRetrySharedEnvironment|\[Environment\]::SetEnvironmentVariable') {
    throw 'Installer contains a persistent environment publisher.'
}
$functions = @('Test-SafeInstallTransactionRoot', 'Recover-IncompleteInstall', 'Restore-SafeInstallRouting')
foreach ($name in $functions) {
    $definition = $ast.Find({ param($node) $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq $name }.GetNewClosure(), $true)
    if (-not $definition) { throw "Missing installer function: $name" }
    . ([scriptblock]::Create($definition.Extent.Text))
}
$topLevel = $ast.EndBlock.Statements
$recoveryCall = @($topLevel | Where-Object { $_.Extent.Text -eq 'Recover-IncompleteInstall' })[0]
$desktopGate = @($topLevel | Where-Object { $_.Extent.Text -like 'if (Test-CodexDesktopRunning)*' })[0]
if (-not $desktopGate -or $desktopGate.Extent.StartOffset -gt $recoveryCall.Extent.StartOffset) {
    throw 'Desktop must be closed before journal recovery or installation writes.'
}
$transaction = @($topLevel | Where-Object { $_ -is [System.Management.Automation.Language.TryStatementAst] })[-1]
$migration = @($transaction.Body.Statements | Where-Object { $_.Extent.Text -like '$environmentMigration = Restore-CodexAutoRetrySharedEnvironment*' })
if ($migration.Count -ne 1) { throw 'Shared routing migration is not unconditional in the install transaction.' }
if ($transaction.CatchClauses[0].Extent.Text -notmatch 'Restore-SafeInstallRouting') { throw 'Rollback is missing safe routing migration.' }
$deploySource = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot '..\release\windows\deploy.ps1')
$deployAst = [System.Management.Automation.Language.Parser]::ParseInput($deploySource, [ref]$tokens, [ref]$parseErrors)
$rollbackInstall = $deployAst.Find({ param($node)
    $node -is [System.Management.Automation.Language.CatchClauseAst] -and $node.Extent.Text -match 'Install-Runtime -PluginPath'
}, $true)
if ($rollbackInstall -or $deploySource -notmatch 'Disable-CodexAutoRetryLegacyRouting -DataDir \$runtimePath') {
    throw 'Release rollback can invoke an unsafe previous installer.'
}
if ($deploySource -match '\(Test-SharedBackendInUse -RuntimePath \$runtimePath\) -and \(Test-CodexDesktopRunning\)') {
    throw 'Release update still allows modification while official Desktop is running.'
}
$recoveryBlock = $deployAst.Find({ param($node)
    $node -is [System.Management.Automation.Language.StatementBlockAst] -and
    $node.Extent.Text -match 'Write-Step "Recovering interrupted upgrade' -and
    $node.Extent.Text -notmatch '\$unfinished ='
}, $true)
if (-not $recoveryBlock -or $recoveryBlock.Extent.Text -notmatch 'Stop-RuntimeForUpgrade[\s\S]+Disable-CodexAutoRetryLegacyRouting[\s\S]+Restore-IncompleteUpgrade') {
    throw 'Interrupted upgrade must stop a legacy publisher before retiring routing and restoring source.'
}

$testRoot = Join-Path $env:TEMP ('codex-auto-retry-install-routing-' + [guid]::NewGuid().ToString('N'))
$environmentName = 'CODEX_AUTO_RETRY_ENV_TEST_' + [guid]::NewGuid().ToString('N')
$productionBefore = [Environment]::GetEnvironmentVariable('CODEX_APP_SERVER_WS_URL', 'User')
$restoreEnvironment = ${function:Restore-CodexAutoRetrySharedEnvironment}
function Restore-CodexAutoRetrySharedEnvironment {
    param([string]$DataDir, [string[]]$LegacyOwnedEndpoint)
    & $restoreEnvironment -DataDir $DataDir -EnvironmentName $environmentName -LegacyOwnedEndpoint $LegacyOwnedEndpoint -SkipBroadcast
}
function Test-CodexDesktopRunning { return $script:desktopRunning }
function Stop-InstalledRuntime { $script:stopCalls++ }
function Get-RunValue { return 'foreign-startup-command' }
function Test-OwnedStartupValue { param($Value) return $false }
function Start-Process { throw 'Rollback must never restart the previous runtime.' }
$script:desktopRunning = $false
$script:stopCalls = 0
try {
    New-Item -ItemType Directory -Path $testRoot | Out-Null
    foreach ($scenario in @('owned', 'poisoned', 'foreign')) {
        $installDir = Join-Path $testRoot $scenario
        $transactionRoot = Join-Path $testRoot ($scenario + '-transaction')
        $backupRoot = Join-Path $transactionRoot 'previous'
        New-Item -ItemType Directory -Path $installDir, $backupRoot | Out-Null
        $configPath = Join-Path $installDir 'config.json'
        $installJournalPath = Join-Path $installDir 'install-journal.json'
        $watchdogTarget = Join-Path $installDir 'codex-auto-retry.exe'
        $endpoint = 'ws://127.0.0.1:49622'
        Write-CodexAutoRetryJsonAtomic -Path (Join-Path $backupRoot 'config.json') -Value ([pscustomobject]@{
            shared_app_server_enabled = $true; retry_prompt = 'retained'; max_recovery_attempts = 7
        })
        Write-CodexAutoRetryJsonAtomic -Path (Join-Path $backupRoot 'environment-backup.json') -Value ([pscustomobject]@{
            schema_version = 1; name = $environmentName
            previous_present = $scenario -eq 'poisoned'; previous_value = $endpoint; installed_value = $endpoint
        })
        Write-CodexAutoRetryJsonAtomic -Path (Join-Path $backupRoot 'shared-server.json') -Value ([pscustomobject]@{
            owner = 'codex-auto-retry'; endpoint = $endpoint; pid = 4000000
        })
        Write-CodexAutoRetryJsonAtomic -Path $installJournalPath -Value ([pscustomobject]@{
            schema_version = 1; phase = 'runtime_stopped'; transaction_root = $transactionRoot
            shared_enabled = $true; watchdog_was_running = $true
            environment_present = $true; environment_value = $endpoint
            files = [pscustomobject]@{ 'config.json' = $true; 'environment-backup.json' = $true; 'shared-server.json' = $true }
        })
        $initial = if ($scenario -eq 'foreign') { 'ws://127.0.0.1:59999' } else { $endpoint }
        [Environment]::SetEnvironmentVariable($environmentName, $initial, 'User')
        $script:desktopRunning = $true
        $blocked = $false
        try { Recover-IncompleteInstall } catch { $blocked = $_.Exception.Message -like '*Codex Desktop to be closed*' }
        if (-not $blocked -or (Test-Path -LiteralPath $configPath)) { throw 'Live Desktop did not block recovery before writes.' }
        $script:desktopRunning = $false
        Recover-IncompleteInstall
        $config = Get-Content -Raw -LiteralPath $configPath | ConvertFrom-Json
        if ($config.shared_app_server_enabled -or $config.retry_prompt -ne 'retained' -or $config.max_recovery_attempts -ne 7) {
            throw 'Rollback did not disable unsafe shared startup while preserving unrelated settings.'
        }
        $actual = [Environment]::GetEnvironmentVariable($environmentName, 'User')
        if ($scenario -eq 'foreign') {
            if ($actual -ne $initial) { throw 'Rollback overwrote a foreign user endpoint.' }
        } elseif ($null -ne $actual) { throw 'Rollback resurrected an owned endpoint.' }
        # Repeated journal recovery is a no-op.
        $beforeStops = $script:stopCalls
        Recover-IncompleteInstall
        if ($script:stopCalls -ne $beforeStops) { throw 'Completed rollback was repeated.' }
    }
    [pscustomobject]@{
        Status = 'passed'; NoPersistentPublisher = $true; UnconditionalMigration = $true
        LiveDesktopBlocked = $true; OldWorkerNotRestarted = $true; OwnedEndpointNotRepublished = $true
        ForeignEndpointPreserved = $true; UnrelatedSettingsPreserved = $true; RepeatedRecoveryNoOp = $true
    }
}
finally {
    Remove-CodexAutoRetryUserEnvironmentValue -Name $environmentName
    $resolved = [System.IO.Path]::GetFullPath($testRoot)
    $tempPrefix = [System.IO.Path]::GetFullPath($env:TEMP).TrimEnd('\') + '\'
    if ($resolved.StartsWith($tempPrefix, [System.StringComparison]::OrdinalIgnoreCase) -and (Test-Path -LiteralPath $resolved)) {
        Remove-Item -LiteralPath $resolved -Recurse -Force
    }
    if ([Environment]::GetEnvironmentVariable('CODEX_APP_SERVER_WS_URL', 'User') -ne $productionBefore) {
        throw 'Isolated installer test changed production Desktop routing.'
    }
}
