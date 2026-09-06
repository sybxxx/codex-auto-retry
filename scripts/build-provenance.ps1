function Get-CodexBuildSourceHash {
    param([string]$Root)
    $source = Join-Path $Root 'scripts\source'
    $files = @(Get-ChildItem -LiteralPath $source -File -Recurse | Where-Object {
        $_.FullName -notmatch '[\\/]node_modules[\\/]' -and (
            $_.Extension -eq '.go' -or $_.Name -in @('go.mod','go.sum','panel.html','settings.ps1'))
    } | Sort-Object FullName)
    if ($files.Count -eq 0) { throw 'Build sources are missing.' }
    $lines = foreach ($file in $files) {
        $file.FullName.Substring($source.Length).Replace('\','/') + ':' + (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash
    }
    $sha = [Security.Cryptography.SHA256]::Create()
    try { return ([BitConverter]::ToString($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes(($lines -join "`n"))))).Replace('-','') }
    finally { $sha.Dispose() }
}

function Write-CodexBuildProvenance {
    param([string]$Root)
    $record = [ordered]@{ schema = 1; source_hash = Get-CodexBuildSourceHash $Root }
    foreach ($name in @('codex-auto-retry.exe','codex-auto-retry-mcp.exe')) {
        $record[$name] = (Get-FileHash -LiteralPath (Join-Path $Root ('scripts\bin\' + $name))).Hash
    }
    [IO.File]::WriteAllText((Join-Path $Root 'scripts\build-info.json'), ($record | ConvertTo-Json), [Text.UTF8Encoding]::new($false))
}

function Assert-CodexBuildProvenance {
    param([string]$Root)
    $path = Join-Path $Root 'scripts\build-info.json'
    if (-not (Test-Path -LiteralPath $path)) { throw 'Build provenance missing. Run scripts/build.ps1; do not package stale executables.' }
    $record = Get-Content -LiteralPath $path -Raw | ConvertFrom-Json
    if ($record.schema -ne 1 -or $record.source_hash -ne (Get-CodexBuildSourceHash $Root)) { throw 'Sources changed since the last build. Rebuild before packaging.' }
    foreach ($name in @('codex-auto-retry.exe','codex-auto-retry-mcp.exe')) {
        if ($record.$name -ne (Get-FileHash -LiteralPath (Join-Path $Root ('scripts\bin\' + $name))).Hash) { throw "Build binary mismatch: $name" }
    }
}
