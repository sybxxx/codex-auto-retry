# Shared process verification used by status.ps1 and startup-manager.ps1.
# Keep this read-only: it must never stop a process or mutate the endpoint.

function Get-CodexAutoRetryStatusProperty {
    param(
        [AllowNull()]$Status,
        [Parameter(Mandatory = $true)][string]$Name,
        $Default = $null
    )
    if ($null -eq $Status) { return $Default }
    $property = $Status.PSObject.Properties[$Name]
    if ($null -eq $property) { return $Default }
    return $property.Value
}

function Get-CodexAutoRetryStatusCompatibility {
    param(
        [AllowNull()]$Status,
        [bool]$ReadFailed = $false
    )
    if ($ReadFailed) { return 'status_unreadable' }
    if ($null -eq $Status) { return 'status_missing' }
    foreach ($name in @(
        'shared_app_server_memory_usage_mb',
        'shared_app_server_memory_limit_mb',
        'shared_app_server_memory_guard_triggered',
        'retry_safety_warning'
    )) {
        if ($null -eq $Status.PSObject.Properties[$name]) { return 'legacy_status_schema' }
    }
    return 'current'
}

function Get-CodexAutoRetryStatusCompatibilityMessage {
    param([string]$Status)
    switch ($Status) {
        'legacy_status_schema' { return 'Legacy status format: readable, some new metrics are unavailable' }
        'status_unreadable' { return 'Status file is unreadable' }
        'status_missing' { return 'Status file has not been generated' }
        default { return 'Status format matches the current manager' }
    }
}

function ConvertTo-CodexAutoRetryDateTimeOffset {
    param([AllowNull()]$Value)
    try {
        if ($Value -is [DateTimeOffset]) { return [DateTimeOffset]$Value }
        if ($Value -is [DateTime]) { return [DateTimeOffset]$Value }
        if ([string]::IsNullOrWhiteSpace([string]$Value)) { return $null }
        return [DateTimeOffset]::Parse([string]$Value)
    }
    catch {
        return $null
    }
}

function Test-CodexAutoRetrySharedServerEndpoint {
    param(
        [AllowNull()][string]$Endpoint,
        [int]$ExpectedPort = 0
    )
    if ([string]::IsNullOrWhiteSpace($Endpoint) -or $Endpoint -notmatch '^ws://127\.0\.0\.1:(\d{1,5})$') {
        return $false
    }
    $port = [int]$matches[1]
    return $port -ge 1024 -and $port -le 65535 -and ($ExpectedPort -le 0 -or $port -eq $ExpectedPort)
}

function Test-CodexAutoRetryTcpEndpoint {
    param([Parameter(Mandatory = $true)][string]$Endpoint)
    if ($Endpoint -notmatch '^ws://127\.0\.0\.1:(\d{1,5})$') { return $false }
    $port = [int]$matches[1]
    $client = New-Object System.Net.Sockets.TcpClient
    try {
        $task = $client.ConnectAsync('127.0.0.1', $port)
        if (-not $task.Wait(250)) { return $false }
        return $client.Connected
    }
    catch {
        return $false
    }
    finally {
        $client.Dispose()
    }
}

