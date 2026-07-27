[CmdletBinding()]
param()

Set-StrictMode -Version 2

function Get-FullPath {
    param([Parameter(Mandatory = $true)][string]$Path)

    if (-not [System.IO.Path]::IsPathRooted($Path)) {
        $Path = Join-Path (Get-Location).Path $Path
    }
    return [System.IO.Path]::GetFullPath($Path)
}

function Resolve-SafeChildPath {
    param(
        [Parameter(Mandatory = $true)][string]$BasePath,
        [Parameter(Mandatory = $true)][string]$ChildPath
    )

    $base = (Get-FullPath $BasePath).TrimEnd('\')
    $candidate = Get-FullPath (Join-Path $base $ChildPath)
    $prefix = $base + '\'
    if (-not $candidate.Equals($base, [System.StringComparison]::OrdinalIgnoreCase) -and
        -not $candidate.StartsWith($prefix, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "Path escapes its intended directory: $ChildPath"
    }
    return $candidate
}

function Write-Utf8NoBom {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$Content
    )

    $encoding = New-Object -TypeName System.Text.UTF8Encoding -ArgumentList $false
    [System.IO.File]::WriteAllText($Path, $Content, $encoding)
}

function Write-JsonAtomic {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)]$Value
    )

    $parent = Split-Path -Parent $Path
    New-Item -ItemType Directory -Force -Path $parent | Out-Null
    $temp = Join-Path $parent ('.' + [guid]::NewGuid().ToString('N') + '.tmp')
    try {
        $json = $Value | ConvertTo-Json -Depth 100
        Write-Utf8NoBom -Path $temp -Content ($json + [Environment]::NewLine)
        Move-Item -LiteralPath $temp -Destination $Path -Force
    }
    finally {
        Remove-Item -LiteralPath $temp -Force -ErrorAction SilentlyContinue
    }
}

function Copy-DirectoryContents {
    param(
        [Parameter(Mandatory = $true)][string]$Source,
        [Parameter(Mandatory = $true)][string]$Destination
    )

    New-Item -ItemType Directory -Force -Path $Destination | Out-Null
    Get-ChildItem -LiteralPath $Source -Force | ForEach-Object {
        Copy-Item -LiteralPath $_.FullName -Destination (Join-Path $Destination $_.Name) -Recurse -Force
    }
}

function Get-CodexCliCandidates {
    param(
        [string]$PreferredPath,
        [string]$LocalAppDataRoot = $env:LOCALAPPDATA
    )

    $paths = New-Object System.Collections.Generic.List[string]
    $add = {
        param([string]$Path)
        if ([string]::IsNullOrWhiteSpace($Path)) { return }
        try { $full = Get-FullPath $Path } catch { return }
        if ((Test-Path -LiteralPath $full -PathType Leaf) -and
            -not ($paths -contains $full)) {
            [void]$paths.Add($full)
        }
    }

    & $add $PreferredPath

    $bundledRoot = Join-Path $LocalAppDataRoot 'OpenAI\Codex\bin'
    if (Test-Path -LiteralPath $bundledRoot -PathType Container) {
        Get-ChildItem -LiteralPath $bundledRoot -Filter 'codex.exe' -File -Recurse -ErrorAction SilentlyContinue |
            Sort-Object LastWriteTime -Descending |
            ForEach-Object { & $add $_.FullName }
    }

    foreach ($commandName in @('codex.exe', 'codex.cmd', 'codex.ps1', 'codex')) {
        try {
            @(Get-Command $commandName -All -ErrorAction SilentlyContinue) | ForEach-Object {
                $path = $_.Path
                if ([string]::IsNullOrWhiteSpace($path)) { $path = $_.Source }
                & $add $path
            }
        }
        catch {
            # A missing command is expected on machines that use only the App.
        }
    }

    try {
        @(Get-AppxPackage -Name 'OpenAI.Codex' -ErrorAction SilentlyContinue) | ForEach-Object {
            & $add (Join-Path $_.InstallLocation 'app\resources\codex.exe')
        }
    }
    catch {
        # Appx discovery is optional; the bundled CLI is preferred.
    }

    return @($paths)
}

function Invoke-CodexCli {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string[]]$Arguments
    )

    $output = ''
    $exitCode = 1
    try {
        $output = (& $Path @Arguments 2>&1 | Out-String)
        if ($null -ne $LASTEXITCODE) { $exitCode = [int]$LASTEXITCODE } else { $exitCode = 0 }
    }
    catch {
        $output = $_.Exception.Message
        $exitCode = 1
    }
    return [pscustomobject]@{
        ExitCode = $exitCode
        Output = $output
    }
}

function Find-CodexCli {
    param(
        [string]$PreferredPath,
        [string]$LocalAppDataRoot = $env:LOCALAPPDATA
    )

    foreach ($candidate in (Get-CodexCliCandidates -PreferredPath $PreferredPath -LocalAppDataRoot $LocalAppDataRoot)) {
        $probe = Invoke-CodexCli -Path $candidate -Arguments @('plugin', '--help')
        if ($probe.ExitCode -eq 0 -and $probe.Output -match 'Manage Codex plugins') {
            return $candidate
        }
    }
    throw 'Codex CLI was not found. Start Codex App once, then run this installer again.'
}

function Assert-X64PeBinary {
    param([Parameter(Mandatory = $true)][string]$Path)

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "Required executable is missing: $Path"
    }
    $bytes = [System.IO.File]::ReadAllBytes($Path)
    if ($bytes.Length -lt 0x40 -or $bytes[0] -ne 0x4d -or $bytes[1] -ne 0x5a) {
        throw "Executable is not a valid Windows PE file: $Path"
    }
    $peOffset = [BitConverter]::ToInt32($bytes, 0x3c)
    if ($peOffset -lt 0 -or $peOffset + 6 -gt $bytes.Length -or
        $bytes[$peOffset] -ne 0x50 -or $bytes[$peOffset + 1] -ne 0x45) {
        throw "Executable has an invalid PE header: $Path"
    }
    $machine = [BitConverter]::ToUInt16($bytes, $peOffset + 4)
    if ($machine -ne 0x8664) {
        throw "Executable is not Windows x64 (machine 0x$('{0:X4}' -f $machine)): $Path"
    }
}

function Read-JsonDocument {
    param([Parameter(Mandatory = $true)][string]$Path)

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return $null }
    try {
        return (Get-Content -LiteralPath $Path -Raw -Encoding UTF8 | ConvertFrom-Json)
    }
    catch {
        throw "JSON file is invalid: $Path"
    }
}

function Get-MarketplaceName {
    param([Parameter(Mandatory = $true)]$Document)

    if ($null -eq $Document.PSObject.Properties['name']) { return 'personal' }
    $name = [string]$Document.name
    if ([string]::IsNullOrWhiteSpace($name)) { return 'personal' }
    return $name
}

function Get-RelativePackagePath {
    param(
        [Parameter(Mandatory = $true)][string]$BasePath,
        [Parameter(Mandatory = $true)][string]$Path
    )

    $base = (Get-FullPath $BasePath).TrimEnd('\') + '\'
    $full = Get-FullPath $Path
    if (-not $full.StartsWith($base, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "Path is outside package root: $Path"
    }
    return $full.Substring($base.Length).Replace('\', '/')
}
