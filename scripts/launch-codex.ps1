[CmdletBinding()]
param(
    [string]$DataDir = (Join-Path $env:LOCALAPPDATA 'CodexAutoRetry'),
    [switch]$Official,
    [switch]$CheckOnly
)

Set-StrictMode -Version 2
. (Join-Path $PSScriptRoot 'shared-server-status.ps1')
. (Join-Path $PSScriptRoot 'path-safety.ps1')

function Read-CodexLaunchJson {
    param([string]$Path)
    try {
        if ((Get-Item -LiteralPath $Path -ErrorAction Stop).Length -gt 1MB) { return $null }
        return Get-Content -LiteralPath $Path -Raw -Encoding UTF8 -ErrorAction Stop | ConvertFrom-Json -ErrorAction Stop
    } catch { return $null }
}

function Test-CodexLaunchWebSocket {
    param([string]$Endpoint)
    if (-not (Test-CodexAutoRetrySharedServerEndpoint $Endpoint)) { return $false }
    $socket = New-Object System.Net.WebSockets.ClientWebSocket
    $socket.Options.Proxy = $null
    $cancel = New-Object System.Threading.CancellationTokenSource
    try {
        $cancel.CancelAfter(1500)
        $task = $socket.ConnectAsync([Uri]$Endpoint, $cancel.Token)
        if (-not $task.Wait(2000)) { return $false }
        if ($socket.State -ne [System.Net.WebSockets.WebSocketState]::Open) { return $false }
        # A listening WebSocket is not proof that the app-server RPC is usable.
        $request = [Text.Encoding]::UTF8.GetBytes('{"id":1,"method":"initialize","params":{"clientInfo":{"name":"codex_auto_retry_launch","version":"1"},"capabilities":{"experimentalApi":true}}}')
        $segment = [ArraySegment[byte]]::new($request)
        $send = $socket.SendAsync($segment, [Net.WebSockets.WebSocketMessageType]::Text, $true, $cancel.Token)
        if (-not $send.Wait(2000)) { return $false }
        $buffer = New-Object byte[] 8192
        for ($message = 0; $message -lt 8; $message++) {
            $content = New-Object IO.MemoryStream
            try {
                do {
                    $receive = $socket.ReceiveAsync([ArraySegment[byte]]::new($buffer), $cancel.Token)
                    if (-not $receive.Wait(2000)) { return $false }
                    $part = $receive.Result
                    if ($part.MessageType -ne [Net.WebSockets.WebSocketMessageType]::Text -or $content.Length + $part.Count -gt 65536) { return $false }
                    $content.Write($buffer, 0, $part.Count)
                } while (-not $part.EndOfMessage)
                $reply = [Text.Encoding]::UTF8.GetString($content.ToArray()) | ConvertFrom-Json -ErrorAction Stop
                if ((Get-CodexAutoRetryStatusProperty $reply 'id' 0) -ne 1) { continue }
                return $null -eq (Get-CodexAutoRetryStatusProperty $reply 'error') -and
                    $null -ne (Get-CodexAutoRetryStatusProperty $reply 'result')
            } finally { $content.Dispose() }
        }
        return $false
    } catch { return $false }
    finally { $socket.Abort(); $socket.Dispose(); $cancel.Dispose() }
}

function Test-CodexLaunchWorker {
    param($Status, [string]$Runtime)
    try {
        $workerId = [int](Get-CodexAutoRetryStatusProperty $Status 'pid' 0)
        if ($workerId -le 0) { return $false }
        $expected = Join-Path $Runtime 'codex-auto-retry.exe'
        if (-not (Test-Path -LiteralPath $expected -PathType Leaf)) { return $false }
        $worker = Get-CimInstance Win32_Process -Filter ('ProcessId = ' + $workerId) -ErrorAction Stop
        if ($null -eq $worker -or -not [string]::Equals([string]$worker.ExecutablePath, $expected, [StringComparison]::OrdinalIgnoreCase)) { return $false }
        if ([string]$worker.CommandLine -notmatch '(?i)(?:^|\s)run(?:\s|$)') { return $false }
        $started = ConvertTo-CodexAutoRetryDateTimeOffset (Get-CodexAutoRetryStatusProperty $Status 'started_at')
        $created = ConvertTo-CodexAutoRetryDateTimeOffset $worker.CreationDate
        if ($null -eq $started -or $null -eq $created) { return $false }
        # The daemon timestamp follows bounded startup preparation, not process creation.
        $startupDelay = ($started - $created).TotalSeconds
        return $startupDelay -ge -2 -and $startupDelay -le 60
    } catch { return $false }
}

