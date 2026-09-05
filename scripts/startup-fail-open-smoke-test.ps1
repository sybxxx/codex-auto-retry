[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'environment.ps1')
$binary = Join-Path $PSScriptRoot 'bin\codex-auto-retry.exe'
$testRoot = Join-Path $env:TEMP ('codex-auto-retry-startup-' + [guid]::NewGuid().ToString('N'))
$dataDir = Join-Path $testRoot 'data'
$configPath = Join-Path $dataDir 'config.json'
$statusPath = Join-Path $dataDir 'status.json'
$stopPath = Join-Path $dataDir 'stop.signal'
$environmentName = 'CODEX_APP_SERVER_WS_URL'
$beforeEndpoint = [Environment]::GetEnvironmentVariable($environmentName, 'User')
$process = $null
$listener = $null

try {
    if (-not [string]::IsNullOrWhiteSpace($beforeEndpoint)) {
        throw 'Startup smoke requires the production user route to be absent; no live routing was modified.'
    }
    if (-not (Test-Path -LiteralPath $binary -PathType Leaf)) { throw "Watchdog binary is missing: $binary" }
    New-Item -ItemType Directory -Force -Path $dataDir | Out-Null

    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    $listener.Start()
    $port = ([System.Net.IPEndPoint]$listener.LocalEndpoint).Port
    $endpoint = 'ws://127.0.0.1:' + $port
    $config = [ordered]@{
        config_version = 8
        poll_interval_seconds = 1
        initial_delay_seconds = 1
        max_delay_seconds = 5
        delay_increment_seconds = 1
        delay_strategy = 'fixed'
        max_consecutive_retries = 2
        max_recovery_attempts = 2
        max_parallel_retries = 1
        start_ack_timeout_seconds = 10
        auth_max_attempts = 1
        unknown_max_attempts = 1
        session_roots = @()
        include_default_home = $false
        include_cockpit_homes = $false
        shared_app_server_port = $port
        shared_app_server_enabled = $true
        controller_failure_limit = 1
        retry_prompt = '继续'
        show_notifications = $false
    }
    [System.IO.File]::WriteAllText(
        $configPath,
        ($config | ConvertTo-Json -Depth 8),
        [System.Text.UTF8Encoding]::new($false)
    )
    # Keep the real User environment read-only. Only the isolated child inherits
    # the unavailable endpoint; never restore test state over live routing.
    $startInfo = [System.Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $binary
    $startInfo.Arguments = 'run --data-dir "' + $dataDir + '" --no-tray'
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.EnvironmentVariables[$environmentName] = $endpoint
    $process = [System.Diagnostics.Process]::Start($startInfo)
    $deadline = (Get-Date).AddSeconds(20)
    $status = $null
    do {
        Start-Sleep -Milliseconds 200
        if (Test-Path -LiteralPath $statusPath -PathType Leaf) {
            try { $status = Get-Content -Raw -Encoding UTF8 -LiteralPath $statusPath | ConvertFrom-Json } catch { $status = $null }
        }
    } while ((-not $status -or -not $status.running -or [int]$status.pid -ne $process.Id) -and (Get-Date) -lt $deadline)
    if (-not $status -or -not $status.running -or [int]$status.pid -ne $process.Id) {
        throw 'The watchdog did not publish a live startup heartbeat.'
    }

    $storedConfig = Get-Content -Raw -Encoding UTF8 -LiteralPath $configPath | ConvertFrom-Json
    $afterEndpoint = [Environment]::GetEnvironmentVariable($environmentName, 'User')
    if ([bool]$storedConfig.shared_app_server_enabled) {
        throw 'A failed shared backend was left enabled during startup.'
    }
    if ([string]$status.controller_state -ne 'shared_app_server_port_conflict') {
        throw "Unexpected startup fail-open reason: $([string]$status.controller_state)"
    }
    if (-not [string]::Equals([string]$afterEndpoint, [string]$beforeEndpoint, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw 'The isolated startup test changed the real user endpoint.'
    }

    [pscustomobject]@{
        Status = 'passed'
        StartupHeartbeat = $true
        SharedModeDisabled = $true
        FailOpenReasonPublished = $true
        ProductionEndpointUntouched = $true
    }
}
finally {
    if ($process) {
        if (-not $process.HasExited) {
            New-Item -ItemType File -Force -Path $stopPath | Out-Null
            try { [void]$process.WaitForExit(5000) } catch { }
        }
        if (-not $process.HasExited) { Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue }
        $process.Dispose()
    }
    if ($listener) { $listener.Stop() }
    if (Test-Path -LiteralPath $testRoot -PathType Container) {
        $resolved = [System.IO.Path]::GetFullPath($testRoot)
        $tempPrefix = [System.IO.Path]::GetFullPath($env:TEMP).TrimEnd('\') + '\'
        if ($resolved.StartsWith($tempPrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
            Remove-Item -LiteralPath $resolved -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
}
