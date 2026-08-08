function Test-CodexAutoRetryEnvironmentValue {
    param([AllowNull()][string]$Left, [AllowNull()][string]$Right)
    if ($null -eq $Left -or $null -eq $Right) { return $null -eq $Left -and $null -eq $Right }
    return [string]::Equals($Left, $Right, [System.StringComparison]::OrdinalIgnoreCase)
}

function Send-CodexAutoRetryEnvironmentChange {
    if (-not ('CodexAutoRetryEnvironmentBroadcast' -as [type])) {
        Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class CodexAutoRetryEnvironmentBroadcast {
    [DllImport("user32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern IntPtr SendMessageTimeout(
        IntPtr hWnd, uint message, IntPtr wParam, string lParam,
        uint flags, uint timeout, out IntPtr result);
    public static void Broadcast(string value) {
        IntPtr result;
        SendMessageTimeout(new IntPtr(0xffff), 0x001a, IntPtr.Zero, value, 0x0002, 5000, out result);
    }
}
'@
    }
    [CodexAutoRetryEnvironmentBroadcast]::Broadcast('Environment')
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
        if ($null -eq $current) { Remove-Item -Path "Env:$name" -ErrorAction SilentlyContinue }
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
                [Environment]::SetEnvironmentVariable($name, $null, 'User')
                Remove-Item -Path "Env:$name" -ErrorAction SilentlyContinue
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
        [Environment]::SetEnvironmentVariable($name, $previous, 'User')
        if ($null -eq $previous) { Remove-Item -Path "Env:$name" -ErrorAction SilentlyContinue }
        else { Set-Item -Path "Env:$name" -Value $previous }
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
    $codexDesktop = @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Where-Object {
        $_.Name -eq 'ChatGPT.exe' -and (-not $_.CommandLine -or $_.CommandLine -notmatch '(?:^|\s)--type=')
    })
    if ($codexDesktop.Count -gt 0) { return $false }
    try { $state = Get-Content -Raw -Encoding UTF8 -LiteralPath $statePath | ConvertFrom-Json }
    catch { return $false }
    $pidValue = [int]$state.pid
    if ($pidValue -le 0 -or [string]$state.owner -ne 'codex-auto-retry' -or [string]::IsNullOrWhiteSpace([string]$state.version) -or
        [string]$state.endpoint -notmatch '^ws://127\.0\.0\.1:\d+$' -or [string]::IsNullOrWhiteSpace([string]$state.executable)) { return $false }
    $process = Get-CimInstance Win32_Process -Filter "ProcessId = $pidValue" -ErrorAction SilentlyContinue
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