function Get-CodexLaunchRoute {
    param([string]$Runtime, [switch]$OfficialOnly)
    $result = [pscustomobject]@{ Mode = 'official'; Reason = 'official_requested'; Endpoint = $null }
    if ($OfficialOnly) { return $result }
    $result.Reason = 'runtime_path_unsafe'
    if (Get-CodexAutoRetryRedirectedPath -Path $Runtime) { return $result }
    $status = Read-CodexLaunchJson (Join-Path $Runtime 'status.json')
    $config = Read-CodexLaunchJson (Join-Path $Runtime 'config.json')
    $result.Reason = 'legacy_or_missing_status'
    if ((Get-CodexAutoRetryStatusProperty $status 'desktop_launch_mode' '') -ne 'process_scoped') { return $result }
    $result.Reason = 'shared_disabled'
    if ((Get-CodexAutoRetryStatusProperty $config 'shared_app_server_enabled' $false) -ne $true) { return $result }
    $result.Reason = 'worker_not_fresh'
    $heartbeat = ConvertTo-CodexAutoRetryDateTimeOffset (Get-CodexAutoRetryStatusProperty $status 'last_scan_at')
    if ($null -eq $heartbeat -or (Get-CodexAutoRetryStatusProperty $status 'running' $false) -ne $true) { return $result }
    $age = ([DateTimeOffset]::UtcNow - $heartbeat.ToUniversalTime()).TotalSeconds
    if ($age -lt 0 -or $age -gt 15 -or -not (Test-CodexLaunchWorker $status $Runtime)) { return $result }
    $result.Reason = 'shared_not_verified'
    try {
        $state = Read-CodexLaunchJson (Join-Path $Runtime 'shared-server.json')
        $port = [int](Get-CodexAutoRetryStatusProperty $config 'shared_app_server_port' 0)
        if ($port -lt 1024 -or $port -gt 65535) { return $result }
        $shared = Get-CodexAutoRetrySharedServerStatus -State $state -ExpectedPort $port
        if ($shared.Status -ne 'live') { return $result }
        $result.Reason = 'shared_handshake_failed'
        if (-not (Test-CodexLaunchWebSocket $shared.Endpoint)) { return $result }
        $result.Mode = 'shared'; $result.Reason = 'verified'; $result.Endpoint = $shared.Endpoint
    } catch { $result.Reason = 'shared_verification_failed' }
    return $result
}

function Get-CodexDesktopExecutable {
    $packages = @(Get-AppxPackage -Name 'OpenAI.Codex' -ErrorAction Stop)
    foreach ($package in ($packages | Sort-Object Version -Descending)) {
        $exe = Join-Path ([string]$package.InstallLocation) 'app\ChatGPT.exe'
        if (Test-Path -LiteralPath $exe -PathType Leaf) { return $exe }
    }
    throw 'Cannot find the current-user OpenAI.Codex package executable. Install or repair Codex first.'
}

