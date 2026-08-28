[CmdletBinding()]
param(
    [ValidateSet('gui', 'status', 'enable', 'disable', 'start', 'stop', 'safe-disable', 'uninstall')]
    [string]$Action = 'gui',
    [switch]$RemoveData,
    [switch]$NoPrompt,
    [string]$UserProfileRoot = $env:USERPROFILE,
    [string]$LocalAppDataRoot = $env:LOCALAPPDATA,
    [string]$RunName = 'CodexAutoRetry'
)

$manager = Join-Path $PSScriptRoot 'payload\codex-auto-retry\scripts\startup-manager.ps1'
if (-not (Test-Path -LiteralPath $manager -PathType Leaf)) {
    # Also support launching the copy bundled inside the plugin payload.
    $manager = Join-Path $PSScriptRoot '..\..\scripts\startup-manager.ps1'
}
if (-not (Test-Path -LiteralPath $manager -PathType Leaf)) { throw "The packaged startup manager is missing: $manager" }
$arguments = @(
    '-NoLogo', '-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass'
)
if ($Action -eq 'gui') { $arguments += @('-WindowStyle', 'Hidden') }
$arguments += @(
    '-File', $manager, '-Action', $Action, '-ReleaseRoot', $PSScriptRoot,
    '-UserProfileRoot', $UserProfileRoot, '-LocalAppDataRoot', $LocalAppDataRoot,
    '-RunName', $RunName
)
if ($RemoveData) { $arguments += '-RemoveData' }
if ($NoPrompt) { $arguments += '-NoPrompt' }
& powershell.exe @arguments
exit $LASTEXITCODE
