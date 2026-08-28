function Test-CodexAutoRetryEnvironmentValue {
    param([AllowNull()][string]$Left, [AllowNull()][string]$Right)
    if ($null -eq $Left -or $null -eq $Right) { return $null -eq $Left -and $null -eq $Right }
    return [string]::Equals($Left, $Right, [System.StringComparison]::OrdinalIgnoreCase)
}

function Remove-CodexAutoRetryUserEnvironmentValue {
    param([Parameter(Mandatory = $true)][string]$Name)
    # PowerShell/.NET versions can serialize a null User value as an empty
    # registry value. Delete the value explicitly so cleanup leaves no stale
    # endpoint key behind.
    [Environment]::SetEnvironmentVariable($Name, $null, 'User')
    Remove-ItemProperty -Path 'HKCU:\Environment' -Name $Name -ErrorAction SilentlyContinue
    Remove-Item -Path "Env:$Name" -ErrorAction SilentlyContinue
}

function Send-CodexAutoRetryEnvironmentChange {
    # Do not compile a new P/Invoke type here. Codex can expose a very large
    # inherited environment block, and Add-Type starts a compiler process whose
    # CreateProcess call then fails before the installer can finish. Use the
    # Windows-provided helper from a minimal environment instead. Broadcasting
    # is advisory: the registry write is the durable operation, and a failure to
    # notify already-running applications must never make installation roll back.
    $rundll32 = Join-Path $env:WINDIR 'System32\rundll32.exe'
    if (-not (Test-Path -LiteralPath $rundll32 -PathType Leaf)) { return }
    try {
        $startInfo = [System.Diagnostics.ProcessStartInfo]::new()
        $startInfo.FileName = $rundll32
        $startInfo.Arguments = 'user32.dll,UpdatePerUserSystemParameters'
        $startInfo.UseShellExecute = $false
        $startInfo.CreateNoWindow = $true
        # A minimal environment avoids inheriting Codex's oversized process
        # environment while retaining the system root used by rundll32.
        $startInfo.EnvironmentVariables.Clear()
        $startInfo.EnvironmentVariables['SystemRoot'] = [string]$env:SystemRoot
        $startInfo.EnvironmentVariables['WINDIR'] = [string]$env:WINDIR
        $process = [System.Diagnostics.Process]::Start($startInfo)
        if ($process) {
            [void]$process.WaitForExit(5000)
            $process.Dispose()
        }
    }
    catch {
        # Environment propagation is best effort and must not break the
        # transaction or expose the underlying environment to the UI.
    }
}

function Write-CodexAutoRetryJsonAtomic {
    param([Parameter(Mandatory = $true)][string]$Path, [Parameter(Mandatory = $true)]$Value)
    $temporary = $Path + '.tmp-' + [guid]::NewGuid().ToString('N')
    try {
        [System.IO.File]::WriteAllText(
            $temporary,
            (($Value | ConvertTo-Json -Depth 8) + [Environment]::NewLine),
            [System.Text.UTF8Encoding]::new($false)
        )
        Move-Item -LiteralPath $temporary -Destination $Path -Force
    }
    finally {
        Remove-Item -LiteralPath $temporary -Force -ErrorAction SilentlyContinue
    }
}

function Disable-CodexAutoRetrySharedMode {
    param([Parameter(Mandatory = $true)][string]$DataDir)
    $configPath = Join-Path $DataDir 'config.json'
    if (-not (Test-Path -LiteralPath $configPath -PathType Leaf)) { return $false }
    try { $config = Get-Content -Raw -Encoding UTF8 -LiteralPath $configPath | ConvertFrom-Json }
    catch { throw "The shared app-server configuration is invalid: $configPath" }
    if ($null -eq $config.PSObject.Properties['shared_app_server_enabled']) {
        $config | Add-Member -NotePropertyName shared_app_server_enabled -NotePropertyValue $false
    }
    else { $config.shared_app_server_enabled = $false }
    Write-CodexAutoRetryJsonAtomic -Path $configPath -Value $config
    return $true
}

function Get-CodexAutoRetrySharedAppServerPort {
    param([Parameter(Mandatory = $true)][string]$ConfigPath)
    $defaultPort = 49621
    if (-not (Test-Path -LiteralPath $ConfigPath -PathType Leaf)) { return $defaultPort }
    try {
        $config = Get-Content -Raw -Encoding UTF8 -LiteralPath $ConfigPath | ConvertFrom-Json
        $port = [int]$config.shared_app_server_port
        if ($port -ge 1024 -and $port -le 65535) { return $port }
    }
    catch { }
    return $defaultPort
}

