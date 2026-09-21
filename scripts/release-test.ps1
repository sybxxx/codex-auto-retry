[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$ArchivePath
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2

$archive = (Resolve-Path -LiteralPath $ArchivePath).Path
$testRoot = Join-Path $env:TEMP ('codex-auto-retry-release-test-' + [guid]::NewGuid().ToString('N'))
$installLauncher = ([string][char]0x5b89) + ([char]0x88c5) + '.cmd'
$uninstallLauncher = ([string][char]0x5378) + ([char]0x8f7d) + '.cmd'
$installReadme = 'README-' + ([char]0x5b89) + ([char]0x88c5) + ([char]0x8bf4) + ([char]0x660e) + '.txt'
$startupManagerLauncher = ([string][char]0x542f) + ([char]0x52a8) + ([char]0x7ba1) + ([char]0x7406) + ([char]0x5668) + '.cmd'
$safeDisableLauncher = ([string][char]0x5b89) + ([char]0x5168) + ([char]0x505c) + ([char]0x7528) + '.cmd'
$startupManagerVbs = 'startup-manager.vbs'
$safeCodexLauncher = ([string][char]0x5b89) + ([char]0x5168) + ([char]0x542f) + ([char]0x52a8) + 'Codex.vbs'

function Get-PeSubsystem {
    param([string]$Path)

    $bytes = [System.IO.File]::ReadAllBytes($Path)
    if ($bytes.Length -lt 256 -or $bytes[0] -ne 0x4d -or $bytes[1] -ne 0x5a) {
        throw "Not a PE executable: $Path"
    }
    $peOffset = [BitConverter]::ToInt32($bytes, 0x3c)
    $optionalHeader = $peOffset + 24
    if ($optionalHeader + 70 -gt $bytes.Length) { throw "Invalid PE optional header: $Path" }
    return [BitConverter]::ToUInt16($bytes, $optionalHeader + 68)
}

function Test-ReleasePathEquals {
    param(
        [Parameter(Mandatory = $true)][string]$Actual,
        [Parameter(Mandatory = $true)][string]$Expected
    )

    try {
        $actualPath = [IO.Path]::GetFullPath($Actual)
        $expectedPath = [IO.Path]::GetFullPath($Expected)
        foreach ($candidate in @($actualPath, $expectedPath)) {
            $parent = Get-Item -LiteralPath (Split-Path -Parent $candidate) -ErrorAction SilentlyContinue
            if ($null -eq $parent) { continue }
            $canonical = Join-Path $parent.FullName (Split-Path -Leaf $candidate)
            if ($candidate -eq $actualPath) { $actualPath = $canonical }
            else { $expectedPath = $canonical }
        }
        return [string]::Equals(
            $actualPath.TrimEnd('\'),
            $expectedPath.TrimEnd('\'),
            [StringComparison]::OrdinalIgnoreCase
        )
    }
    catch {
        return $false
    }
}

try {
    New-Item -ItemType Directory -Force -Path $testRoot | Out-Null
    Expand-Archive -LiteralPath $archive -DestinationPath $testRoot -Force
    $roots = @(Get-ChildItem -LiteralPath $testRoot -Directory -Force)
    if ($roots.Count -ne 1) { throw 'Release archive must contain exactly one top-level folder.' }
    $root = $roots[0].FullName

    foreach ($required in @(
        $installLauncher,
        $uninstallLauncher,
        'deploy.ps1',
        'uninstall-release.ps1',
        'startup-manager.ps1',
        $startupManagerVbs,
        $safeCodexLauncher,
        $startupManagerLauncher,
        $safeDisableLauncher,
        'common.ps1',
        'native-command.ps1',
        'upgrade-runtime.ps1',
        'close-codex.ps1',
        $installReadme,
        'release-manifest.json',
        'SHA256SUMS.txt',
        'payload\codex-auto-retry\.codex-plugin\plugin.json',
        'payload\codex-auto-retry\.mcp.json',
        'payload\codex-auto-retry\scripts\source\ui\settings.ps1',
        'payload\codex-auto-retry\scripts\environment.ps1',
        'payload\codex-auto-retry\scripts\launch-codex.ps1',
        'payload\codex-auto-retry\scripts\launch-codex-smoke-test.ps1',
        'payload\codex-auto-retry\scripts\install-routing-smoke-test.ps1',
        'payload\codex-auto-retry\scripts\startup-approval.ps1',
        'payload\codex-auto-retry\scripts\shared-server-status.ps1',
        'payload\codex-auto-retry\scripts\path-safety.ps1',
        'payload\codex-auto-retry\scripts\path-safety-smoke-test.ps1',
        'payload\codex-auto-retry\scripts\safe-disable.ps1',
        'payload\codex-auto-retry\scripts\safe-disable-smoke-test.ps1',
        'payload\codex-auto-retry\scripts\startup-manager-smoke-test.ps1',
        'payload\codex-auto-retry\scripts\status-smoke-test.ps1',
        'payload\codex-auto-retry\scripts\supervisor-smoke-test.ps1',
        'payload\codex-auto-retry\scripts\startup-fail-open-smoke-test.ps1',
        'payload\codex-auto-retry\scripts\shared-app-server-smoke-test.ps1',
        'payload\codex-auto-retry\scripts\shared-server-status-smoke-test.ps1',
        'payload\codex-auto-retry\scripts\app-server-protocol-smoke-test.ps1',
        'payload\codex-auto-retry\scripts\empty-response-protocol-smoke-test.ps1',
        'payload\codex-auto-retry\scripts\bin\codex-auto-retry.exe',
        'payload\codex-auto-retry\scripts\bin\codex-auto-retry-mcp.exe'
    )) {
        if (-not (Test-Path -LiteralPath (Join-Path $root $required))) {
            throw "Release archive is missing: $required"
        }
    }

    $archiveEntries = @(tar.exe -tf $archive)
    $privateRuntimeEntries = @($archiveEntries | Where-Object {
        $_ -match '(^|/)(config|control|state|status)\.json$' -or
        $_ -match '(^|/)logs/' -or
        $_ -match '(^|/)node_modules/' -or
        $_ -match '(^|/)\.git/'
    })
    if ($privateRuntimeEntries.Count -gt 0) {
        throw "Release contains runtime state or build dependencies: $($privateRuntimeEntries -join ', ')"
    }

    $sumCount = 0
    foreach ($line in (Get-Content -LiteralPath (Join-Path $root 'SHA256SUMS.txt') -Encoding UTF8)) {
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        if ($line -notmatch '^([0-9A-Fa-f]{64})  (.+)$') { throw "Invalid checksum line: $line" }
        $path = [System.IO.Path]::GetFullPath((Join-Path $root $matches[2].Replace('/', '\')))
        $rootPrefix = [System.IO.Path]::GetFullPath($root).TrimEnd('\') + '\'
        if (-not $path.StartsWith($rootPrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
            throw 'Checksum path escapes the release root.'
        }
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Checksum file is missing: $path" }
        if ((Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash -ne $matches[1]) {
            throw "Checksum mismatch: $path"
        }
        $sumCount++
    }
    if ($sumCount -lt 8) { throw 'Release checksum list is incomplete.' }

    $payloadRoot = Join-Path $root 'payload\codex-auto-retry'
    . (Join-Path $payloadRoot 'scripts\build-provenance.ps1')
    Assert-CodexBuildProvenance -Root $payloadRoot
    $payloadMcpConfig = Get-Content -Raw -Encoding UTF8 -LiteralPath (Join-Path $payloadRoot '.mcp.json') | ConvertFrom-Json
    $payloadMcpServer = $payloadMcpConfig.mcpServers.'codex-auto-retry'
    $payloadArgs = @($payloadMcpServer.args)
    if ([string]$payloadMcpServer.command -ne 'powershell.exe' -or
        [Array]::IndexOf($payloadArgs, '-WindowStyle') -lt 0 -or
        [Array]::IndexOf($payloadArgs, 'Hidden') -lt 0) {
        throw 'Portable MCP fallback is not configured to remain hidden.'
    }
    if ((Get-PeSubsystem (Join-Path $payloadRoot 'scripts\bin\codex-auto-retry-mcp.exe')) -ne 2) {
        throw 'MCP executable must use the Windows GUI subsystem.'
    }

    foreach ($script in @('common.ps1', 'native-command.ps1', 'upgrade-runtime.ps1', 'close-codex.ps1', 'deploy.ps1', 'uninstall-release.ps1', 'startup-manager.ps1')) {
        $tokens = $null
        $errors = $null
        [void][System.Management.Automation.Language.Parser]::ParseFile((Join-Path $root $script), [ref]$tokens, [ref]$errors)
        if ($errors.Count -gt 0) {
            throw "PowerShell parse error in $script`: $($errors[0].Message)"
        }
    }
    foreach ($relative in @(
        'payload\codex-auto-retry\scripts\environment.ps1',
        'payload\codex-auto-retry\scripts\install.ps1',
        'payload\codex-auto-retry\scripts\startup-approval.ps1',
        'payload\codex-auto-retry\scripts\shared-server-status.ps1',
        'payload\codex-auto-retry\scripts\path-safety.ps1',
        'payload\codex-auto-retry\scripts\path-safety-smoke-test.ps1',
        'payload\codex-auto-retry\scripts\safe-disable.ps1',
        'payload\codex-auto-retry\scripts\safe-disable-smoke-test.ps1',
        'payload\codex-auto-retry\scripts\startup-manager-smoke-test.ps1',
        'payload\codex-auto-retry\scripts\status-smoke-test.ps1',
        'payload\codex-auto-retry\scripts\supervisor-smoke-test.ps1',
        'payload\codex-auto-retry\scripts\uninstall.ps1',
        'payload\codex-auto-retry\scripts\smoke-test.ps1',
        'payload\codex-auto-retry\scripts\startup-fail-open-smoke-test.ps1',
        'payload\codex-auto-retry\scripts\shared-app-server-smoke-test.ps1',
        'payload\codex-auto-retry\scripts\shared-server-status-smoke-test.ps1',
        'payload\codex-auto-retry\scripts\status.ps1',
        'payload\codex-auto-retry\scripts\app-server-protocol-smoke-test.ps1',
        'payload\codex-auto-retry\scripts\environment-smoke-test.ps1'
    )) {
        $tokens = $null
        $errors = $null
        [void][System.Management.Automation.Language.Parser]::ParseFile((Join-Path $root $relative), [ref]$tokens, [ref]$errors)
        if ($errors.Count -gt 0) {
            throw "PowerShell parse error in $relative`: $($errors[0].Message)"
        }
    }
    $sharedStatusSource = [System.IO.File]::ReadAllText(
        (Join-Path $root 'payload\codex-auto-retry\scripts\shared-server-status.ps1'),
        [System.Text.UTF8Encoding]::new($false)
    )
    if (-not $sharedStatusSource.Contains('Get-CodexAutoRetrySharedServerStatus') -or
        -not $sharedStatusSource.Contains('Get-CodexAutoRetryStatusProperty') -or
        -not $sharedStatusSource.Contains('legacy_status_schema') -or
        -not $sharedStatusSource.Contains('CreationDate') -or
        -not $sharedStatusSource.Contains('Test-CodexAutoRetryTcpEndpoint')) {
        throw 'Shared-server status does not verify process identity, creation time, and endpoint liveness.'
    }
    $settingsPath = Join-Path $root 'payload\codex-auto-retry\scripts\source\ui\settings.ps1'
    $settingsSource = [System.IO.File]::ReadAllText($settingsPath, [System.Text.UTF8Encoding]::new($false))
    $settingsTokens = $null
    $settingsErrors = $null
    [void][System.Management.Automation.Language.Parser]::ParseInput(
        $settingsSource,
        [ref]$settingsTokens,
        [ref]$settingsErrors
    )
    if ($settingsErrors.Count -gt 0) {
        throw "PowerShell parse error in tray settings script: $($settingsErrors[0].Message)"
    }
    if ($settingsSource.Contains('.WaitForExit()')) {
        throw 'Tray settings still contains an unbounded local-command wait.'
    }
    $reservedPortMessage = ([string][char]0x88AB) + ' Windows ' + ([char]0x4FDD) + ([char]0x7559)
    $boundedLocalWait = $settingsSource.Contains('.WaitForExit(100)') -or
        ($settingsSource.Contains('Start-Sleep -Milliseconds 50') -and
            $settingsSource.Contains('[System.Windows.Forms.Application]::DoEvents()'))
    if (-not $boundedLocalWait -or
        -not $settingsSource.Contains('[System.Windows.Forms.Application]::DoEvents()') -or
        -not $settingsSource.Contains('$localCommandTimeoutMilliseconds') -or
        -not $settingsSource.Contains('$localCommandExitPortReserved') -or
        -not $settingsSource.Contains($reservedPortMessage)) {
        throw 'Tray settings does not keep local commands responsive and bounded.'
    }
    if (-not $settingsSource.Contains('Start-SafeCodexLaunch') -or
        -not $settingsSource.Contains('WaitForExitSeconds') -or
        -not $settingsSource.Contains('safe_launch_running')) {
        throw 'Tray settings does not expose the bounded safe Codex launcher.'
    }
    $installerSource = [System.IO.File]::ReadAllText(
        (Join-Path $root 'payload\codex-auto-retry\scripts\install.ps1'),
        [System.Text.UTF8Encoding]::new($false)
    )
    $startupApprovalSource = [System.IO.File]::ReadAllText(
        (Join-Path $root 'payload\codex-auto-retry\scripts\startup-approval.ps1'),
        [System.Text.UTF8Encoding]::new($false)
    )
    if ($installerSource -match 'Set-CodexAutoRetrySharedEnvironment|\[Environment\]::SetEnvironmentVariable' -or
        -not $installerSource.Contains('Restore-SafeInstallRouting')) {
        throw 'Installer can republish persistent Desktop routing or lacks safe rollback.'
    }
    if (-not $installerSource.Contains('Set-ConfigSharedMode ([bool]$EnableSharedAppServer)') -or
        -not $installerSource.Contains('Invoke-CodexAutoRetryConfigLocked') -or
        -not $installerSource.Contains('Assert-CodexAutoRetryHostPath') -or
        -not $installerSource.Contains('-WorkingDirectory $installDir') -or
        -not $installerSource.Contains('-LegacyOwnedEndpoint') -or
        -not $installerSource.Contains('Set-SupervisedStartupEntry') -or
        -not $installerSource.Contains('Test-OwnedStartupValue') -or
        -not $installerSource.Contains("ArgumentList @('supervise')") -or
        -not $startupApprovalSource.Contains('Open-CodexAutoRetryRunKey')) {
        throw 'Installer does not enforce fail-open upgrades and supervised startup migration.'
    }
    foreach ($relative in @(
        'payload\codex-auto-retry\scripts\install.ps1',
        'payload\codex-auto-retry\scripts\safe-disable.ps1',
        'payload\codex-auto-retry\scripts\uninstall.ps1',
        'payload\codex-auto-retry\scripts\safe-disable-smoke-test.ps1',
        'payload\codex-auto-retry\scripts\startup-manager-smoke-test.ps1'
    )) {
        $source = [System.IO.File]::ReadAllText((Join-Path $root $relative), [System.Text.UTF8Encoding]::new($false))
        if ($source -match '(?im)New-Item\s+-Path[^\r\n]*(?:CurrentVersion\\Run|\$runKey)') {
            throw "The release contains an unsafe whole-key Run registry write: $relative"
        }
    }
    $environmentSource = [System.IO.File]::ReadAllText(
        (Join-Path $root 'payload\codex-auto-retry\scripts\environment.ps1'),
        [System.Text.UTF8Encoding]::new($false)
    )
    if (-not $installerSource.Contains('existing Codex Auto Retry configuration is invalid and was not overwritten') -or
        -not $environmentSource.Contains('Invoke-CodexAutoRetryConfigLocked') -or
        -not $environmentSource.Contains('Break-glass cleanup must continue even when the settings file is') -or
        -not $environmentSource.Contains('Do not replace it with guessed defaults')) {
        throw 'Installer or safe-disable does not preserve a damaged configuration.'
    }
    $deploySource = [System.IO.File]::ReadAllText(
        (Join-Path $root 'deploy.ps1'),
        [System.Text.UTF8Encoding]::new($false)
    )
    $statusSource = [System.IO.File]::ReadAllText(
        (Join-Path $root 'payload\codex-auto-retry\scripts\status.ps1'),
        [System.Text.UTF8Encoding]::new($false)
    )
    $managerSource = [System.IO.File]::ReadAllText(
        (Join-Path $root 'payload\codex-auto-retry\scripts\startup-manager.ps1'),
        [System.Text.UTF8Encoding]::new($false)
    )
    if (-not $deploySource.Contains('Assert-CodexAutoRetryHostPath') -or
        -not $statusSource.Contains('runtime_path_redirected') -or
        -not $statusSource.Contains('Get-CodexAutoRetryStatusProperty') -or
        -not $managerSource.Contains('Get-CodexAutoRetryStatusProperty') -or
        -not $managerSource.Contains('StatusCompatibility')) {
        throw 'Release does not enforce runtime-path or legacy-status compatibility guards.'
    }
    $runtimeUninstallSource = [System.IO.File]::ReadAllText(
        (Join-Path $root 'payload\codex-auto-retry\scripts\uninstall.ps1'),
        [System.Text.UTF8Encoding]::new($false)
    )
    if (-not $runtimeUninstallSource.Contains('Assert-CodexAutoRetryHostPath') -or
        -not $runtimeUninstallSource.Contains('Test-OwnedStartupValue')) {
        throw 'Runtime uninstall does not enforce path and startup ownership guards.'
    }
    $releaseUninstallSource = [System.IO.File]::ReadAllText(
        (Join-Path $root 'uninstall-release.ps1'),
        [System.Text.UTF8Encoding]::new($false)
    )
    if (-not $releaseUninstallSource.Contains('$startupProperty = Get-ItemProperty')) {
        throw 'Release uninstaller does not handle a missing startup value safely.'
    }
    if (-not $releaseUninstallSource.Contains('function Remove-ReleaseStartupApproval') -or
        -not $releaseUninstallSource.Contains('function Test-ReleaseStartupApprovalPresent') -or
        -not $releaseUninstallSource.Contains('$startupApprovedRunSubKey') -or
        -not $releaseUninstallSource.Contains('OpenSubKey($startupApprovedRunSubKey, $true)') -or
        -not $releaseUninstallSource.Contains('Test-ReleaseStartupApprovalPresent -RunName')) {
        throw 'Release uninstaller does not independently remove and verify StartupApproved state.'
    }
    $safeDisableSource = [System.IO.File]::ReadAllText(
        (Join-Path $root 'payload\codex-auto-retry\scripts\safe-disable.ps1'),
        [System.Text.UTF8Encoding]::new($false)
    )
    if (-not $safeDisableSource.Contains('Test-OwnedStartupValue')) {
        throw 'Safe-disable does not enforce startup ownership guards.'
    }
    $managerLauncherSource = [System.IO.File]::ReadAllText(
        (Join-Path $root $startupManagerLauncher),
        [System.Text.Encoding]::ASCII
    )
    if (-not $managerLauncherSource.Contains('wscript.exe //B //Nologo') -or
        -not $managerLauncherSource.Contains('exit /b 0')) {
        throw 'The graphical startup manager launcher still holds a visible console.'
    }
    $managerVbsSource = [System.IO.File]::ReadAllText(
        (Join-Path $root $startupManagerVbs),
        [System.Text.Encoding]::ASCII
    )
    if (-not $managerVbsSource.Contains('shell.Run command, 0, False') -or
        -not $managerVbsSource.Contains('powershell.exe')) {
        throw 'The graphical startup manager does not use a detached no-console launcher.'
    }
    $managerWrapperSource = [System.IO.File]::ReadAllText(
        (Join-Path $root 'startup-manager.ps1'),
        [System.Text.UTF8Encoding]::new($false)
    )
    if ($managerWrapperSource.Contains('Start-Process') -or
        -not $managerWrapperSource.Contains('& powershell.exe @arguments')) {
        throw 'The graphical startup manager wrapper does not preserve paths safely.'
    }

    $savedPreference = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $pathSafetyOutput = (& powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File (Join-Path $root 'payload\codex-auto-retry\scripts\path-safety-smoke-test.ps1') 2>&1 | Out-String)
        if ($LASTEXITCODE -ne 0) { throw "Runtime path-safety test failed:`n$pathSafetyOutput" }

        $managerOutput = (& powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File (Join-Path $root 'payload\codex-auto-retry\scripts\startup-manager-smoke-test.ps1') 2>&1 | Out-String)
        if ($LASTEXITCODE -ne 0) { throw "Startup manager test failed:`n$managerOutput" }

        $installOutput = (& powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File (Join-Path $root 'deploy.ps1') -DryRun -SkipCodexCheck 2>&1 | Out-String)
        $installExitCode = $LASTEXITCODE
        if ($installExitCode -ne 0) { throw "Installer dry run failed:`n$installOutput" }

        $uninstallOutput = (& powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File (Join-Path $root 'uninstall-release.ps1') -DryRun -SkipCodexCheck 2>&1 | Out-String)
        $uninstallExitCode = $LASTEXITCODE
        if ($uninstallExitCode -ne 0) { throw "Uninstaller dry run failed:`n$uninstallOutput" }
    }
    finally {
        $ErrorActionPreference = $savedPreference
    }

    $testProfile = Join-Path $testRoot 'install-profile'
    $testLocalAppData = Join-Path $testRoot 'install-local-app-data'
    $savedPreference = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try { $installOutput = (& powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File (Join-Path $root 'deploy.ps1') `
        -UserProfileRoot $testProfile `
        -LocalAppDataRoot $testLocalAppData `
        -SkipCodexCheck `
        -SkipPluginRegistration `
        -SkipRuntimeInstall 2>&1 | Out-String)
        $isolatedExit = $LASTEXITCODE
    } finally { $ErrorActionPreference = $savedPreference }
    $directMcpInstall = 'passed'
    if ($isolatedExit -ne 0) {
        if ($installOutput -notmatch 'Close Codex completely before installing or upgrading' -or
            (Test-Path -LiteralPath (Join-Path $testProfile 'plugins\codex-auto-retry'))) {
            throw "Isolated installer test failed:`n$installOutput"
        }
        $directMcpInstall = 'blocked_by_live_desktop; no plugin files changed'
    } else {
    $installedMcpConfig = Get-Content -Raw -Encoding UTF8 -LiteralPath (Join-Path $testProfile 'plugins\codex-auto-retry\.mcp.json') | ConvertFrom-Json
    $installedMcpServer = $installedMcpConfig.mcpServers.'codex-auto-retry'
    $installedMcpArgs = @($installedMcpServer.args)
    $expectedMcpCommand = Join-Path $testLocalAppData 'CodexAutoRetry\codex-auto-retry-mcp.exe'
    if (-not (Test-ReleasePathEquals -Actual ([string]$installedMcpServer.command) -Expected $expectedMcpCommand) -or
        $installedMcpArgs.Count -ne 1 -or [string]$installedMcpArgs[0] -ne 'mcp') {
        throw "Installed plugin did not replace the shell wrapper with the direct MCP launcher. Actual command: $([string]$installedMcpServer.command); expected: $expectedMcpCommand; args: $($installedMcpArgs -join ',')"
    }
    }

    [pscustomobject]@{
        Archive = $archive
        TopLevelFolder = $roots[0].Name
        FilesVerified = $sumCount
        InstallerDryRun = 'passed'
        DirectMcpInstall = $directMcpInstall
        UninstallerDryRun = 'passed'
        SettingsCommandWait = 'bounded-and-responsive'
        StartupManager = 'verified'
        FailOpenInstallGuard = 'present'
        RuntimePathGuard = 'verified'
        Status = if ($isolatedExit -eq 0) { 'release verified' } else { 'package verified; live installation not tested' }
    }
}
finally {
    if (Test-Path -LiteralPath $testRoot -PathType Container) {
        $tempPrefix = [System.IO.Path]::GetFullPath($env:TEMP).TrimEnd('\') + '\'
        $testFull = [System.IO.Path]::GetFullPath($testRoot)
        if ($testFull.StartsWith($tempPrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
            Remove-Item -LiteralPath $testFull -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
}
