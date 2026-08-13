[CmdletBinding()]
param(
    [string]$PackageRoot = '',
    [string]$UserProfileRoot = $env:USERPROFILE,
    [string]$LocalAppDataRoot = $env:LOCALAPPDATA,
    [string]$CodexCliPath = '',
    [switch]$DryRun,
    [switch]$SkipCodexCheck,
    [switch]$SkipPluginRegistration,
    [switch]$SkipRuntimeInstall,
    [switch]$EnableSharedAppServer
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2
. (Join-Path $PSScriptRoot 'common.ps1')

function Write-Step {
    param([string]$Message)
    Write-Host ('[Codex Auto Retry] ' + $Message)
}

function Set-ObjectProperty {
    param($Object, [string]$Name, $Value)
    if ($null -ne $Object.PSObject.Properties[$Name]) {
        $Object.$Name = $Value
    }
    else {
        $Object | Add-Member -NotePropertyName $Name -NotePropertyValue $Value
    }
}

function Read-ReleaseManifest {
    param([string]$Root)

    $path = Join-Path $Root 'release-manifest.json'
    $manifest = Read-JsonDocument -Path $path
    if ($null -eq $manifest -or $manifest.product -ne 'Codex Auto Retry' -or
        $manifest.target -ne 'windows-x64') {
        throw 'This folder is not a valid Codex Auto Retry Windows x64 release.'
    }
    return $manifest
}

function Test-ReleaseIntegrity {
    param([string]$Root)

    $sumsPath = Join-Path $Root 'SHA256SUMS.txt'
    if (-not (Test-Path -LiteralPath $sumsPath -PathType Leaf)) {
        throw 'SHA256SUMS.txt is missing from the release.'
    }
    $checked = 0
    foreach ($line in (Get-Content -LiteralPath $sumsPath -Encoding UTF8)) {
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        if ($line -notmatch '^([0-9A-Fa-f]{64})  (.+)$') {
            throw "Invalid checksum line: $line"
        }
        $expected = $matches[1].ToUpperInvariant()
        $relative = $matches[2].Replace('/', '\')
        $path = Resolve-SafeChildPath -BasePath $Root -ChildPath $relative
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
            throw "Release file is missing: $relative"
        }
        $actual = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToUpperInvariant()
        if ($actual -ne $expected) {
            throw "Release file failed its integrity check: $relative"
        }
        $checked++
    }
    if ($checked -lt 8) { throw 'The release checksum list is incomplete.' }
    return $checked
}

function Read-OrCreateMarketplace {
    param([string]$Path)

    $document = Read-JsonDocument -Path $Path
    if ($null -eq $document) {
        return [pscustomobject][ordered]@{
            name = 'personal'
            interface = [pscustomobject][ordered]@{ displayName = 'Personal' }
            plugins = @()
        }
    }
    if ($null -eq $document.PSObject.Properties['plugins']) {
        $document | Add-Member -NotePropertyName plugins -NotePropertyValue @()
    }
    if ($null -eq $document.plugins) { $document.plugins = @() }
    return $document
}

function Ensure-MarketplaceEntry {
    param($Document)

    $updated = New-Object System.Collections.Generic.List[object]
    $found = $false
    foreach ($entry in @($Document.plugins)) {
        if ($null -ne $entry -and [string]$entry.name -eq 'codex-auto-retry') {
            if ($found) { continue }
            $found = $true
            Set-ObjectProperty -Object $entry -Name 'source' -Value ([pscustomobject][ordered]@{
                source = 'local'
                path = './plugins/codex-auto-retry'
            })
            if ($null -eq $entry.PSObject.Properties['policy'] -or $null -eq $entry.policy) {
                Set-ObjectProperty -Object $entry -Name 'policy' -Value ([pscustomobject][ordered]@{
                    installation = 'AVAILABLE'
                    authentication = 'ON_INSTALL'
                })
            }
            else {
                if ($null -eq $entry.policy.PSObject.Properties['installation']) {
                    $entry.policy | Add-Member -NotePropertyName installation -NotePropertyValue 'AVAILABLE'
                }
                if ($null -eq $entry.policy.PSObject.Properties['authentication']) {
                    $entry.policy | Add-Member -NotePropertyName authentication -NotePropertyValue 'ON_INSTALL'
                }
            }
            if ($null -eq $entry.PSObject.Properties['category']) {
                $entry | Add-Member -NotePropertyName category -NotePropertyValue 'Productivity'
            }
            [void]$updated.Add($entry)
            continue
        }
        [void]$updated.Add($entry)
    }
    if (-not $found) {
        [void]$updated.Add([pscustomobject][ordered]@{
            name = 'codex-auto-retry'
            source = [pscustomobject][ordered]@{
                source = 'local'
                path = './plugins/codex-auto-retry'
            }
            policy = [pscustomobject][ordered]@{
                installation = 'AVAILABLE'
                authentication = 'ON_INSTALL'
            }
            category = 'Productivity'
        })
    }
    $Document.plugins = $updated.ToArray()
    return $Document
}

function Assert-ExistingPluginIsOurs {
    param([string]$Path)

    if (-not (Test-Path -LiteralPath $Path -PathType Container)) { return }
    $item = Get-Item -LiteralPath $Path -Force
    if (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Refusing to replace a linked plugin directory: $Path"
    }
    $manifest = Read-JsonDocument -Path (Join-Path $Path '.codex-plugin\plugin.json')
    if ($null -eq $manifest -or [string]$manifest.name -ne 'codex-auto-retry') {
        throw "The existing target directory is not Codex Auto Retry: $Path"
    }
}

function Set-InstalledMcpLauncher {
    param([string]$PluginPath, [string]$RuntimePath)

    $configPath = Join-Path $PluginPath '.mcp.json'
    $config = Read-JsonDocument -Path $configPath
    if ($null -eq $config -or $null -eq $config.PSObject.Properties['mcpServers'] -or
        $null -eq $config.mcpServers.PSObject.Properties['codex-auto-retry']) {
        throw 'The plugin MCP configuration is missing codex-auto-retry.'
    }

    $server = $config.mcpServers.'codex-auto-retry'
    $mcpPath = Join-Path $RuntimePath 'codex-auto-retry-mcp.exe'
    Set-ObjectProperty -Object $server -Name 'command' -Value $mcpPath
    Set-ObjectProperty -Object $server -Name 'args' -Value @('mcp')
    Write-JsonAtomic -Path $configPath -Value $config
}

function Install-Runtime {
    param([string]$PluginPath, [bool]$EnableSharedAppServer)

    $script = Join-Path $PluginPath 'scripts\install.ps1'
    $arguments = @('-NoLogo', '-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', $script)
    if ($EnableSharedAppServer) { $arguments += '-EnableSharedAppServer' }
    $output = (& powershell.exe @arguments 2>&1 | Out-String)
    if ($LASTEXITCODE -ne 0) {
        throw "The watchdog installer failed. Exit code: $LASTEXITCODE`n$($output.Trim())"
    }
    return $output
}

function Stop-RuntimeForUpgrade {
    param([string]$RuntimePath)

    $watchdog = Join-Path $RuntimePath 'codex-auto-retry.exe'
    $mcp = Join-Path $RuntimePath 'codex-auto-retry-mcp.exe'
    $stopSignal = Join-Path $RuntimePath 'stop.signal'
    $supervisorStop = Join-Path $RuntimePath 'supervisor.stop'
    $watchdogProcesses = @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
        Where-Object { $_.ExecutablePath -and [string]::Equals($_.ExecutablePath, $watchdog, [System.StringComparison]::OrdinalIgnoreCase) })
    $wasRunning = $watchdogProcesses.Count -gt 0
    if ($wasRunning) {
        New-Item -ItemType Directory -Force -Path $RuntimePath | Out-Null
        New-Item -ItemType File -Force -Path $supervisorStop | Out-Null
        New-Item -ItemType File -Force -Path $stopSignal | Out-Null
        $deadline = (Get-Date).AddSeconds(12)
        do {
            Start-Sleep -Milliseconds 250
            $watchdogProcesses = @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
                Where-Object { $_.ExecutablePath -and [string]::Equals($_.ExecutablePath, $watchdog, [System.StringComparison]::OrdinalIgnoreCase) })
        } while ($watchdogProcesses.Count -gt 0 -and (Get-Date) -lt $deadline)
        if ($watchdogProcesses.Count -gt 0) {
            $watchdogProcesses | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
        }
    }

    @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
        Where-Object { $_.ExecutablePath -and [string]::Equals($_.ExecutablePath, $mcp, [System.StringComparison]::OrdinalIgnoreCase) }) |
        ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
    Remove-Item -LiteralPath $stopSignal -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $supervisorStop -Force -ErrorAction SilentlyContinue
    return $wasRunning
}