function Set-CodexAutoRetrySharedEnvironment {
    param(
        [Parameter(Mandatory = $true)][string]$DataDir,
        [Parameter(Mandatory = $true)][string]$ConfigPath,
        [string]$EnvironmentName = 'CODEX_APP_SERVER_WS_URL',
        [switch]$SkipBroadcast
    )
    $name = $EnvironmentName
    $backupPath = Join-Path $DataDir 'environment-backup.json'
    $port = Get-CodexAutoRetrySharedAppServerPort -ConfigPath $ConfigPath
    $desired = "ws://127.0.0.1:$port"
    $current = [Environment]::GetEnvironmentVariable($name, 'User')
    $backup = $null
    if (Test-Path -LiteralPath $backupPath -PathType Leaf) {
        try { $backup = Get-Content -Raw -Encoding UTF8 -LiteralPath $backupPath | ConvertFrom-Json }
        catch { throw "The saved $name backup is invalid: $backupPath" }
        if ([int]$backup.schema_version -ne 1 -or [string]$backup.name -ne $name) {
            throw "The saved $name backup is not recognized: $backupPath"
        }
        $expected = if ([bool]$backup.previous_present) { [string]$backup.previous_value } else { $null }
        $installed = [string]$backup.installed_value
        if (-not (Test-CodexAutoRetryEnvironmentValue $current $installed) -and
            -not (Test-CodexAutoRetryEnvironmentValue $current $expected) -and
            -not (Test-CodexAutoRetryEnvironmentValue $current $desired)) {
            throw "$name already has a different user value. It was not overwritten: $current"
        }
    }
    else {
        if ($null -ne $current -and -not (Test-CodexAutoRetryEnvironmentValue $current $desired)) {
            throw "$name already has a different user value. It was not overwritten: $current"
        }
        $backup = [pscustomobject][ordered]@{
            schema_version = 1
            name = $name
            previous_present = $null -ne $current
            previous_value = if ($null -ne $current) { $current } else { '' }
            installed_value = $desired
            recorded_at = [DateTime]::UtcNow.ToString('o')
        }
    }
    $backupExisted = Test-Path -LiteralPath $backupPath -PathType Leaf
    $backupBytes = if ($backupExisted) { [System.IO.File]::ReadAllBytes($backupPath) } else { $null }
    $backup.installed_value = $desired
    try {
        [Environment]::SetEnvironmentVariable($name, $desired, 'User')
        Set-Item -Path "Env:$name" -Value $desired
        Write-CodexAutoRetryJsonAtomic -Path $backupPath -Value $backup
        if (-not $SkipBroadcast) { Send-CodexAutoRetryEnvironmentChange }
    }
    catch {
        [Environment]::SetEnvironmentVariable($name, $current, 'User')
        if ($null -eq $current) { Remove-CodexAutoRetryUserEnvironmentValue -Name $name }
        else { Set-Item -Path "Env:$name" -Value $current }
        if ($backupExisted) { [System.IO.File]::WriteAllBytes($backupPath, $backupBytes) }
        else { Remove-Item -LiteralPath $backupPath -Force -ErrorAction SilentlyContinue }
        if (-not $SkipBroadcast) { Send-CodexAutoRetryEnvironmentChange }
        throw
    }
    return [pscustomobject]@{
        Name = $name
        Value = $desired
        Changed = -not (Test-CodexAutoRetryEnvironmentValue $current $desired)
    }
}

