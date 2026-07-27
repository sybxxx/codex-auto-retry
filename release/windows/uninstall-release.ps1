[CmdletBinding()]
param(
    [string]$UserProfileRoot = $env:USERPROFILE,
    [string]$LocalAppDataRoot = $env:LOCALAPPDATA,
    [string]$CodexCliPath = '',
    [switch]$RemoveData,
    [switch]$DryRun,
    [switch]$SkipCodexCheck
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2
. (Join-Path $PSScriptRoot 'common.ps1')

function Write-Step {
    param([string]$Message)
    Write-Host ('[Codex Auto Retry] ' + $Message)
}

function Test-PluginInstalled {
    param([string]$Cli, [string]$PluginId)

    $result = Invoke-CodexCli -Path $Cli -Arguments @('plugin', 'list', '--json')
    if ($result.ExitCode -ne 0) { throw 'Codex could not read the installed plugin list.' }
    try { $document = $result.Output | ConvertFrom-Json } catch { throw 'Codex returned an invalid plugin list.' }
    return $null -ne (@($document.installed) | Where-Object { $_.pluginId -eq $PluginId } | Select-Object -First 1)
}

function Remove-MarketplaceEntry {
    param([string]$Path)

    $document = Read-JsonDocument -Path $Path
    if ($null -eq $document -or $null -eq $document.PSObject.Properties['plugins']) { return $false }
    $kept = @($document.plugins | Where-Object { $null -eq $_ -or [string]$_.name -ne 'codex-auto-retry' })
    if ($kept.Count -eq @($document.plugins).Count) { return $false }
    $document.plugins = $kept
    Write-JsonAtomic -Path $Path -Value $document
    return $true
}

if ($env:OS -ne 'Windows_NT') { throw 'This uninstaller supports Windows only.' }

$profileRootPath = Get-FullPath $UserProfileRoot
$localAppDataPath = Get-FullPath $LocalAppDataRoot
$pluginParent = Resolve-SafeChildPath -BasePath $profileRootPath -ChildPath 'plugins'
$pluginTarget = Resolve-SafeChildPath -BasePath $pluginParent -ChildPath 'codex-auto-retry'
$marketplacePath = Resolve-SafeChildPath -BasePath $profileRootPath -ChildPath '.agents\plugins\marketplace.json'
$runtimePath = Resolve-SafeChildPath -BasePath $localAppDataPath -ChildPath 'CodexAutoRetry'

$marketplace = Read-JsonDocument -Path $marketplacePath
$marketplaceName = if ($null -eq $marketplace) { 'personal' } else { Get-MarketplaceName -Document $marketplace }
if ($marketplaceName -notmatch '^[A-Za-z0-9._-]+$') {
    throw "The personal marketplace has an unsupported name: $marketplaceName"
}
$pluginId = 'codex-auto-retry@' + $marketplaceName

$cli = $null
if (-not $SkipCodexCheck) {
    Write-Step 'Locating Codex App command line support...'
    $cli = Find-CodexCli -PreferredPath $CodexCliPath -LocalAppDataRoot $localAppDataPath
}

if ($DryRun) {
    Write-Step 'Dry run completed. No files or settings were changed.'
    [pscustomobject]@{
        Ready = $true
        PluginId = $pluginId
        PluginPath = $pluginTarget
        RuntimePath = $runtimePath
        DataWillBeRemoved = [bool]$RemoveData
        CodexCli = $cli
    }
    return
}

$oldUserProfile = $env:USERPROFILE
$oldHome = $env:HOME
$oldLocalAppData = $env:LOCALAPPDATA
try {
    $env:USERPROFILE = $profileRootPath
    $env:HOME = $profileRootPath
    $env:LOCALAPPDATA = $localAppDataPath

    Write-Step 'Stopping the background watchdog and removing startup...'
    $runtimeUninstaller = Join-Path $pluginTarget 'scripts\uninstall.ps1'
    if (-not (Test-Path -LiteralPath $runtimeUninstaller -PathType Leaf)) {
        $runtimeUninstaller = Join-Path $PSScriptRoot 'payload\codex-auto-retry\scripts\uninstall.ps1'
    }
    if (Test-Path -LiteralPath $runtimeUninstaller -PathType Leaf) {
        $arguments = @('-NoLogo', '-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', $runtimeUninstaller)
        if (-not $RemoveData) { $arguments += '-KeepData' }
        $output = (& powershell.exe @arguments 2>&1 | Out-String)
        if ($LASTEXITCODE -ne 0) { throw "The watchdog uninstaller failed with exit code $LASTEXITCODE." }
    }
    else {
        Remove-ItemProperty -Path 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run' -Name 'CodexAutoRetry' -ErrorAction SilentlyContinue
        if ($RemoveData -and (Test-Path -LiteralPath $runtimePath -PathType Container)) {
            $runtimeItem = Get-Item -LiteralPath $runtimePath -Force
            if (($runtimeItem.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "Refusing to remove a linked runtime directory: $runtimePath"
            }
            Remove-Item -LiteralPath $runtimePath -Recurse -Force
        }
    }

    if (-not $SkipCodexCheck) {
        Write-Step 'Removing the plugin from Codex...'
        if (Test-PluginInstalled -Cli $cli -PluginId $pluginId) {
            $removeResult = Invoke-CodexCli -Path $cli -Arguments @('plugin', 'remove', $pluginId, '--json')
            if ($removeResult.ExitCode -ne 0) {
                throw "Codex plugin removal failed with exit code $($removeResult.ExitCode)."
            }
        }
    }

    Write-Step 'Removing the personal marketplace entry and plugin files...'
    [void](Remove-MarketplaceEntry -Path $marketplacePath)
    if (Test-Path -LiteralPath $pluginTarget -PathType Container) {
        $pluginItem = Get-Item -LiteralPath $pluginTarget -Force
        if (($pluginItem.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Refusing to remove a linked plugin directory: $pluginTarget"
        }
        $manifest = Read-JsonDocument -Path (Join-Path $pluginTarget '.codex-plugin\plugin.json')
        if ($null -eq $manifest -or [string]$manifest.name -ne 'codex-auto-retry') {
            throw "The plugin target is not Codex Auto Retry: $pluginTarget"
        }
        Remove-Item -LiteralPath $pluginTarget -Recurse -Force
    }

    $runProperty = Get-ItemProperty -Path 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run' -Name 'CodexAutoRetry' -ErrorAction SilentlyContinue
    $runValue = if ($null -eq $runProperty) { $null } else { $runProperty.CodexAutoRetry }
    if (-not [string]::IsNullOrWhiteSpace([string]$runValue)) {
        throw 'The startup entry is still present after uninstall.'
    }
    if (-not $SkipCodexCheck -and (Test-PluginInstalled -Cli $cli -PluginId $pluginId)) {
        throw 'Codex still reports the plugin as installed.'
    }

    Write-Step 'Uninstall completed successfully.'
    if ($RemoveData) {
        Write-Host 'Runtime settings, state, and logs were removed.'
    }
    else {
        Write-Host "Retry settings and state were preserved in: $runtimePath"
    }
    Write-Host 'Open a new Codex task to refresh the plugin list.'
}
finally {
    $env:USERPROFILE = $oldUserProfile
    $env:HOME = $oldHome
    $env:LOCALAPPDATA = $oldLocalAppData
}
