[CmdletBinding()]
param(
    [string]$OutputDirectory = (Join-Path $env:USERPROFILE 'releases\codex-auto-retry'),
    [switch]$SkipBuild
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2

$pluginRoot = Split-Path -Parent $PSScriptRoot
$releaseTemplate = Join-Path $pluginRoot 'release\windows'
. (Join-Path $releaseTemplate 'common.ps1')
$installLauncher = ([string][char]0x5b89) + ([char]0x88c5) + '.cmd'
$uninstallLauncher = ([string][char]0x5378) + ([char]0x8f7d) + '.cmd'
$installReadme = 'README-' + ([char]0x5b89) + ([char]0x88c5) + ([char]0x8bf4) + ([char]0x660e) + '.txt'

$pluginManifestPath = Join-Path $pluginRoot '.codex-plugin\plugin.json'
$pluginManifest = Read-JsonDocument -Path $pluginManifestPath
if ($null -eq $pluginManifest -or [string]$pluginManifest.name -ne 'codex-auto-retry') {
    throw 'The Codex Auto Retry plugin manifest is missing or invalid.'
}
$pluginVersion = [string]$pluginManifest.version
$packageVersion = ($pluginVersion -split '\+', 2)[0]
if ($packageVersion -notmatch '^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$') {
    throw "The plugin version is not suitable for a release package: $pluginVersion"
}

$versionSource = Get-Content -LiteralPath (Join-Path $pluginRoot 'scripts\source\model.go') -Raw -Encoding UTF8
if ($versionSource -notmatch 'const\s+appVersion\s*=\s*"([^"]+)"' -or $matches[1] -ne $packageVersion) {
    throw 'The watchdog appVersion and plugin base version do not match.'
}

foreach ($required in @('common.ps1', 'deploy.ps1', 'uninstall-release.ps1', $installLauncher, $uninstallLauncher, $installReadme)) {
    if (-not (Test-Path -LiteralPath (Join-Path $releaseTemplate $required) -PathType Leaf)) {
        throw "Release template file is missing: $required"
    }
}

if (-not $SkipBuild) {
    Write-Host '[Release] Building and testing application binaries...'
    & (Join-Path $PSScriptRoot 'build.ps1') | Out-Host
}

$watchdog = Join-Path $pluginRoot 'scripts\bin\codex-auto-retry.exe'
$mcp = Join-Path $pluginRoot 'scripts\bin\codex-auto-retry-mcp.exe'
Assert-X64PeBinary -Path $watchdog
Assert-X64PeBinary -Path $mcp

$outputPath = Get-FullPath $OutputDirectory
New-Item -ItemType Directory -Force -Path $outputPath | Out-Null
$packageName = "Codex-Auto-Retry-$packageVersion-windows-x64"
$archivePath = Join-Path $outputPath ($packageName + '.zip')
$archiveHashPath = $archivePath + '.sha256.txt'
$stageParent = Join-Path ([System.IO.Path]::GetTempPath()) ('codex-auto-retry-release-' + [guid]::NewGuid().ToString('N'))
$packageRoot = Join-Path $stageParent $packageName

try {
    New-Item -ItemType Directory -Force -Path $packageRoot | Out-Null

    Write-Host '[Release] Staging one-click installer...'
    foreach ($file in @('common.ps1', 'deploy.ps1', 'uninstall-release.ps1', $installLauncher, $uninstallLauncher, $installReadme)) {
        Copy-Item -LiteralPath (Join-Path $releaseTemplate $file) -Destination (Join-Path $packageRoot $file) -Force
    }

    $payloadRoot = Join-Path $packageRoot 'payload\codex-auto-retry'
    New-Item -ItemType Directory -Force -Path $payloadRoot | Out-Null
    foreach ($entry in @('.codex-plugin', '.mcp.json', 'assets', 'docs', 'release', 'skills', 'scripts', 'LICENSE', 'README.md', 'THIRD_PARTY_NOTICES.md')) {
        $source = Join-Path $pluginRoot $entry
        if (-not (Test-Path -LiteralPath $source)) { throw "Plugin payload entry is missing: $entry" }
        Copy-Item -LiteralPath $source -Destination (Join-Path $payloadRoot $entry) -Recurse -Force
    }

    $nodeModules = Join-Path $payloadRoot 'scripts\source\ui\node_modules'
    if (Test-Path -LiteralPath $nodeModules -PathType Container) {
        $nodeModulesItem = Get-Item -LiteralPath $nodeModules -Force
        if (($nodeModulesItem.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw 'The staged node_modules path is a link and cannot be safely removed.'
        }
        Remove-Item -LiteralPath $nodeModules -Recurse -Force
    }

    $sourceBin = Join-Path $pluginRoot 'scripts\bin'
    $unexpectedSourceBinFiles = @(Get-ChildItem -LiteralPath $sourceBin -File -Recurse -Force |
        Where-Object {
            $_.DirectoryName -ne $sourceBin -or
            @('codex-auto-retry.exe', 'codex-auto-retry-mcp.exe') -notcontains $_.Name
        })
    if ($unexpectedSourceBinFiles.Count -gt 0) {
        $names = ($unexpectedSourceBinFiles.FullName | Sort-Object) -join ', '
        throw "Refusing to package runtime state from scripts/bin: $names"
    }

    $stagedBin = Join-Path $payloadRoot 'scripts\bin'
    $allowedBinFiles = @('codex-auto-retry.exe', 'codex-auto-retry-mcp.exe')
    $unexpectedBinEntries = @(Get-ChildItem -LiteralPath $stagedBin -Force |
        Where-Object { $allowedBinFiles -notcontains $_.Name })
    foreach ($entry in $unexpectedBinEntries) {
        Remove-Item -LiteralPath $entry.FullName -Recurse -Force
    }

    $releaseManifest = [pscustomobject][ordered]@{
        schemaVersion = 1
        product = 'Codex Auto Retry'
        packageVersion = $packageVersion
        pluginName = 'codex-auto-retry'
        pluginVersion = $pluginVersion
        target = 'windows-x64'
        payloadPath = 'payload/codex-auto-retry'
        installer = $installLauncher
        uninstaller = $uninstallLauncher
        createdAt = [DateTime]::UtcNow.ToString('o')
        requirements = @(
            'Windows 10 or 11 x64',
            'Codex App installed and started at least once',
            'Current-user write access; administrator rights are not required'
        )
    }
    Write-JsonAtomic -Path (Join-Path $packageRoot 'release-manifest.json') -Value $releaseManifest

    $sumLines = New-Object System.Collections.Generic.List[string]
    Get-ChildItem -LiteralPath $packageRoot -File -Recurse -Force |
        Where-Object { $_.Name -ne 'SHA256SUMS.txt' } |
        Sort-Object FullName |
        ForEach-Object {
            $relative = Get-RelativePackagePath -BasePath $packageRoot -Path $_.FullName
            $hash = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
            [void]$sumLines.Add($hash + '  ' + $relative)
        }
    Write-Utf8NoBom -Path (Join-Path $packageRoot 'SHA256SUMS.txt') -Content (($sumLines -join [Environment]::NewLine) + [Environment]::NewLine)

    Remove-Item -LiteralPath $archivePath -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $archiveHashPath -Force -ErrorAction SilentlyContinue
    Write-Host '[Release] Compressing archive...'
    Compress-Archive -LiteralPath $packageRoot -DestinationPath $archivePath -CompressionLevel Optimal -Force

    $archiveHash = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()
    [System.IO.File]::WriteAllText(
        $archiveHashPath,
        ($archiveHash + '  ' + (Split-Path -Leaf $archivePath) + [Environment]::NewLine),
        [System.Text.Encoding]::ASCII
    )

    $archive = Get-Item -LiteralPath $archivePath
    [pscustomobject]@{
        Package = $archive.FullName
        Bytes = $archive.Length
        SHA256 = $archiveHash
        HashFile = $archiveHashPath
        PluginVersion = $pluginVersion
        Target = 'Windows x64'
    }
}
finally {
    if (Test-Path -LiteralPath $stageParent -PathType Container) {
        $tempRoot = (Get-FullPath ([System.IO.Path]::GetTempPath())).TrimEnd('\') + '\'
        $stageFull = Get-FullPath $stageParent
        if ($stageFull.StartsWith($tempRoot, [System.StringComparison]::OrdinalIgnoreCase)) {
            Remove-Item -LiteralPath $stageFull -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
}