function Restore-CodexAutoRetrySharedEnvironment {
    param(
        [Parameter(Mandatory = $true)][string]$DataDir,
        [string]$EnvironmentName = 'CODEX_APP_SERVER_WS_URL',
        [AllowNull()][string[]]$LegacyOwnedEndpoint,
        [switch]$SkipBroadcast
    )
    $name = $EnvironmentName
    $backupPath = Join-Path $DataDir 'environment-backup.json'
    if (-not (Test-Path -LiteralPath $backupPath -PathType Leaf)) {
        # Releases before ownership records existed could leave the endpoint
        # behind. Only clear that legacy value when the caller has independently
        # proved that this exact endpoint belonged to the plugin.
        $current = [Environment]::GetEnvironmentVariable($name, 'User')
        foreach ($endpoint in @($LegacyOwnedEndpoint)) {
            if (-not [string]::IsNullOrWhiteSpace([string]$endpoint) -and
                (Test-CodexAutoRetryEnvironmentValue $current ([string]$endpoint))) {
                Remove-CodexAutoRetryUserEnvironmentValue -Name $name
                if (-not $SkipBroadcast) { Send-CodexAutoRetryEnvironmentChange }
                return [pscustomobject]@{ Restored = $true; ChangedByUser = $false }
            }
        }
        return [pscustomobject]@{ Restored = $false; ChangedByUser = $false }
    }
    try { $backup = Get-Content -Raw -Encoding UTF8 -LiteralPath $backupPath | ConvertFrom-Json }
    catch { throw "The saved $name backup is invalid: $backupPath" }
    if ([int]$backup.schema_version -ne 1 -or [string]$backup.name -ne $name) {
        throw "The saved $name backup is not recognized: $backupPath"
    }
    $current = [Environment]::GetEnvironmentVariable($name, 'User')
    $installed = [string]$backup.installed_value
    $previous = if ([bool]$backup.previous_present) { [string]$backup.previous_value } else { $null }
    $changedByUser = -not (Test-CodexAutoRetryEnvironmentValue $current $installed) -and
        -not (Test-CodexAutoRetryEnvironmentValue $current $previous)
    $restored = $false
    if (-not $changedByUser -and -not (Test-CodexAutoRetryEnvironmentValue $current $previous)) {
        if ($null -eq $previous) {
            Remove-CodexAutoRetryUserEnvironmentValue -Name $name
        }
        else {
            [Environment]::SetEnvironmentVariable($name, $previous, 'User')
            Set-Item -Path "Env:$name" -Value $previous
        }
        if (-not $SkipBroadcast) { Send-CodexAutoRetryEnvironmentChange }
        $restored = $true
    }
    Remove-Item -LiteralPath $backupPath -Force
    return [pscustomobject]@{ Restored = $restored; ChangedByUser = $changedByUser }
}

function Stop-CodexAutoRetrySharedServerIfUnused {
    param([Parameter(Mandatory = $true)][string]$DataDir)
    $statePath = Join-Path $DataDir 'shared-server.json'
    if (-not (Test-Path -LiteralPath $statePath -PathType Leaf)) { return $false }
    try { $state = Get-Content -Raw -Encoding UTF8 -LiteralPath $statePath | ConvertFrom-Json }
    catch { return $false }
    $pidValue = [int]$state.pid
    if ($pidValue -le 0 -or [string]$state.owner -ne 'codex-auto-retry' -or [string]::IsNullOrWhiteSpace([string]$state.version) -or
        [string]$state.endpoint -notmatch '^ws://127\.0\.0\.1:\d+$' -or [string]::IsNullOrWhiteSpace([string]$state.executable)) { return $false }
    $process = Get-CimInstance Win32_Process -Filter "ProcessId = $pidValue" -ErrorAction SilentlyContinue
    if ($null -eq $process) {
        # A dead PID cannot be confused with another process. Remove its
        # ownership record even when Codex is open, so a later startup cannot
        # mistake stale state for a live shared backend.
        Remove-Item -LiteralPath $statePath -Force -ErrorAction SilentlyContinue
        return $true
    }
    $codexDesktop = @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Where-Object {
        $_.Name -eq 'ChatGPT.exe' -and (-not $_.CommandLine -or $_.CommandLine -notmatch '(?:^|\s)--type=')
    })
    if ($codexDesktop.Count -gt 0) { return $false }
    if ($null -eq $process -or -not $process.CommandLine -or
        $process.CommandLine.IndexOf('app-server', [System.StringComparison]::OrdinalIgnoreCase) -lt 0 -or
        $process.CommandLine.IndexOf([string]$state.endpoint, [System.StringComparison]::OrdinalIgnoreCase) -lt 0 -or
        -not [string]::Equals([string]$process.ExecutablePath, [string]$state.executable, [System.StringComparison]::OrdinalIgnoreCase)) {
        return $false
    }
    Stop-Process -Id $pidValue -Force -ErrorAction Stop
    Remove-Item -LiteralPath $statePath -Force -ErrorAction SilentlyContinue
    return $true
}
