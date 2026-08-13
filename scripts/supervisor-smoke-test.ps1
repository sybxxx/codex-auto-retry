[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$binary = Join-Path $PSScriptRoot 'bin\codex-auto-retry.exe'
$testRoot = Join-Path $env:TEMP ('codex-auto-retry-supervisor-' + [guid]::NewGuid().ToString('N'))
$dataDir = Join-Path $testRoot 'data'
$supervisor = $null

function Read-Status {
    $path = Join-Path $dataDir 'status.json'
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { return $null }
    try { return Get-Content -Raw -Encoding UTF8 -LiteralPath $path | ConvertFrom-Json } catch { return $null }
}

function Wait-Status {
    param([scriptblock]$Predicate, [int]$Seconds = 15)
    $deadline = (Get-Date).AddSeconds($Seconds)
    do {
        Start-Sleep -Milliseconds 250
        $value = Read-Status
        if ($value -and (& $Predicate $value)) { return $value }
    } while ((Get-Date) -lt $deadline)
    $supervisorLog = Join-Path $dataDir 'logs\supervisor.log'
    $daemonLog = Join-Path $dataDir 'logs\daemon.log'
    $details = @(
        if (Test-Path -LiteralPath $supervisorLog) { Get-Content -Raw -LiteralPath $supervisorLog } else { 'supervisor log missing' }
        if (Test-Path -LiteralPath $daemonLog) { Get-Content -Tail 20 -LiteralPath $daemonLog } else { 'daemon log missing' }
        Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
            Where-Object { $_.ExecutablePath -and $_.ExecutablePath -like '*codex-auto-retry.exe' } |
            Select-Object ProcessId, CommandLine | Out-String
    ) -join [Environment]::NewLine
    throw "Timed out waiting for the supervisor worker heartbeat.`n$details"
}

try {
    if (-not (Test-Path -LiteralPath $binary -PathType Leaf)) { throw "Watchdog binary is missing: $binary" }
    New-Item -ItemType Directory -Force -Path $dataDir | Out-Null
    $config = [ordered]@{
        config_version = 8; poll_interval_seconds = 1; initial_delay_seconds = 1
        max_delay_seconds = 5; delay_increment_seconds = 1; delay_strategy = 'fixed'
        max_consecutive_retries = 2; max_recovery_attempts = 2; max_parallel_retries = 1
        start_ack_timeout_seconds = 10; auth_max_attempts = 1; unknown_max_attempts = 1
        session_roots = @(); include_default_home = $false; include_cockpit_homes = $false
        shared_app_server_port = 49621; shared_app_server_enabled = $false
        controller_failure_limit = 1; retry_prompt = '缁х画'; show_notifications = $false
    }
    [System.IO.File]::WriteAllText((Join-Path $dataDir 'config.json'), ($config | ConvertTo-Json -Depth 8), [System.Text.UTF8Encoding]::new($false))

    $supervisor = Start-Process -FilePath $binary -ArgumentList @('supervise', '--data-dir', $dataDir, '--no-tray') -WindowStyle Hidden -PassThru
    $first = Wait-Status -Seconds 20 -Predicate { param($value) $value.running -and [int]$value.pid -gt 0 }
    $workerPid = [int]$first.pid
    if ($workerPid -eq $supervisor.Id) { throw 'The supervisor published its own PID instead of the worker PID.' }
    Stop-Process -Id $workerPid -Force
    $restarted = Wait-Status -Seconds 15 -Predicate { param($value) $value.running -and [int]$value.pid -ne $workerPid }
    $newWorkerPid = [int]$restarted.pid
    if ($newWorkerPid -eq $supervisor.Id) { throw 'The restarted heartbeat belongs to the supervisor, not the worker.' }

    New-Item -ItemType File -Force -Path (Join-Path $dataDir 'stop.signal') | Out-Null
    if (-not $supervisor.WaitForExit(15000)) { throw 'The supervisor did not honor an intentional stop.' }
    if (Test-Path -LiteralPath (Join-Path $dataDir 'supervisor.stop')) { throw 'Intentional stop marker was left behind.' }

    [pscustomobject]@{ Status = 'passed'; SupervisorStarted = $true; WorkerHeartbeatPublished = $true; WorkerRestarted = $true; SupervisorPID = $supervisor.Id; InitialWorkerPID = $workerPid; RestartedWorkerPID = $newWorkerPid; IntentionalStopHonored = $true }
}
finally {
    if ($supervisor -and -not $supervisor.HasExited) { Stop-Process -Id $supervisor.Id -Force -ErrorAction SilentlyContinue }
    if (Test-Path -LiteralPath $testRoot -PathType Container) {
        $resolved = [System.IO.Path]::GetFullPath($testRoot); $prefix = [System.IO.Path]::GetFullPath($env:TEMP).TrimEnd('\') + '\'
        if ($resolved.StartsWith($prefix, [System.StringComparison]::OrdinalIgnoreCase)) { Remove-Item -LiteralPath $resolved -Recurse -Force -ErrorAction SilentlyContinue }
    }
}
