[CmdletBinding()]
param(
    [switch]$EnableSharedAppServer
)

$ErrorActionPreference = 'Stop'
$watchdogSource = Join-Path $PSScriptRoot 'bin\codex-auto-retry.exe'
$mcpSource = Join-Path $PSScriptRoot 'bin\codex-auto-retry-mcp.exe'
$installDir = Join-Path $env:LOCALAPPDATA 'CodexAutoRetry'
$watchdogTarget = Join-Path $installDir 'codex-auto-retry.exe'
$mcpTarget = Join-Path $installDir 'codex-auto-retry-mcp.exe'
$settingsTarget = Join-Path $installDir 'settings.ps1'
$stopSignal = Join-Path $installDir 'stop.signal'
$supervisorStop = Join-Path $installDir 'supervisor.stop'
$statusPath = Join-Path $installDir 'status.json'
$configPath = Join-Path $installDir 'config.json'
$runKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'
$runName = 'CodexAutoRetry'
$environmentName = 'CODEX_APP_SERVER_WS_URL'
. (Join-Path $PSScriptRoot 'environment.ps1')
. (Join-Path $PSScriptRoot 'path-safety.ps1')
[void](Assert-CodexAutoRetryHostPath -Path $installDir)

function Get-RunValue {
    $property = Get-ItemProperty -Path $runKey -Name $runName -ErrorAction SilentlyContinue
    if ($null -eq $property) { return $null }
    return [string]$property.$runName
}

function Test-OwnedStartupValue {
    param([AllowNull()][string]$Value)
    if ([string]::IsNullOrWhiteSpace($Value)) { return $false }
    $trimmed = $Value.Trim()
    if ($trimmed.StartsWith('"')) {
        $closingQuote = $trimmed.IndexOf('"', 1)
        if ($closingQuote -le 1) { return $false }
        $executable = $trimmed.Substring(1, $closingQuote - 1)
    }
    else {
        $executable = ($trimmed -split '[\s\t]', 2)[0]
    }
    return [string]::Equals($executable, $watchdogTarget, [System.StringComparison]::OrdinalIgnoreCase)
}

function Set-SupervisedStartupEntry {
    $existing = Get-RunValue
    if (-not [string]::IsNullOrWhiteSpace($existing) -and -not (Test-OwnedStartupValue $existing)) {
        throw 'The current-user startup entry belongs to another command and was not overwritten.'
    }
    New-Item -Path $runKey -Force | Out-Null
    Set-ItemProperty -Path $runKey -Name $runName -Value ('"{0}" supervise' -f $watchdogTarget)
    $value = Get-RunValue
    if ([string]::IsNullOrWhiteSpace($value) -or $value -notmatch '(?i)\bsupervise\b' -or
        $value -notmatch [regex]::Escape($watchdogTarget)) {
        throw 'The current-user startup entry was not migrated to supervised mode.'
    }
}

function Stop-OwnedProcessPath {
    param([string]$Path)
    @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
        Where-Object { $_.ExecutablePath -and [string]::Equals($_.ExecutablePath, $Path, [System.StringComparison]::OrdinalIgnoreCase) }) |
        ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
}

function Stop-InstalledRuntime {
    $existing = @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
        Where-Object { $_.ExecutablePath -and [string]::Equals($_.ExecutablePath, $watchdogTarget, [System.StringComparison]::OrdinalIgnoreCase) })
    if ($existing.Count -gt 0) {
        New-Item -ItemType File -Force -Path $supervisorStop | Out-Null
        New-Item -ItemType File -Force -Path $stopSignal | Out-Null
        $deadline = (Get-Date).AddSeconds(12)
        do {
            Start-Sleep -Milliseconds 250
            $existing = @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
                Where-Object { $_.ExecutablePath -and [string]::Equals($_.ExecutablePath, $watchdogTarget, [System.StringComparison]::OrdinalIgnoreCase) })
        } while ($existing.Count -gt 0 -and (Get-Date) -lt $deadline)
        if ($existing.Count -gt 0) { $existing | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue } }
    }
    Stop-OwnedProcessPath $mcpTarget
    @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
        Where-Object { $_.CommandLine -and $_.CommandLine.IndexOf($settingsTarget, [System.StringComparison]::OrdinalIgnoreCase) -ge 0 }) |
        ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
    Remove-Item -LiteralPath $stopSignal -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $supervisorStop -Force -ErrorAction SilentlyContinue
}

