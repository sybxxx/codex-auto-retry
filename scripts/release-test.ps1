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
        'common.ps1',
        $installReadme,
        'release-manifest.json',
        'SHA256SUMS.txt',
        'payload\codex-auto-retry\.codex-plugin\plugin.json',
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
        $_ -match '(^|/)node_modules/'
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

    foreach ($script in @('common.ps1', 'deploy.ps1', 'uninstall-release.ps1')) {
        $tokens = $null
        $errors = $null
        [void][System.Management.Automation.Language.Parser]::ParseFile((Join-Path $root $script), [ref]$tokens, [ref]$errors)
        if ($errors.Count -gt 0) {
            throw "PowerShell parse error in $script`: $($errors[0].Message)"
        }
    }

    $savedPreference = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
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

    [pscustomobject]@{
        Archive = $archive
        TopLevelFolder = $roots[0].Name
        FilesVerified = $sumCount
        InstallerDryRun = 'passed'
        UninstallerDryRun = 'passed'
        Status = 'release verified'
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