function Invoke-CodexLaunchRecovery {
    param([string]$Runtime, [string]$Executable)
    $route = Get-CodexLaunchRoute $Runtime
    if ($route.Mode -eq 'shared') { return $route }
    $clock = [Diagnostics.Stopwatch]::StartNew()
    for ($attempt = 1; $attempt -le 2 -and $clock.Elapsed.TotalSeconds -lt 24; $attempt++) {
        Assert-CodexDesktopStopped $Executable
        $config = Read-CodexLaunchJson (Join-Path $Runtime 'config.json')
        $status = Read-CodexLaunchJson (Join-Path $Runtime 'status.json')
        if ((Get-CodexAutoRetryStatusProperty $config 'shared_app_server_requested' $false) -ne $true -or
            $null -eq $status -or $null -eq $status.PSObject.Properties['shared_app_server_requested'] -or
            (Get-CodexAutoRetryStatusProperty $status 'running' $false) -ne $true -or
            -not (Test-CodexLaunchWorker $status $Runtime)) { return $route }
        $heartbeat = ConvertTo-CodexAutoRetryDateTimeOffset (Get-CodexAutoRetryStatusProperty $status 'last_scan_at')
        if ($null -eq $heartbeat -or ([DateTimeOffset]::UtcNow - $heartbeat).TotalSeconds -gt 15 -or $heartbeat -gt [DateTimeOffset]::UtcNow) { return $route }
        $id = [Guid]::NewGuid().ToString('N')
        $requestPath = Join-Path $Runtime 'shared-recovery-request.json'
        $temporary = Join-Path $Runtime ('shared-recovery-' + $id + '.tmp')
        try {
            $request = @{ id = $id; expires_at = [DateTimeOffset]::UtcNow.AddSeconds(10).ToString('o') }
            [IO.File]::WriteAllText($temporary, ($request | ConvertTo-Json -Compress), [Text.UTF8Encoding]::new($false))
            if (Test-Path -LiteralPath $requestPath) { [IO.File]::Replace($temporary, $requestPath, [NullString]::Value) }
            else { [IO.File]::Move($temporary, $requestPath) }
            $until = [DateTimeOffset]::UtcNow.AddSeconds(10)
            do {
                Start-Sleep -Milliseconds 200
                $result = Read-CodexLaunchJson (Join-Path $Runtime 'shared-recovery-result.json')
                if ((Get-CodexAutoRetryStatusProperty $result 'id' '') -eq $id) { break }
            } while ([DateTimeOffset]::UtcNow -lt $until -and $clock.Elapsed.TotalSeconds -lt 24)
            Write-CodexLaunchResult $Runtime 'official' ('recovery_attempt_' + $attempt) 'checked'
            $route = Get-CodexLaunchRoute $Runtime
            if ($route.Mode -eq 'shared') { return $route }
            if ((Get-CodexAutoRetryStatusProperty $result 'reason' '') -in @('manual_recovery_required', 'preference_disabled', 'desktop_already_running', 'cleanup_not_safe')) { return $route }
        } catch {
            $route.Reason = 'recovery_request_failed'
            return $route
        } finally {
            if (Test-Path -LiteralPath $temporary) { Remove-Item -LiteralPath $temporary -Force }
            $pending = Read-CodexLaunchJson $requestPath
            if ((Get-CodexAutoRetryStatusProperty $pending 'id' '') -eq $id) { Remove-Item -LiteralPath $requestPath -Force -ErrorAction SilentlyContinue }
        }
    }
    return $route
}

function Assert-CodexDesktopStopped {
    param([string]$Executable)
    try {
        $processes = @(Get-CimInstance Win32_Process -Filter "Name = 'ChatGPT.exe' OR Name = 'Codex.exe'" -ErrorAction Stop)
        foreach ($process in $processes) {
            if (-not $process.ExecutablePath -or $process.Name -ieq 'ChatGPT.exe' -or
                [string]::Equals([string]$process.ExecutablePath, $Executable, [StringComparison]::OrdinalIgnoreCase) -or
                [string]$process.ExecutablePath -match '(?i)\\app\\Codex\.exe$') {
                throw 'Codex may already be running. Fully exit Codex before using this launcher.'
            }
        }
    } catch { throw 'Cannot confirm Codex is fully closed. Fully exit Codex and try again. No process was stopped.' }
}

function New-CodexDesktopStartInfo {
    param([string]$Executable, $Route)
    $info = New-Object System.Diagnostics.ProcessStartInfo
    $info.FileName = $Executable
    $info.WorkingDirectory = Split-Path -Parent $Executable
    $info.UseShellExecute = $false
    # Edit only the child copy. Explorer and the current shell keep their environment.
    $info.EnvironmentVariables.Remove('CODEX_APP_SERVER_WS_URL')
    if ($Route.Mode -eq 'shared') { $info.EnvironmentVariables['CODEX_APP_SERVER_WS_URL'] = $Route.Endpoint }
    return $info
}

