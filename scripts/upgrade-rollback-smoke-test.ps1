[CmdletBinding()]
param()
$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
. (Join-Path $repo 'release\windows\common.ps1')
. (Join-Path $repo 'release\windows\upgrade-runtime.ps1')
. (Join-Path $PSScriptRoot 'startup-approval.ps1')
$testRoot = Join-Path $env:TEMP ('codex-auto-retry-upgrade-test-' + [guid]::NewGuid().ToString('N'))
$tokens = $null; $errors = $null
$ast = [System.Management.Automation.Language.Parser]::ParseFile((Join-Path $repo 'release\windows\deploy.ps1'), [ref]$tokens, [ref]$errors)
if ($errors.Count) { throw 'Deploy parse failed.' }

# Registry, routing and process operations are mocked. Real backup/restore,
# file copying, JSON verification and the production transaction execute below.
function Open-CodexAutoRetryRunKey {
    param($Writable)
    $key = [pscustomobject]@{}
    $key | Add-Member ScriptMethod GetValue { param($name,$default,$options) return $script:runValue }
    $key | Add-Member ScriptMethod SetValue { param($name,$value,$kind) $script:runValue = $value }
    $key | Add-Member ScriptMethod DeleteValue { param($name,$missing) $script:runValue = $null }
    $key | Add-Member ScriptMethod Close { }
    return $key
}
function Get-CodexAutoRetryStartupApproval {
    param($RunName)
    return [pscustomobject]@{ Present=($null -ne $script:approvalBytes); Bytes=$script:approvalBytes }
}
function Restore-CodexAutoRetryStartupApproval { param($RunName,$Bytes) $script:approvalBytes = $Bytes }
function Start-Process { throw 'Unexpected real process launch in rollback test.' }
function Stop-Process { throw 'Unexpected real process termination in rollback test.' }

