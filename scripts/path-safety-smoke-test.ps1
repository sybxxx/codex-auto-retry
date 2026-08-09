$ErrorActionPreference = 'Stop'
$testRoot = Join-Path ([System.IO.Path]::GetTempPath()) ('codex-auto-retry-path-safety-' + [guid]::NewGuid().ToString('N'))
$linkedPath = $null
. (Join-Path $PSScriptRoot 'path-safety.ps1')

try {
    New-Item -ItemType Directory -Path $testRoot | Out-Null

    $plainPath = Join-Path $testRoot 'plain-runtime'
    $plainRedirect = Get-CodexAutoRetryRedirectedPath -Path $plainPath -ProbeIfMissing
    if ($plainRedirect) { throw "An ordinary runtime path was reported as redirected: $plainRedirect" }
    if (Test-Path -LiteralPath $plainPath) { throw 'The missing-path probe left its runtime directory behind.' }

    $targetPath = Join-Path $testRoot 'redirect-target'
    $linkedPath = Join-Path $testRoot 'redirected-runtime'
    New-Item -ItemType Directory -Path $targetPath | Out-Null
    New-Item -ItemType Junction -Path $linkedPath -Target $targetPath | Out-Null

    $redirect = Get-CodexAutoRetryRedirectedPath -Path $linkedPath
    if (-not $redirect -or -not [string]::Equals(
        ([System.IO.Path]::GetFullPath($redirect)).TrimEnd('\'),
        ([System.IO.Path]::GetFullPath($targetPath)).TrimEnd('\'),
        [System.StringComparison]::OrdinalIgnoreCase
    )) {
        throw 'A redirected runtime directory was not detected.'
    }

    $blocked = $false
    try { [void](Assert-CodexAutoRetryHostPath -Path $linkedPath) }
    catch { $blocked = $_.Exception.Message -like '*real host installation was not changed*' }
    if (-not $blocked) { throw 'The host-path assertion did not reject a redirected runtime directory.' }
    if (-not (Test-Path -LiteralPath $linkedPath -PathType Container) -or
        -not (Test-Path -LiteralPath $targetPath -PathType Container)) {
        throw 'The redirection check changed the linked path or its target.'
    }

    [pscustomobject]@{
        PlainPath = 'accepted'
        RedirectedPath = 'blocked'
        ProbeCleanup = 'passed'
    }
}
finally {
    if ($linkedPath -and (Test-Path -LiteralPath $linkedPath)) {
        Remove-Item -LiteralPath $linkedPath -Force -ErrorAction SilentlyContinue
    }
    if (Test-Path -LiteralPath $testRoot -PathType Container) {
        Remove-Item -LiteralPath $testRoot -Recurse -Force -ErrorAction SilentlyContinue
    }
}