function Write-CodexLaunchResult {
    param([string]$Runtime, [string]$Mode, [string]$Reason, [string]$Outcome)
    $temporary = $null
    try {
        if (-not (Test-Path -LiteralPath $Runtime -PathType Container) -or (Get-CodexAutoRetryRedirectedPath -Path $Runtime)) { return }
        $target = Join-Path $Runtime 'desktop-launch.json'
        if (Get-CodexAutoRetryRedirectedPath -Path $target) { return }
        $temporary = Join-Path $Runtime ('desktop-launch.' + [Guid]::NewGuid().ToString('N') + '.tmp')
        $record = @{ timestamp = [DateTimeOffset]::UtcNow.ToString('o'); mode = $Mode; reason = $Reason; outcome = $Outcome }
        [IO.File]::WriteAllText($temporary, ($record | ConvertTo-Json -Compress), (New-Object Text.UTF8Encoding($false)))
        if (Test-Path -LiteralPath $target) { [IO.File]::Replace($temporary, $target, [NullString]::Value) }
        else { [IO.File]::Move($temporary, $target) }
    } catch { }
    finally { if ($temporary -and (Test-Path -LiteralPath $temporary)) { Remove-Item -LiteralPath $temporary -Force -ErrorAction SilentlyContinue } }
}

function Enter-CodexLaunchLock {
    $lock = New-Object Threading.Mutex($false, 'Local\CodexAutoRetry.DesktopLaunch')
    $acquired = $false
    try {
        try { $acquired = $lock.WaitOne(0) }
        catch [Threading.AbandonedMutexException] { $acquired = $true }
        if (-not $acquired) { throw 'Another safe Codex launch is already in progress. No second instance was started.' }
        return $lock
    } catch { $lock.Dispose(); throw }
}

if ($MyInvocation.InvocationName -eq '.') { return }
$route = [pscustomobject]@{ Mode = 'official'; Reason = 'launch_not_prepared'; Endpoint = $null }
$launchLock = $null
try {
    if (-not $CheckOnly) { $launchLock = Enter-CodexLaunchLock }
    $route = Get-CodexLaunchRoute -Runtime $DataDir -OfficialOnly:$Official
    $exe = Get-CodexDesktopExecutable
    Assert-CodexDesktopStopped -Executable $exe
    if ($CheckOnly) {
        [pscustomobject]@{ Mode = $route.Mode; Reason = $route.Reason; Executable = $exe; CanLaunch = $true }
        return
    }
    if (-not $Official -and $route.Mode -ne 'shared') {
        $route = Invoke-CodexLaunchRecovery -Runtime $DataDir -Executable $exe
    }
    # Recheck immediately before creating the child; never delegate to a shell broker.
    $route = Get-CodexLaunchRoute -Runtime $DataDir -OfficialOnly:$Official
    Assert-CodexDesktopStopped -Executable $exe
    $child = [Diagnostics.Process]::Start((New-CodexDesktopStartInfo $exe $route))
    try {
        # Keep concurrent clicks out until the process is visible to the next probe.
        if ($child.WaitForExit(1000)) { throw 'Codex exited immediately after launch. No automatic restart was attempted.' }
    } finally { $child.Dispose() }
    Write-CodexLaunchResult $DataDir $route.Mode $route.Reason 'launch_requested'
} catch {
    if ($CheckOnly) { throw }
    Write-CodexLaunchResult $DataDir $route.Mode 'launch_refused_or_failed' 'failed'
    Add-Type -AssemblyName System.Windows.Forms
    [void][Windows.Forms.MessageBox]::Show($_.Exception.Message, 'Codex safe launcher', 'OK', 'Warning')
    exit 1
} finally {
    if ($null -ne $launchLock) { $launchLock.ReleaseMutex(); $launchLock.Dispose() }
}