function Invoke-UpgradeFixture {
    param([string]$Scenario)
    foreach ($definition in $ast.EndBlock.Statements | Where-Object { $_ -is [System.Management.Automation.Language.FunctionDefinitionAst] }) {
        . ([scriptblock]::Create($definition.Extent.Text))
    }
    $scenarioRoot = Join-Path $testRoot $Scenario
    $profileRootPath = Join-Path $scenarioRoot 'profile'
    $localAppDataPath = Join-Path $scenarioRoot 'local'
    $pluginParent = Join-Path $profileRootPath 'plugins'
    $pluginTarget = Join-Path $pluginParent 'codex-auto-retry'
    $runtimePath = Join-Path $localAppDataPath 'CodexAutoRetry'
    $upgradeJournalPath = Join-Path $runtimePath 'upgrade-journal.json'
    $marketplacePath = Join-Path $profileRootPath 'marketplace.json'
    $payloadRoot = Join-Path $scenarioRoot 'payload'
    $cli = 'fixture.exe'; $pluginId = 'codex-auto-retry@personal'
    $upgradeLock = $null
    $SkipRuntimeInstall = $false; $SkipPluginRegistration = $false; $EnableSharedAppServer = $false
    $manifest = [pscustomobject]@{packageVersion='0.7.11';pluginVersion='new'}
    New-Item -ItemType Directory -Path $pluginTarget, $runtimePath, (Join-Path $payloadRoot 'scripts') -Force | Out-Null
    Write-JsonAtomic (Join-Path $pluginTarget '.codex-plugin\plugin.json') ([pscustomobject]@{name='codex-auto-retry';version='old'})
    Write-JsonAtomic (Join-Path $payloadRoot '.codex-plugin\plugin.json') ([pscustomobject]@{name='codex-auto-retry';version='new'})
    Write-JsonAtomic (Join-Path $payloadRoot '.mcp.json') ([pscustomobject]@{mcpServers=@{'codex-auto-retry'=@{command='powershell.exe';args=@()}}})
    Write-JsonAtomic $marketplacePath ([pscustomobject]@{name='personal';plugins=@()})
    $marketplace = Read-OrCreateMarketplace $marketplacePath
    $marketplace = Ensure-MarketplaceEntry $marketplace
    foreach ($file in @('codex-auto-retry.exe','codex-auto-retry-mcp.exe','settings.ps1')) {
        [IO.File]::WriteAllText((Join-Path $runtimePath $file), 'old runtime')
    }
    foreach ($file in @('config.json','state.json','control.json')) {
        [IO.File]::WriteAllText((Join-Path $runtimePath $file), '{"sentinel":"preserve"}')
    }
    [IO.File]::WriteAllText((Join-Path $payloadRoot 'scripts\environment.ps1'), @'
function Disable-CodexAutoRetryLegacyRouting { param($DataDir) $script:routingCalls++ }
function Stop-CodexAutoRetrySharedServerIfUnused { param($DataDir) $script:sharedStopCalls++; return $true }
'@)
    $script:runValue = '"old.exe" run'
    $script:approvalBytes = [byte[]](3,0,0,0,0,0,0,0,0,0,0,0)
    $script:stopCalls = 0; $script:routingCalls = 0; $script:sharedStopCalls = 0
    $script:installCalls = 0; $script:installedVersion = 'old'; $script:postFailureSeen = $false
    if ($Scenario -eq 'fresh-failure') {
        $fullPlugin = Get-FullPath $pluginTarget
        if (-not $fullPlugin.StartsWith((Get-FullPath $scenarioRoot) + '\', [StringComparison]::OrdinalIgnoreCase)) { throw 'Unsafe fixture path.' }
        Remove-Item -LiteralPath $fullPlugin -Recurse -Force
        Remove-Item -LiteralPath $marketplacePath -Force
        foreach ($file in @('codex-auto-retry.exe','codex-auto-retry-mcp.exe','settings.ps1')) { Remove-Item -LiteralPath (Join-Path $runtimePath $file) }
        $script:runValue = $null; $script:approvalBytes = $null
    }
    function Test-CodexDesktopRunning { return $Scenario -eq 'desktop-reopened' -and $script:installCalls -gt 0 }
    function Stop-RuntimeForUpgrade { param($RuntimePath) $script:stopCalls++; return $true }
    function Install-Runtime {
        param($PluginPath,$EnableSharedAppServer)
        $script:installCalls++
        foreach ($file in @('codex-auto-retry.exe','codex-auto-retry-mcp.exe','settings.ps1')) { [IO.File]::WriteAllText((Join-Path $runtimePath $file), 'new runtime') }
        $script:runValue = '"{0}" supervise' -f (Join-Path $runtimePath 'codex-auto-retry.exe')
        $script:approvalBytes = [byte[]](2,0,0,0,0,0,0,0,0,0,0,0)
        if ($Scenario -eq 'foreign-startup') { $script:runValue = 'foreign.exe' }
        if ($Scenario -eq 'foreign-approval') { $script:approvalBytes = [byte[]](7,0,0,0,0,0,0,0,0,0,0,0) }
        if ($Scenario -eq 'corrupt-backup') { [IO.File]::WriteAllText((Join-Path $transactionRoot 'runtime-backup\settings.ps1'), 'corrupt') }
        return 'installed'
    }
    function Invoke-CodexCli {
        param($Path,$Arguments,$TimeoutMilliseconds)
        if ($Arguments[1] -eq 'add') {
            $current = Read-JsonDocument (Join-Path $pluginTarget '.codex-plugin\plugin.json')
            if ($Scenario -eq 'cache-rollback-fails' -and $current.version -eq 'old') { return [pscustomobject]@{ExitCode=9;Failure='';ErrorOutput='';Output=''} }
            $script:installedVersion = $current.version
            return [pscustomobject]@{ExitCode=0;Failure='';ErrorOutput='warning';Output='{}'}
        }
        if ($Arguments[1] -eq 'remove') {
            $script:installedVersion = ''
            return [pscustomobject]@{ExitCode=0;Failure='';ErrorOutput='';Output='{}'}
        }
        if (-not $script:postFailureSeen -and $Scenario -ne 'success-with-warning') {
            $script:postFailureSeen = $true
            return [pscustomobject]@{ExitCode=23;Failure='';ErrorOutput='synthetic verify failure SECRET';Output=''}
        }
        $installed = @()
        if ($script:installedVersion) { $installed = @(@{pluginId=$pluginId;installed=$true;enabled=$true;version=$script:installedVersion}) }
        return [pscustomobject]@{ExitCode=0;Failure='';ErrorOutput='warning';Output=(@{installed=$installed} | ConvertTo-Json -Depth 5 -Compress)}
    }
    $verify = ${function:Verify-Installation}
    function Verify-Installation {
        param($PluginPath,$RuntimePath,$Cli,$PluginId,$ExpectedBaseVersion,$VerifyPlugin,$VerifyRuntime,$ExpectedSharedAppServer)
        # Real files are surrogate text, so only runtime PE/process inspection
        # is excluded. Manifest, direct launcher and CLI verification are real.
        & $verify -PluginPath $PluginPath -RuntimePath $RuntimePath -Cli $Cli -PluginId $PluginId -ExpectedBaseVersion $ExpectedBaseVersion -VerifyPlugin $VerifyPlugin -VerifyRuntime $false -ExpectedSharedAppServer $ExpectedSharedAppServer
    }
    # Execute the real outer install transaction, including its catch/finally.
    $start = $ast.EndBlock.Statements | Where-Object { $_.Extent.Text -like '$transactionRoot = Join-Path*' } | Select-Object -First 1
    $text = $ast.Extent.Text.Substring($start.Extent.StartOffset)
    $failure = ''
    try { . ([scriptblock]::Create($text)) } catch { $failure = $_.Exception.Message }
    if ($Scenario -eq 'success-with-warning') {
        if ($failure -or $script:installedVersion -ne 'new' -or $script:installCalls -ne 1 -or
            (Test-Path -LiteralPath $upgradeJournalPath)) { throw "Successful install with warning was rolled back: $failure" }
        foreach ($file in @('codex-auto-retry.exe','codex-auto-retry-mcp.exe','settings.ps1')) {
            if ([IO.File]::ReadAllText((Join-Path $runtimePath $file)) -ne 'new runtime') { throw 'Successful runtime was replaced by old files.' }
        }
        return
    }
    if ($failure -notmatch 'exit=23' -or $failure -match 'SECRET' -or $script:installCalls -ne 1) { throw "Wrong injected failure: $failure" }
    $retained = $Scenario -in @('corrupt-backup','cache-rollback-fails','desktop-reopened')
    if ((Test-Path -LiteralPath $upgradeJournalPath) -ne $retained) { throw "Wrong recovery journal retention: $Scenario" }
    foreach ($file in @('config.json','state.json','control.json')) {
        if ([IO.File]::ReadAllText((Join-Path $runtimePath $file)) -ne '{"sentinel":"preserve"}') { throw 'Retry data changed in rollback.' }
    }
    if ($Scenario -eq 'desktop-reopened') {
        if ($script:stopCalls -ne 1) { throw 'Rollback stopped a service after Desktop reopened.' }
    } elseif ($Scenario -eq 'fresh-failure') {
        foreach ($file in @('codex-auto-retry.exe','codex-auto-retry-mcp.exe','settings.ps1')) {
            if (Test-Path -LiteralPath (Join-Path $runtimePath $file)) { throw 'Fresh-install runtime remained after rollback.' }
        }
        if ((Test-Path -LiteralPath $pluginTarget) -or (Test-Path -LiteralPath $marketplacePath) -or
            $null -ne $script:runValue -or $null -ne $script:approvalBytes) { throw 'Fresh-install registration remained after rollback.' }
    } elseif ($Scenario -ne 'corrupt-backup') {
        foreach ($file in @('codex-auto-retry.exe','codex-auto-retry-mcp.exe','settings.ps1')) {
            if ([IO.File]::ReadAllText((Join-Path $runtimePath $file)) -ne 'old runtime') { throw 'Runtime was not restored.' }
        }
        $old = Read-JsonDocument (Join-Path $pluginTarget '.codex-plugin\plugin.json')
        if ($old.version -ne 'old') { throw 'Plugin source was not restored.' }
        if ($Scenario -eq 'foreign-startup') {
            if ($script:runValue -ne 'foreign.exe') { throw 'Concurrent startup was overwritten.' }
        } elseif ($Scenario -eq 'foreign-approval') {
            if ($script:approvalBytes[0] -ne 7) { throw 'Concurrent approval was overwritten.' }
        } elseif ($script:runValue -ne '"old.exe" run' -or $script:approvalBytes[0] -ne 3) { throw 'Startup snapshot was not restored.' }
    }
    if ($retained) {
        # Repair the fixture and exercise the same function used by interrupted
        # journal recovery. Retrying restoration must be idempotent.
        if ($Scenario -eq 'corrupt-backup') { [IO.File]::WriteAllText((Join-Path $transactionRoot 'runtime-backup\settings.ps1'), 'old runtime') }
        $Scenario = 'recovered'
        $saved = Read-UpgradeJournal $upgradeJournalPath
        Restore-IncompleteUpgrade -Journal $saved -PluginTarget $pluginTarget -MarketplacePath $marketplacePath -JournalPath $upgradeJournalPath -RuntimePath $runtimePath -Cli $cli -PluginId $pluginId
        if (Test-Path -LiteralPath $upgradeJournalPath) { throw 'Successful journal recovery remained incomplete.' }
    }
}
try {
    New-Item -ItemType Directory -Path $testRoot | Out-Null
    foreach ($scenario in @('success-with-warning','verify-fails','fresh-failure','foreign-startup','foreign-approval','corrupt-backup','cache-rollback-fails','desktop-reopened')) { Invoke-UpgradeFixture $scenario }
    [pscustomobject]@{Status='passed'; Scenarios=8; RuntimeAndSourceRollback=$true; Registry='mocked'; LiveProcesses='untouched'; RetryData='preserved'; InterruptedRecovery='passed'}
}
finally {
    $root = [IO.Path]::GetFullPath($testRoot)
    $prefix = [IO.Path]::GetFullPath($env:TEMP).TrimEnd('\') + '\'
    if (-not $root.StartsWith($prefix,[StringComparison]::OrdinalIgnoreCase)) { throw 'Unsafe cleanup root.' }
    if (Test-Path -LiteralPath $root) { Remove-Item -LiteralPath $root -Recurse -Force }
}