function Set-ConfigSharedMode {
    param([bool]$Enabled)
    $config = $null
    if (Test-Path -LiteralPath $configPath -PathType Leaf) {
        try {
            $config = Get-Content -Raw -Encoding UTF8 -LiteralPath $configPath | ConvertFrom-Json
        }
        catch {
            throw "The existing Codex Auto Retry configuration is invalid and was not overwritten: $configPath"
        }
    }
    if ($null -eq $config) {
        $config = [pscustomobject]@{}
    }
    if ($null -eq $config.PSObject.Properties['shared_app_server_enabled']) {
        $config | Add-Member -NotePropertyName shared_app_server_enabled -NotePropertyValue $Enabled
    }
    else { $config.shared_app_server_enabled = $Enabled }
    Write-CodexAutoRetryJsonAtomic -Path $configPath -Value $config
}

function Wait-Heartbeat {
    param([int]$ProcessId, [bool]$RequireSharedReady, [switch]$ProcessIsSupervisor)
    $deadline = (Get-Date).AddSeconds(20)
    $status = $null
    do {
        Start-Sleep -Milliseconds 300
        if (Test-Path -LiteralPath $statusPath) {
            try { $status = Get-Content -Raw -Encoding UTF8 -LiteralPath $statusPath | ConvertFrom-Json } catch { $status = $null }
        }
        if ($RequireSharedReady -and $status -and [string]$status.controller_state -eq 'shared_app_server_disabled') { $status = $null }
        $heartbeatMatches = if ($ProcessIsSupervisor) {
            $supervisor = Get-Process -Id $ProcessId -ErrorAction SilentlyContinue
            $worker = if ($status) { Get-CimInstance Win32_Process -Filter ('ProcessId = ' + [int]$status.pid) -ErrorAction SilentlyContinue } else { $null }
            $supervisor -and -not $supervisor.HasExited -and $worker -and $worker.ExecutablePath -and [string]::Equals($worker.ExecutablePath, $watchdogTarget, [System.StringComparison]::OrdinalIgnoreCase)
        } else {
            $status -and [int]$status.pid -eq $ProcessId
        }
    } while ((-not $status -or -not $status.running -or -not $heartbeatMatches -or ($RequireSharedReady -and [string]$status.controller_state -notin @('ready', 'codex_restart_required'))) -and (Get-Date) -lt $deadline)
    if (-not $status -or -not $status.running -or -not $heartbeatMatches) {
        throw "Watchdog did not publish a running heartbeat. Check $installDir\logs\daemon.log"
    }
    if ($RequireSharedReady -and [string]$status.controller_state -notin @('ready', 'codex_restart_required')) {
        throw "Shared app-server health check did not pass. State: $([string]$status.controller_state)"
    }
    return $status
}

if (-not (Test-Path -LiteralPath $watchdogSource -PathType Leaf)) { throw "Built watchdog not found: $watchdogSource" }
if (-not (Test-Path -LiteralPath $mcpSource -PathType Leaf)) { throw "Built MCP server not found: $mcpSource" }
[void](Assert-CodexAutoRetryHostPath -Path $installDir)

$transactionRoot = Join-Path ([System.IO.Path]::GetTempPath()) ('codex-auto-retry-runtime-' + [guid]::NewGuid().ToString('N'))
$backupRoot = Join-Path $transactionRoot 'previous'
$environmentBackupPath = Join-Path $installDir 'environment-backup.json'
$sharedStatePath = Join-Path $installDir 'shared-server.json'
$oldRunValue = $null
$oldConfigBytes = $null
$oldWatchdog = $false
$oldMcp = $false
$oldSettings = $false
$oldEnvironment = $null
$oldEnvironmentPresent = $false
$oldEnvironmentBackupBytes = $null
$oldEnvironmentBackupExisted = $false
$oldSharedStateBytes = $null
$oldSharedStateExisted = $false
$legacyOwnedEndpoint = $null
$environmentChanged = $false
$startedProcess = $null
$installationSucceeded = $false

