[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$pluginRoot = Split-Path -Parent $PSScriptRoot
$sourceDir = Join-Path $PSScriptRoot 'source'
$uiDir = Join-Path $sourceDir 'ui'
$binDir = Join-Path $PSScriptRoot 'bin'
$watchdogOutput = Join-Path $binDir 'codex-auto-retry.exe'
$mcpOutput = Join-Path $binDir 'codex-auto-retry-mcp.exe'
$settingsScript = Join-Path $sourceDir 'ui\settings.ps1'

$settingsSource = [System.IO.File]::ReadAllText($settingsScript, [System.Text.UTF8Encoding]::new($false))
$tokens = $null
$parseErrors = $null
[void][System.Management.Automation.Language.Parser]::ParseInput($settingsSource, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count -gt 0) {
    throw "Tray settings script has a PowerShell parse error: $($parseErrors[0].Message)"
}

New-Item -ItemType Directory -Force -Path $binDir | Out-Null
Push-Location $uiDir
try {
    & npm ci
    if ($LASTEXITCODE -ne 0) { throw 'Panel dependencies failed to install.' }
    & npm run typecheck
    if ($LASTEXITCODE -ne 0) { throw 'Panel type check failed.' }
    & npm run build
    if ($LASTEXITCODE -ne 0) { throw 'Panel build failed.' }
}
finally {
    Pop-Location
}

Push-Location $sourceDir
try {
    & go fmt ./...
    if ($LASTEXITCODE -ne 0) { throw 'Go formatting failed.' }
    & go test ./...
    if ($LASTEXITCODE -ne 0) { throw 'Go tests failed.' }
    & go vet ./...
    if ($LASTEXITCODE -ne 0) { throw 'Go vet failed.' }
    & go build -trimpath -ldflags '-s -w -H=windowsgui' -o $watchdogOutput .
    if ($LASTEXITCODE -ne 0) { throw 'Watchdog build failed.' }
    & go build -trimpath -ldflags '-s -w' -o $mcpOutput .
    if ($LASTEXITCODE -ne 0) { throw 'MCP server build failed.' }
}
finally {
    Pop-Location
}

$watchdogBinary = Get-Item -LiteralPath $watchdogOutput
$mcpBinary = Get-Item -LiteralPath $mcpOutput
[pscustomobject]@{
    PluginRoot = $pluginRoot
    WatchdogBinary = $watchdogBinary.FullName
    WatchdogBytes = $watchdogBinary.Length
    MCPBinary = $mcpBinary.FullName
    MCPBytes = $mcpBinary.Length
    Status = 'built'
}
