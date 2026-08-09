function Get-CodexAutoRetryRedirectedPath {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path,
        [switch]$ProbeIfMissing
    )

    $fullPath = [System.IO.Path]::GetFullPath($Path)
    $inspectPath = $fullPath
    $probePath = $null

    try {
        if (-not (Test-Path -LiteralPath $inspectPath -PathType Container)) {
            if (-not $ProbeIfMissing) { return $null }

            $parent = Split-Path -Parent $fullPath
            if (-not (Test-Path -LiteralPath $parent -PathType Container)) {
                throw "The runtime parent directory does not exist: $parent"
            }
            $probePath = Join-Path $parent ('.codex-auto-retry-host-probe-' + [guid]::NewGuid().ToString('N'))
            New-Item -ItemType Directory -Path $probePath | Out-Null
            $inspectPath = $probePath
        }

        $item = Get-Item -Force -LiteralPath $inspectPath
        if ($null -eq $item.PSObject.Properties['Target']) { return $null }

        foreach ($target in @($item.Target)) {
            if ([string]::IsNullOrWhiteSpace([string]$target)) { continue }
            $targetPath = [System.IO.Path]::GetFullPath([string]$target)
            if (-not [string]::Equals(
                $targetPath.TrimEnd('\'),
                ([System.IO.Path]::GetFullPath($inspectPath)).TrimEnd('\'),
                [System.StringComparison]::OrdinalIgnoreCase
            )) {
                return $targetPath
            }
        }
        return $null
    }
    finally {
        if ($probePath -and (Test-Path -LiteralPath $probePath -PathType Container)) {
            Remove-Item -LiteralPath $probePath -Force -ErrorAction SilentlyContinue
        }
    }
}

function Assert-CodexAutoRetryHostPath {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path
    )

    $fullPath = [System.IO.Path]::GetFullPath($Path)
    $redirectedPath = Get-CodexAutoRetryRedirectedPath -Path $fullPath -ProbeIfMissing
    if ($redirectedPath) {
        throw "Windows redirected the Codex Auto Retry runtime path into an app sandbox: $redirectedPath. The real host installation was not changed. Run the installer from Windows Explorer or a normal PowerShell window outside Codex."
    }
    return $fullPath
}