try {
    New-Item -ItemType Directory -Force -Path $installDir, $backupRoot | Out-Null
    $oldRunValue = Get-RunValue
    $oldEnvironment = [Environment]::GetEnvironmentVariable($environmentName, 'User')
    $oldEnvironmentPresent = $null -ne $oldEnvironment
    $oldEnvironmentBackupExisted = Test-Path -LiteralPath $environmentBackupPath -PathType Leaf
    if ($oldEnvironmentBackupExisted) { $oldEnvironmentBackupBytes = [System.IO.File]::ReadAllBytes($environmentBackupPath) }
    $oldSharedStateExisted = Test-Path -LiteralPath $sharedStatePath -PathType Leaf
    if ($oldSharedStateExisted) { $oldSharedStateBytes = [System.IO.File]::ReadAllBytes($sharedStatePath) }
    if (Test-Path -LiteralPath $configPath -PathType Leaf) { $oldConfigBytes = [System.IO.File]::ReadAllBytes($configPath) }
    if ($oldSharedStateExisted) {
        try {
            $oldSharedState = Get-Content -Raw -Encoding UTF8 -LiteralPath $sharedStatePath | ConvertFrom-Json
            if ([string]$oldSharedState.owner -eq 'codex-auto-retry' -and
                [string]$oldSharedState.endpoint -match '^ws://127\.0\.0\.1:\d+$') {
                $legacyOwnedEndpoint = [string]$oldSharedState.endpoint
            }
        } catch { }
    }
    if (-not $legacyOwnedEndpoint -and $oldRunValue -and
        $oldRunValue.IndexOf($watchdogTarget, [System.StringComparison]::OrdinalIgnoreCase) -ge 0 -and
        (Test-Path -LiteralPath $configPath -PathType Leaf)) {
        try {
            $oldConfig = Get-Content -Raw -Encoding UTF8 -LiteralPath $configPath | ConvertFrom-Json
            if ([bool]$oldConfig.shared_app_server_enabled) {
                $oldPort = Get-CodexAutoRetrySharedAppServerPort -ConfigPath $configPath
                $legacyOwnedEndpoint = 'ws://127.0.0.1:' + $oldPort
            }
        } catch { }
    }
    if (-not $legacyOwnedEndpoint -and $oldRunValue -and
        $oldRunValue.IndexOf($watchdogTarget, [System.StringComparison]::OrdinalIgnoreCase) -ge 0) {
        $legacyOwnedEndpoint = @('ws://127.0.0.1:49621', 'ws://127.0.0.1:49321')
    }
    foreach ($pair in @(
        @($watchdogTarget, (Join-Path $backupRoot 'codex-auto-retry.exe')),
        @($mcpTarget, (Join-Path $backupRoot 'codex-auto-retry-mcp.exe')),
        @($settingsTarget, (Join-Path $backupRoot 'settings.ps1'))
    )) {
        if (Test-Path -LiteralPath $pair[0] -PathType Leaf) {
            Copy-Item -LiteralPath $pair[0] -Destination $pair[1] -Force
            if ($pair[0] -eq $watchdogTarget) { $oldWatchdog = $true }
            if ($pair[0] -eq $mcpTarget) { $oldMcp = $true }
            if ($pair[0] -eq $settingsTarget) { $oldSettings = $true }
        }
    }

    Stop-InstalledRuntime
    if (-not $EnableSharedAppServer) {
        # A previous release may have installed the endpoint by default. Remove
        # only the recorded value, or a legacy value proven to belong to this
        # installation, before the new fail-open runtime starts.
        $null = Restore-CodexAutoRetrySharedEnvironment -DataDir $installDir -LegacyOwnedEndpoint $legacyOwnedEndpoint
    }

    $candidateRoot = Join-Path $transactionRoot 'candidate'
    New-Item -ItemType Directory -Force -Path $candidateRoot | Out-Null
    $candidateWatchdog = Join-Path $candidateRoot 'codex-auto-retry.exe'
    $candidateMcp = Join-Path $candidateRoot 'codex-auto-retry-mcp.exe'
    Copy-Item -LiteralPath $watchdogSource -Destination $candidateWatchdog -Force
    Copy-Item -LiteralPath $mcpSource -Destination $candidateMcp -Force
    if ((Get-FileHash -LiteralPath $watchdogSource -Algorithm SHA256).Hash -ne (Get-FileHash -LiteralPath $candidateWatchdog -Algorithm SHA256).Hash -or
        (Get-FileHash -LiteralPath $mcpSource -Algorithm SHA256).Hash -ne (Get-FileHash -LiteralPath $candidateMcp -Algorithm SHA256).Hash) {
        throw 'Candidate binary verification failed.'
    }
    Copy-Item -LiteralPath $candidateWatchdog -Destination $watchdogTarget -Force
    Copy-Item -LiteralPath $candidateMcp -Destination $mcpTarget -Force
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'source\ui\settings.ps1') -Destination $settingsTarget -Force
    Set-SupervisedStartupEntry

    Set-ConfigSharedMode ([bool]$EnableSharedAppServer)
    Remove-Item -LiteralPath $stopSignal -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $supervisorStop -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $statusPath -Force -ErrorAction SilentlyContinue
    $startedProcess = Start-Process -FilePath $watchdogTarget -ArgumentList @('supervise') -WorkingDirectory $installDir -WindowStyle Hidden -PassThru
    $status = Wait-Heartbeat -ProcessId $startedProcess.Id -RequireSharedReady:$EnableSharedAppServer -ProcessIsSupervisor

    if ($EnableSharedAppServer) {
        $environment = Set-CodexAutoRetrySharedEnvironment -DataDir $installDir -ConfigPath $configPath
        $environmentChanged = $true
    }
    $installationSucceeded = $true
    [pscustomobject]@{
        Installed = $true
        Running = $status.running
        Version = $status.version
        PID = $status.pid
        Paused = [bool]$status.paused
        MCPServerInstalled = Test-Path -LiteralPath $mcpTarget
        InstallDirectory = $installDir
        Startup = 'Current user sign-in'
        SharedAppServerEnabled = [bool]$EnableSharedAppServer
        SharedAppServer = if ($EnableSharedAppServer) { $environment.Value } else { $null }
        EnvironmentChanged = if ($EnableSharedAppServer) { $environment.Changed } else { $false }
        CodexRestartRequired = [string]$status.controller_state -eq 'codex_restart_required'
    }
}
catch {
    $failure = $_
    try {
        if ($startedProcess -and -not $startedProcess.HasExited) { Stop-InstalledRuntime }
        $sharedStateChanged = Test-Path -LiteralPath $sharedStatePath -PathType Leaf
        if ($sharedStateChanged -and ((-not $oldSharedStateExisted) -or
            [Convert]::ToBase64String([System.IO.File]::ReadAllBytes($sharedStatePath)) -ne [Convert]::ToBase64String($oldSharedStateBytes))) {
            $null = Stop-CodexAutoRetrySharedServerIfUnused -DataDir $installDir
        }
        if ($environmentChanged) { $null = Restore-CodexAutoRetrySharedEnvironment -DataDir $installDir }
        if ($oldEnvironmentPresent) { [Environment]::SetEnvironmentVariable($environmentName, $oldEnvironment, 'User') }
        else { Remove-CodexAutoRetryUserEnvironmentValue -Name $environmentName }
        if ($oldRunValue) { New-Item -Path $runKey -Force | Out-Null; Set-ItemProperty -Path $runKey -Name $runName -Value $oldRunValue }
        else { Remove-ItemProperty -Path $runKey -Name $runName -ErrorAction SilentlyContinue }
        foreach ($pair in @(
            @($watchdogTarget, (Join-Path $backupRoot 'codex-auto-retry.exe'), $oldWatchdog),
            @($mcpTarget, (Join-Path $backupRoot 'codex-auto-retry-mcp.exe'), $oldMcp),
            @($settingsTarget, (Join-Path $backupRoot 'settings.ps1'), $oldSettings)
        )) {
            if ($pair[2] -and (Test-Path -LiteralPath $pair[1] -PathType Leaf)) { Copy-Item -LiteralPath $pair[1] -Destination $pair[0] -Force }
            elseif (-not $pair[2]) { Remove-Item -LiteralPath $pair[0] -Force -ErrorAction SilentlyContinue }
        }
        if ($null -ne $oldConfigBytes) { [System.IO.File]::WriteAllBytes($configPath, $oldConfigBytes) }
        else { Remove-Item -LiteralPath $configPath -Force -ErrorAction SilentlyContinue }
        if ($oldEnvironmentBackupExisted) { [System.IO.File]::WriteAllBytes($environmentBackupPath, $oldEnvironmentBackupBytes) }
        else { Remove-Item -LiteralPath $environmentBackupPath -Force -ErrorAction SilentlyContinue }
        if ($oldSharedStateExisted) { [System.IO.File]::WriteAllBytes($sharedStatePath, $oldSharedStateBytes) }
        else { Remove-Item -LiteralPath $sharedStatePath -Force -ErrorAction SilentlyContinue }
        Send-CodexAutoRetryEnvironmentChange
    }
    catch { Write-Warning 'Automatic runtime rollback was incomplete; user task data was not deleted.' }
    throw $failure
}
finally {
    if (Test-Path -LiteralPath $transactionRoot -PathType Container) {
        Remove-Item -LiteralPath $transactionRoot -Recurse -Force -ErrorAction SilentlyContinue
    }
}
