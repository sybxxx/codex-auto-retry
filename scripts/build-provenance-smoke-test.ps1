$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'build-provenance.ps1')
$root = Join-Path $env:TEMP ('CodexBuildProof_' + [guid]::NewGuid().ToString('N'))
try {
    [void][IO.Directory]::CreateDirectory((Join-Path $root 'scripts\source'))
    [void][IO.Directory]::CreateDirectory((Join-Path $root 'scripts\bin'))
    $source = Join-Path $root 'scripts\source\main.go'
    [IO.File]::WriteAllText($source,'package main')
    foreach ($name in @('codex-auto-retry.exe','codex-auto-retry-mcp.exe')) { [IO.File]::WriteAllText((Join-Path $root ('scripts\bin\'+$name)), 'fixture only') }
    $rejected = $false
    try { Assert-CodexBuildProvenance $root } catch { $rejected = $true }
    if (-not $rejected) { throw 'Missing provenance accepted.' }
    Write-CodexBuildProvenance $root
    Assert-CodexBuildProvenance $root
    [IO.File]::WriteAllText($source,'package changed')
    $rejected = $false
    try { Assert-CodexBuildProvenance $root } catch { $rejected = $true }
    if (-not $rejected) { throw 'Changed source accepted.' }
    Write-CodexBuildProvenance $root
    [IO.File]::WriteAllText((Join-Path $root 'scripts\bin\codex-auto-retry.exe'), 'stale binary')
    $rejected = $false
    try { Assert-CodexBuildProvenance $root } catch { $rejected = $true }
    if (-not $rejected) { throw 'Stale binary accepted.' }
    'Build provenance regression passed'
} finally {
    $resolved = [IO.Path]::GetFullPath($root)
    $prefix = [IO.Path]::GetFullPath($env:TEMP).TrimEnd('\') + '\CodexBuildProof_'
    if ($resolved.StartsWith($prefix,[StringComparison]::OrdinalIgnoreCase)) { Remove-Item -LiteralPath $resolved -Recurse -Force }
}