function Get-CodexAutoRetrySharedServerStatus {
    param(
        [AllowNull()]$State,
        [int]$ExpectedPort = 0,
        [int]$CreationToleranceSeconds = 15
    )
    if ($null -eq $State) {
        return [pscustomobject][ordered]@{ Status = 'missing'; Reason = 'state_missing'; PID = $null; Endpoint = $null }
    }
    try {
        $processId = if ($null -ne $State.PSObject.Properties['pid']) { [int]$State.pid } else { 0 }
        $endpoint = if ($null -ne $State.PSObject.Properties['endpoint']) { [string]$State.endpoint } else { '' }
        $executable = if ($null -ne $State.PSObject.Properties['executable']) { [string]$State.executable } else { '' }
        $recordedHash = if ($null -ne $State.PSObject.Properties['executable_hash']) { ([string]$State.executable_hash).ToLowerInvariant() } else { '' }
        $owner = if ($null -ne $State.PSObject.Properties['owner']) { [string]$State.owner } else { '' }
        if ($owner -ne 'codex-auto-retry' -or $processId -le 0 -or
            -not (Test-CodexAutoRetrySharedServerEndpoint -Endpoint $endpoint -ExpectedPort $ExpectedPort) -or
            [string]::IsNullOrWhiteSpace($executable) -or $recordedHash -notmatch '^[0-9a-f]{64}$') {
            return [pscustomobject][ordered]@{ Status = 'invalid'; Reason = 'state_validation_failed'; PID = $processId; Endpoint = $endpoint }
        }
        if (-not (Test-Path -LiteralPath $executable -PathType Leaf)) {
            return [pscustomobject][ordered]@{ Status = 'invalid'; Reason = 'executable_missing'; PID = $processId; Endpoint = $endpoint }
        }
        try { $actualHash = (Get-FileHash -LiteralPath $executable -Algorithm SHA256).Hash.ToLowerInvariant() }
        catch { return [pscustomobject][ordered]@{ Status = 'unknown'; Reason = 'executable_hash_unavailable'; PID = $processId; Endpoint = $endpoint } }
        if ($actualHash -ne $recordedHash) {
            return [pscustomobject][ordered]@{ Status = 'invalid'; Reason = 'executable_hash_mismatch'; PID = $processId; Endpoint = $endpoint }
        }
        try { $process = Get-CimInstance Win32_Process -Filter ('ProcessId = ' + $processId) -ErrorAction Stop }
        catch { return [pscustomobject][ordered]@{ Status = 'unknown'; Reason = 'process_query_failed'; PID = $processId; Endpoint = $endpoint } }
        if ($null -eq $process) {
            return [pscustomobject][ordered]@{ Status = 'stale'; Reason = 'process_missing'; PID = $processId; Endpoint = $endpoint }
        }
        if (-not $process.ExecutablePath -or
            -not [string]::Equals([string]$process.ExecutablePath, $executable, [System.StringComparison]::OrdinalIgnoreCase)) {
            return [pscustomobject][ordered]@{ Status = 'unknown'; Reason = 'executable_mismatch'; PID = $processId; Endpoint = $endpoint }
        }
        $commandLine = [string]$process.CommandLine
        if ([string]::IsNullOrWhiteSpace($commandLine) -or
            $commandLine -notmatch '(?i)(?:^|\s)app-server(?:\s|$)' -or
            $commandLine.IndexOf($endpoint, [System.StringComparison]::OrdinalIgnoreCase) -lt 0) {
            return [pscustomobject][ordered]@{ Status = 'unknown'; Reason = 'command_line_mismatch'; PID = $processId; Endpoint = $endpoint }
        }
        $startedAt = ConvertTo-CodexAutoRetryDateTimeOffset $State.started_at
        $createdAt = ConvertTo-CodexAutoRetryDateTimeOffset $process.CreationDate
        if ($null -eq $startedAt -or $null -eq $createdAt) {
            return [pscustomobject][ordered]@{ Status = 'unknown'; Reason = 'creation_time_unavailable'; PID = $processId; Endpoint = $endpoint }
        }
        $delta = ($createdAt.ToUniversalTime() - $startedAt.ToUniversalTime()).Duration()
        if ($delta -gt [TimeSpan]::FromSeconds($CreationToleranceSeconds)) {
            return [pscustomobject][ordered]@{ Status = 'unknown'; Reason = 'creation_time_mismatch'; PID = $processId; Endpoint = $endpoint }
        }
        if (-not (Test-CodexAutoRetryTcpEndpoint -Endpoint $endpoint)) {
            return [pscustomobject][ordered]@{ Status = 'stale'; Reason = 'endpoint_not_listening'; PID = $processId; Endpoint = $endpoint }
        }
        return [pscustomobject][ordered]@{ Status = 'live'; Reason = 'verified'; PID = $processId; Endpoint = $endpoint }
    }
    catch {
        return [pscustomobject][ordered]@{ Status = 'unknown'; Reason = 'verification_failed'; PID = $null; Endpoint = $null }
    }
}
