[CmdletBinding()]
param()
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2
. (Join-Path $PSScriptRoot 'launch-codex.ps1')
$root = Join-Path ([IO.Path]::GetTempPath()) ('CodexLaunchSmoke_' + [Guid]::NewGuid().ToString('N'))
[void][IO.Directory]::CreateDirectory($root)
$originalEndpoint = $env:CODEX_APP_SERVER_WS_URL
$realWebSocket = ${function:Test-CodexLaunchWebSocket}
$script:checks = 0
function Assert-LaunchTest { param([bool]$OK, [string]$Name)
    if (-not $OK) { throw "FAIL: $Name" }; $script:checks++
}
function Write-Fixture { param([string]$Name, $Value)
    [IO.File]::WriteAllText((Join-Path $root $Name), ($Value | ConvertTo-Json -Depth 8))
}
$script:mockProcess = $null
$script:mockDesktop = @()
$script:mockPackages = @()
$script:sharedStatus = 'live'
$script:handshake = $true
function Get-CimInstance { param($ClassName, $Filter, $ErrorAction)
    if ($Filter -like 'ProcessId = *') { return $script:mockProcess }
    return $script:mockDesktop
}
function Get-AppxPackage { param($Name, $ErrorAction) return $script:mockPackages }
function Get-CodexAutoRetrySharedServerStatus { param($State, $ExpectedPort)
    return [pscustomobject]@{ Status = $script:sharedStatus; Endpoint = 'ws://127.0.0.1:49622' }
}
function Test-CodexLaunchWebSocket { param($Endpoint) return $script:handshake }
try {
    $status = [pscustomobject]@{ desktop_launch_mode = 'process_scoped'; running = $true; pid = 123;
        started_at = [DateTimeOffset]::UtcNow.ToString('o'); last_scan_at = [DateTimeOffset]::UtcNow.ToString('o') }
    $config = @{ shared_app_server_enabled = $true; shared_app_server_port = 49622 }
    Write-Fixture 'config.json' $config
    Assert-LaunchTest ((Get-CodexLaunchRoute $root).Mode -eq 'official') 'missing status falls back'
    Write-Fixture 'status.json' @{ running = $true }
    Assert-LaunchTest ((Get-CodexLaunchRoute $root).Reason -eq 'legacy_or_missing_status') 'legacy schema falls back'
    Write-Fixture 'status.json' $status
    Assert-LaunchTest ((Get-CodexLaunchRoute $root).Mode -eq 'official') 'missing worker executable falls back'
    [IO.File]::WriteAllText((Join-Path $root 'codex-auto-retry.exe'), 'test fixture, not executable')
    Assert-LaunchTest ((Get-CodexLaunchRoute $root).Mode -eq 'official') 'dead worker falls back'
    $script:mockProcess = [pscustomobject]@{ ExecutablePath = Join-Path $root 'codex-auto-retry.exe';
        CommandLine = 'codex-auto-retry.exe run'; CreationDate = [DateTime]::UtcNow }
    Write-Fixture 'shared-server.json' @{ fixture = $true }
    Assert-LaunchTest ((Get-CodexLaunchRoute $root).Mode -eq 'shared') 'healthy shared route without startup registry dependency'
    $script:mockProcess.CreationDate = [DateTime]::UtcNow.AddHours(-1)
    Assert-LaunchTest ((Get-CodexLaunchRoute $root).Mode -eq 'official') 'reused worker PID refused'
    $script:mockProcess.CreationDate = [DateTime]::UtcNow
    $script:mockProcess.CommandLine = 'codex-auto-retry.exe mcp'
    Assert-LaunchTest ((Get-CodexLaunchRoute $root).Mode -eq 'official') 'wrong worker command refused'
    $script:mockProcess.CommandLine = 'codex-auto-retry.exe run'
    $status.last_scan_at = [DateTimeOffset]::UtcNow.AddMinutes(-1).ToString('o')
    Write-Fixture 'status.json' $status
    Assert-LaunchTest ((Get-CodexLaunchRoute $root).Mode -eq 'official') 'stale heartbeat falls back'
    $status.last_scan_at = [DateTimeOffset]::UtcNow.ToString('o'); $status.running = $false
    Write-Fixture 'status.json' $status
    Assert-LaunchTest ((Get-CodexLaunchRoute $root).Mode -eq 'official') 'stopped service falls back'
    $status.running = $true; Write-Fixture 'status.json' $status
    foreach ($state in @('missing', 'invalid', 'unknown', 'stale')) {
        $script:sharedStatus = $state
        Assert-LaunchTest ((Get-CodexLaunchRoute $root).Mode -eq 'official') "shared $state falls back"
    }
    $script:sharedStatus = 'live'; $script:handshake = $false
    Assert-LaunchTest ((Get-CodexLaunchRoute $root).Reason -eq 'shared_handshake_failed') 'bad websocket falls back'
    $script:handshake = $true
    Assert-LaunchTest ((Get-CodexLaunchRoute $root -OfficialOnly).Mode -eq 'official') 'explicit official bypasses shared'
    Write-Fixture 'config.json' @{ shared_app_server_enabled = $false }
    Assert-LaunchTest ((Get-CodexLaunchRoute $root).Reason -eq 'shared_disabled') 'disabled shared falls back'
    Write-Fixture 'config.json' $config
    $exeMissing = $false
    try { Get-CodexDesktopExecutable | Out-Null } catch { $exeMissing = $true }
    Assert-LaunchTest $exeMissing 'missing app package fails explicitly'
    [void][IO.Directory]::CreateDirectory((Join-Path $root 'app'))
    $desktopExe = Join-Path $root 'app\ChatGPT.exe'
    [IO.File]::WriteAllText($desktopExe, 'test fixture, never launched')
    $script:mockPackages = @([pscustomobject]@{ InstallLocation = $root; Version = '1.0' })
    Assert-LaunchTest ((Get-CodexDesktopExecutable) -eq $desktopExe) 'current user package exact executable'
    Remove-Item -LiteralPath $desktopExe -Force
    $codexExe = Join-Path $root 'app\Codex.exe'
    [IO.File]::WriteAllText($codexExe, 'test fixture, never launched')
    Assert-LaunchTest ((Get-CodexDesktopExecutable) -eq $codexExe) 'new Codex executable fallback'
    Remove-Item -LiteralPath $codexExe -Force
    [IO.File]::WriteAllText($desktopExe, 'test fixture, never launched')
    Assert-CodexDesktopStopped $desktopExe
    $script:mockDesktop = @([pscustomobject]@{ Name = 'ChatGPT.exe'; ExecutablePath = $desktopExe })
    $refused = $false
    try { Assert-CodexDesktopStopped $desktopExe } catch { $refused = $true }
    Assert-LaunchTest $refused 'existing desktop rejected without stopping it'
    $waitRefused = $false
    $waitClock = [Diagnostics.Stopwatch]::StartNew()
    try { Wait-CodexDesktopStopped -TimeoutSeconds 1 } catch { $waitRefused = $true }
    Assert-LaunchTest ($waitRefused -and $waitClock.Elapsed.TotalSeconds -lt 3) 'wait-for-exit is bounded and non-destructive'
    $script:mockDesktop = @([pscustomobject]@{ Name = 'Codex.exe'; ExecutablePath = $null })
    $refused = $false
    try { Assert-CodexDesktopStopped $desktopExe } catch { $refused = $true }
    Assert-LaunchTest $refused 'unknown desktop identity rejected'

    # Exercise the real child environment path using PowerShell, never Codex.
    $env:CODEX_APP_SERVER_WS_URL = 'ws://127.0.0.1:1'
    $powershell = Join-Path $PSHOME 'powershell.exe'
    if (-not (Test-Path -LiteralPath $powershell)) { $powershell = (Get-Process -Id $PID).Path }
    foreach ($mode in @('official', 'shared')) {
        $route = [pscustomobject]@{ Mode = $mode; Endpoint = 'ws://127.0.0.1:49622' }
        $start = New-CodexDesktopStartInfo $powershell $route
        $start.CreateNoWindow = $true; $start.RedirectStandardOutput = $true
        $start.EnvironmentVariables['CODEX_LAUNCH_SMOKE_PRESERVE'] = 'unchanged'
        $command = '[Console]::Write($env:CODEX_APP_SERVER_WS_URL + "|" + $env:CODEX_LAUNCH_SMOKE_PRESERVE)'
        $start.Arguments = '-NoProfile -NonInteractive -EncodedCommand ' + [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($command))
        $child = [Diagnostics.Process]::Start($start)
        try {
            Assert-LaunchTest ($child.WaitForExit(15000)) "child $mode exited"
            $output = $child.StandardOutput.ReadToEnd()
            $expected = if ($mode -eq 'official') { '|unchanged' } else { 'ws://127.0.0.1:49622|unchanged' }
            Assert-LaunchTest ($output -eq $expected) "child $mode environment isolated"
        } finally {
            if (-not $child.HasExited) { $child.Kill(); [void]$child.WaitForExit(3000) }
            $child.Dispose()
        }
        Assert-LaunchTest ($env:CODEX_APP_SERVER_WS_URL -eq 'ws://127.0.0.1:1') 'parent endpoint unchanged'
    }
    Write-CodexLaunchResult $root 'official' 'test' 'started'
    Write-CodexLaunchResult $root 'shared' 'verified' 'started'
    Assert-LaunchTest ((Read-CodexLaunchJson (Join-Path $root 'desktop-launch.json')).mode -eq 'shared') 'bounded atomic result replaced'
    Assert-LaunchTest (@(Get-ChildItem -LiteralPath $root -Filter '*.tmp').Count -eq 0) 'atomic temporary files cleaned'
    $lock = Enter-CodexLaunchLock
    try {
        $start = New-CodexDesktopStartInfo $powershell ([pscustomobject]@{ Mode = 'official'; Endpoint = $null })
        $start.CreateNoWindow = $true; $start.RedirectStandardOutput = $true
        $start.EnvironmentVariables['LAUNCH_TEST_SCRIPT'] = Join-Path $PSScriptRoot 'launch-codex.ps1'
        $command = '. $env:LAUNCH_TEST_SCRIPT; try { $m = Enter-CodexLaunchLock; $m.ReleaseMutex(); $m.Dispose(); [Console]::Write("unexpected") } catch { [Console]::Write("blocked") }'
        $start.Arguments = '-NoProfile -NonInteractive -EncodedCommand ' + [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($command))
        $child = [Diagnostics.Process]::Start($start)
        try {
            Assert-LaunchTest ($child.WaitForExit(5000)) 'concurrent launcher returns within bound'
            Assert-LaunchTest ($child.StandardOutput.ReadToEnd() -eq 'blocked') 'concurrent launch refused by native mutex'
        } finally {
            if (-not $child.HasExited) { $child.Kill(); [void]$child.WaitForExit(3000) }
            $child.Dispose()
        }
    } finally { $lock.ReleaseMutex(); $lock.Dispose() }
    ${function:Test-CodexLaunchWebSocket} = $realWebSocket
    $probeListener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    $probeListener.Start()
    $refusedEndpoint = 'ws://127.0.0.1:' + $probeListener.LocalEndpoint.Port
    $probeListener.Stop()
    $clock = [Diagnostics.Stopwatch]::StartNew()
    Assert-LaunchTest (-not (Test-CodexLaunchWebSocket $refusedEndpoint)) 'real refused endpoint fails'
    Assert-LaunchTest ($clock.Elapsed.TotalSeconds -lt 5) 'real handshake bounded'
    [pscustomobject]@{ Status = 'safe launcher verified'; Checks = $script:checks; RealCodexLaunched = $false }
} finally {
    $env:CODEX_APP_SERVER_WS_URL = $originalEndpoint
    $resolved = [IO.Path]::GetFullPath($root)
    $tempBase = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\') + '\CodexLaunchSmoke_'
    if ($resolved.StartsWith($tempBase, [StringComparison]::OrdinalIgnoreCase)) {
        Remove-Item -LiteralPath $resolved -Recurse -Force -ErrorAction SilentlyContinue
    }
}