function Verify-Installation {
    param(
        [string]$PluginPath,
        [string]$RuntimePath,
        [string]$Cli,
        [string]$PluginId,
        [string]$ExpectedBaseVersion,
        [bool]$VerifyPlugin,
        [bool]$VerifyRuntime,
        [bool]$ExpectedSharedAppServer
    )

    $pluginManifest = Read-JsonDocument -Path (Join-Path $PluginPath '.codex-plugin\plugin.json')
    if ($null -eq $pluginManifest -or [string]$pluginManifest.name -ne 'codex-auto-retry') {
        throw 'The installed plugin source could not be verified.'
    }

    $mcpConfig = Read-JsonDocument -Path (Join-Path $PluginPath '.mcp.json')
    $mcpServer = if ($null -eq $mcpConfig -or $null -eq $mcpConfig.PSObject.Properties['mcpServers'] -or
        $null -eq $mcpConfig.mcpServers.PSObject.Properties['codex-auto-retry']) {
        $null
    }
    else {
        $mcpConfig.mcpServers.'codex-auto-retry'
    }
    $expectedMcpPath = Join-Path $RuntimePath 'codex-auto-retry-mcp.exe'
    $mcpArgs = @()
    if ($null -ne $mcpServer -and $null -ne $mcpServer.PSObject.Properties['args']) {
        $mcpArgs = @($mcpServer.args)
    }
    if ($null -eq $mcpServer -or
        -not [string]::Equals([string]$mcpServer.command, $expectedMcpPath, [System.StringComparison]::OrdinalIgnoreCase) -or
        $mcpArgs.Count -ne 1 -or [string]$mcpArgs[0] -ne 'mcp') {
        throw 'The installed plugin does not use the direct background MCP launcher.'
    }

    if ($VerifyPlugin) {
        $listing = Invoke-CodexCli -Path $Cli -Arguments @('plugin', 'list', '--json')
        if ($listing.ExitCode -ne 0) { throw 'Codex could not verify the installed plugin.' }
        try { $listDocument = $listing.Output | ConvertFrom-Json } catch { throw 'Codex returned an invalid plugin list.' }
        $match = @($listDocument.installed) | Where-Object { $_.pluginId -eq $PluginId -and $_.installed -and $_.enabled } | Select-Object -First 1
        if ($null -eq $match) { throw "Codex did not report $PluginId as installed and enabled." }
    }

    if ($VerifyRuntime) {
        $watchdog = Join-Path $RuntimePath 'codex-auto-retry.exe'
        $mcp = Join-Path $RuntimePath 'codex-auto-retry-mcp.exe'
        Assert-X64PeBinary -Path $watchdog
        Assert-X64PeBinary -Path $mcp

        $status = Read-JsonDocument -Path (Join-Path $RuntimePath 'status.json')
        if ($null -eq $status -or -not $status.running -or [string]$status.version -ne $ExpectedBaseVersion) {
            throw 'The watchdog did not publish the expected running heartbeat.'
        }
        $process = Get-CimInstance Win32_Process -Filter ("ProcessId = " + [int]$status.pid) -ErrorAction SilentlyContinue
        if ($null -eq $process -or -not $process.ExecutablePath -or
            -not [string]::Equals($process.ExecutablePath, $watchdog, [System.StringComparison]::OrdinalIgnoreCase)) {
            throw 'The watchdog heartbeat does not match a running installed process.'
        }
        $config = Read-JsonDocument -Path (Join-Path $RuntimePath 'config.json')
        if ($null -eq $config -or [bool]$config.shared_app_server_enabled -ne $ExpectedSharedAppServer) {
            throw 'The installed shared app-server mode does not match the requested setting.'
        }
        $runProperty = Get-ItemProperty -Path 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run' -Name 'CodexAutoRetry' -ErrorAction SilentlyContinue
        $runValue = if ($null -eq $runProperty) { $null } else { $runProperty.CodexAutoRetry }
        if ([string]::IsNullOrWhiteSpace([string]$runValue) -or $runValue -notmatch [regex]::Escape($watchdog)) {
            throw 'The current-user startup entry was not registered.'
        }
    }
}

if ($env:OS -ne 'Windows_NT') { throw 'This release supports Windows only.' }
if (-not [Environment]::Is64BitOperatingSystem) { throw 'This release requires 64-bit Windows.' }
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = $PSScriptRoot }

$packageRootPath = Get-FullPath $PackageRoot
$profileRootPath = Get-FullPath $UserProfileRoot
$localAppDataPath = Get-FullPath $LocalAppDataRoot
$manifest = Read-ReleaseManifest -Root $packageRootPath
$payloadRelative = [string]$manifest.payloadPath
$payloadRoot = Resolve-SafeChildPath -BasePath $packageRootPath -ChildPath $payloadRelative.Replace('/', '\')
$pluginManifestPath = Join-Path $payloadRoot '.codex-plugin\plugin.json'
$pluginManifest = Read-JsonDocument -Path $pluginManifestPath
if ($null -eq $pluginManifest -or [string]$pluginManifest.name -ne 'codex-auto-retry' -or
    [string]$pluginManifest.version -ne [string]$manifest.pluginVersion) {
    throw 'The payload plugin manifest does not match the release manifest.'
}
Assert-X64PeBinary -Path (Join-Path $payloadRoot 'scripts\bin\codex-auto-retry.exe')
Assert-X64PeBinary -Path (Join-Path $payloadRoot 'scripts\bin\codex-auto-retry-mcp.exe')

Write-Step 'Verifying release files...'
$checkedFiles = Test-ReleaseIntegrity -Root $packageRootPath

$cli = $null
if (-not $SkipCodexCheck) {
    Write-Step 'Locating Codex App command line support...'
    $cli = Find-CodexCli -PreferredPath $CodexCliPath -LocalAppDataRoot $localAppDataPath
}
elseif (-not [string]::IsNullOrWhiteSpace($CodexCliPath)) {
    $cli = Get-FullPath $CodexCliPath
}

$pluginParent = Resolve-SafeChildPath -BasePath $profileRootPath -ChildPath 'plugins'
$pluginTarget = Resolve-SafeChildPath -BasePath $pluginParent -ChildPath 'codex-auto-retry'
$marketplacePath = Resolve-SafeChildPath -BasePath $profileRootPath -ChildPath '.agents\plugins\marketplace.json'
$runtimePath = Resolve-SafeChildPath -BasePath $localAppDataPath -ChildPath 'CodexAutoRetry'
$marketplace = Ensure-MarketplaceEntry -Document (Read-OrCreateMarketplace -Path $marketplacePath)
$marketplaceName = Get-MarketplaceName -Document $marketplace
if ($marketplaceName -notmatch '^[A-Za-z0-9._-]+$') {
    throw "The personal marketplace has an unsupported name: $marketplaceName"
}
$pluginId = 'codex-auto-retry@' + $marketplaceName

if ($DryRun) {
    Write-Step 'Dry run completed. No files or settings were changed.'
    [pscustomobject]@{
        Ready = $true
        PackageVersion = [string]$manifest.packageVersion
        PluginVersion = [string]$manifest.pluginVersion
        FilesVerified = $checkedFiles
        PluginTarget = $pluginTarget
        RuntimeTarget = $runtimePath
        Marketplace = $marketplacePath
        CodexCli = $cli
    }
    return
}

if (-not $SkipRuntimeInstall) {
    $pathSafety = Join-Path $payloadRoot 'scripts\path-safety.ps1'
    if (-not (Test-Path -LiteralPath $pathSafety -PathType Leaf)) {
        throw 'The payload is missing the runtime path-safety helper.'
    }
    . $pathSafety
    [void](Assert-CodexAutoRetryHostPath -Path $runtimePath)
}

Get-ChildItem -LiteralPath $packageRootPath -File -Recurse -Force -ErrorAction SilentlyContinue |
    Unblock-File -ErrorAction SilentlyContinue

$transactionRoot = Join-Path ([System.IO.Path]::GetTempPath()) ('codex-auto-retry-install-' + [guid]::NewGuid().ToString('N'))
$pluginBackup = Join-Path $transactionRoot 'plugin-backup'
$marketplaceBackup = Join-Path $transactionRoot 'marketplace.json'
$pluginExisted = Test-Path -LiteralPath $pluginTarget -PathType Container
$marketplaceExisted = Test-Path -LiteralPath $marketplacePath -PathType Leaf
$pluginChanged = $false
$marketplaceChanged = $false
$runtimeAttempted = $false
$registered = $false
$existingRuntimeWasRunning = $false
$success = $false

$oldUserProfile = $env:USERPROFILE
$oldHome = $env:HOME
$oldLocalAppData = $env:LOCALAPPDATA
try {
    New-Item -ItemType Directory -Force -Path $transactionRoot | Out-Null
    Assert-ExistingPluginIsOurs -Path $pluginTarget
    if ($pluginExisted) {
        Copy-DirectoryContents -Source $pluginTarget -Destination $pluginBackup
    }
    if ($marketplaceExisted) {
        Copy-Item -LiteralPath $marketplacePath -Destination $marketplaceBackup -Force
    }

    Write-Step 'Installing plugin files...'
    if ($pluginExisted) {
        $existingRuntimeWasRunning = Stop-RuntimeForUpgrade -RuntimePath $runtimePath
    }
    New-Item -ItemType Directory -Force -Path $pluginParent | Out-Null
    if ($pluginExisted) {
        Remove-Item -LiteralPath $pluginTarget -Recurse -Force
    }
    Copy-DirectoryContents -Source $payloadRoot -Destination $pluginTarget
    $gitMetadataBackup = Join-Path $pluginBackup '.git'
    $gitMetadataTarget = Join-Path $pluginTarget '.git'
    if (Test-Path -LiteralPath $gitMetadataBackup -PathType Container) {
        Copy-DirectoryContents -Source $gitMetadataBackup -Destination $gitMetadataTarget
    }
    elseif (Test-Path -LiteralPath $gitMetadataBackup -PathType Leaf) {
        Copy-Item -LiteralPath $gitMetadataBackup -Destination $gitMetadataTarget -Force
    }
    Set-InstalledMcpLauncher -PluginPath $pluginTarget -RuntimePath $runtimePath
    $pluginChanged = $true
    Write-JsonAtomic -Path (Join-Path $pluginTarget '.codex-auto-retry-release.json') -Value ([pscustomobject][ordered]@{
        packageVersion = [string]$manifest.packageVersion
        pluginVersion = [string]$manifest.pluginVersion
        installedAt = [DateTime]::UtcNow.ToString('o')
    })

    Write-Step 'Registering the personal Codex plugin...'
    Write-JsonAtomic -Path $marketplacePath -Value $marketplace
    $marketplaceChanged = $true

    $env:USERPROFILE = $profileRootPath
    $env:HOME = $profileRootPath
    $env:LOCALAPPDATA = $localAppDataPath

    if (-not $SkipPluginRegistration) {
        if ([string]::IsNullOrWhiteSpace([string]$cli)) { throw 'Codex CLI is required to register the plugin.' }
        $addResult = Invoke-CodexCli -Path $cli -Arguments @('plugin', 'add', $pluginId, '--json')
        if ($addResult.ExitCode -ne 0) {
            throw "Codex plugin registration failed with exit code $($addResult.ExitCode)."
        }
        $registered = $true
    }

    if (-not $SkipRuntimeInstall) {
        Write-Step 'Installing and starting the background watchdog...'
        $runtimeAttempted = $true
        [void](Install-Runtime -PluginPath $pluginTarget -EnableSharedAppServer:$EnableSharedAppServer)
    }

    Write-Step 'Verifying the completed installation...'
    $baseVersion = ([string]$manifest.pluginVersion -split '\+', 2)[0]
    Verify-Installation -PluginPath $pluginTarget -RuntimePath $runtimePath -Cli $cli -PluginId $pluginId -ExpectedBaseVersion $baseVersion -VerifyPlugin (-not $SkipPluginRegistration) -VerifyRuntime (-not $SkipRuntimeInstall) -ExpectedSharedAppServer:$EnableSharedAppServer
    $success = $true

    Write-Step 'Installation completed successfully.'
    Write-Host 'Restart Codex once so it connects to the shared recovery service, then open a new task to load the management panel.'
    [pscustomobject]@{
        Installed = $true
        Running = -not $SkipRuntimeInstall
        PackageVersion = [string]$manifest.packageVersion
        PluginVersion = [string]$manifest.pluginVersion
        PluginPath = $pluginTarget
        RuntimePath = $runtimePath
        Startup = if ($SkipRuntimeInstall) { 'not changed' } else { 'current user sign-in' }
        ExistingStatePreserved = $true
    }
}
catch {
    $failure = $_
    Write-Step 'Installation failed; restoring the previous installation...'
    try {
        if ($runtimeAttempted -and -not $pluginExisted -and
            (Test-Path -LiteralPath (Join-Path $pluginTarget 'scripts\uninstall.ps1') -PathType Leaf)) {
            [void](& powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File (Join-Path $pluginTarget 'scripts\uninstall.ps1') -KeepData 2>&1 | Out-String)
        }
        if ($pluginChanged -and (Test-Path -LiteralPath $pluginTarget -PathType Container)) {
            $current = Get-Item -LiteralPath $pluginTarget -Force
            if (($current.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -eq 0) {
                Remove-Item -LiteralPath $pluginTarget -Recurse -Force
            }
        }
        if ($pluginExisted -and (Test-Path -LiteralPath $pluginBackup -PathType Container)) {
            Copy-DirectoryContents -Source $pluginBackup -Destination $pluginTarget
        }
        if ($marketplaceChanged) {
            if ($marketplaceExisted) {
                Copy-Item -LiteralPath $marketplaceBackup -Destination $marketplacePath -Force
            }
            else {
                Remove-Item -LiteralPath $marketplacePath -Force -ErrorAction SilentlyContinue
            }
        }
        if ($registered -and -not [string]::IsNullOrWhiteSpace([string]$cli)) {
            if ($pluginExisted) {
                [void](Invoke-CodexCli -Path $cli -Arguments @('plugin', 'add', $pluginId, '--json'))
            }
            else {
                [void](Invoke-CodexCli -Path $cli -Arguments @('plugin', 'remove', $pluginId, '--json'))
            }
        }
        if (($runtimeAttempted -or $existingRuntimeWasRunning) -and $pluginExisted -and
            (Test-Path -LiteralPath (Join-Path $pluginTarget 'scripts\install.ps1') -PathType Leaf)) {
            [void](Install-Runtime -PluginPath $pluginTarget -EnableSharedAppServer:$EnableSharedAppServer)
        }
    }
    catch {
        Write-Warning 'Automatic rollback was incomplete. Existing retry data was not deleted.'
    }
    throw $failure
}
finally {
    $env:USERPROFILE = $oldUserProfile
    $env:HOME = $oldHome
    $env:LOCALAPPDATA = $oldLocalAppData
    if (Test-Path -LiteralPath $transactionRoot -PathType Container) {
        $tempRoot = (Get-FullPath ([System.IO.Path]::GetTempPath())).TrimEnd('\') + '\'
        $transactionFull = Get-FullPath $transactionRoot
        if ($transactionFull.StartsWith($tempRoot, [System.StringComparison]::OrdinalIgnoreCase)) {
            Remove-Item -LiteralPath $transactionFull -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
    if (-not $success) {
        Write-Host 'See the error above. No retry configuration or task state was intentionally deleted.'
    }
}
