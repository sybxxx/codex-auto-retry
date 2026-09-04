[CmdletBinding()]
param(
    [ValidateSet('gui', 'status', 'enable', 'disable', 'start', 'stop', 'safe-disable', 'uninstall')]
    [string]$Action = 'gui',
    [switch]$RemoveData,
    [switch]$NoPrompt,
    [string]$UserProfileRoot = $env:USERPROFILE,
    [string]$LocalAppDataRoot = $env:LOCALAPPDATA,
    [string]$RunName = 'CodexAutoRetry',
    [string]$ReleaseRoot = ''
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2
if ($env:OS -ne 'Windows_NT') { throw 'The startup manager supports Windows only.' }

$profileRoot = [System.IO.Path]::GetFullPath($UserProfileRoot)
$localAppDataRoot = [System.IO.Path]::GetFullPath($LocalAppDataRoot)
$installDir = Join-Path $localAppDataRoot 'CodexAutoRetry'
$watchdog = Join-Path $installDir 'codex-auto-retry.exe'
$pluginTarget = Join-Path $profileRoot 'plugins\codex-auto-retry'
$statusPath = Join-Path $installDir 'status.json'
$configPath = Join-Path $installDir 'config.json'
$sharedStatePath = Join-Path $installDir 'shared-server.json'
$runSubKey = 'Software\Microsoft\Windows\CurrentVersion\Run'
. (Join-Path $PSScriptRoot 'startup-approval.ps1')
. (Join-Path $PSScriptRoot 'shared-server-status.ps1')

function Open-RunKey {
    param([bool]$Writable)
    return [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey($runSubKey, $Writable)
}

function Get-RunValue {
    $key = Open-RunKey -Writable $false
    if ($null -eq $key) { return '' }
    try {
        $value = $key.GetValue($RunName, $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
        if ($null -eq $value) { return '' }
        return [string]$value
    }
    finally {
        $key.Close()
    }
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
    return [string]::Equals($executable, $watchdog, [System.StringComparison]::OrdinalIgnoreCase)
}

function Restore-ManagedStartupValue {
    param([AllowNull()][string]$Value)
    $key = Open-RunKey -Writable $true
    if ($null -eq $key) {
        if ([string]::IsNullOrWhiteSpace($Value)) { return }
        throw 'The current-user startup registry key could not be opened while restoring the previous value.'
    }
    try {
        if ([string]::IsNullOrWhiteSpace($Value)) {
            $key.DeleteValue($RunName, $false)
        }
        else {
            $key.SetValue($RunName, $Value, [Microsoft.Win32.RegistryValueKind]::String)
        }
    }
    finally {
        $key.Close()
    }
}

function Set-ManagedStartup {
    if (-not (Test-Path -LiteralPath $watchdog -PathType Leaf)) {
        throw "The watchdog executable is missing: $watchdog"
    }
    $existing = Get-RunValue
    if (-not [string]::IsNullOrWhiteSpace($existing) -and -not (Test-OwnedStartupValue $existing)) {
        throw "The startup entry $RunName belongs to another command and was not changed."
    }
    $oldApproval = Get-CodexAutoRetryStartupApproval -RunName $RunName
    $desiredValue = ('"{0}" supervise' -f $watchdog)
    [byte[]]$expectedApprovalBytes = @(Get-CodexAutoRetryStartupApprovalEnabledBytes -ExistingBytes $(if ($oldApproval.Present) { [byte[]]$oldApproval.Bytes } else { $null }))
    try {
        $key = Open-RunKey -Writable $true
        if ($null -eq $key) {
            $key = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey($runSubKey, $true)
        }
        if ($null -eq $key) { throw 'The current-user startup registry key could not be opened.' }
        try {
            $key.SetValue($RunName, $desiredValue, [Microsoft.Win32.RegistryValueKind]::String)
        }
        finally {
            $key.Close()
        }
        $null = Set-CodexAutoRetryStartupApprovalEnabled -RunName $RunName
        $actual = Get-RunValue
        if (-not (Test-OwnedStartupValue $actual) -or $actual -notmatch '(?i)\bsupervise\b') {
            throw 'The startup entry could not be registered in supervised mode.'
        }
    }
    catch {
        try {
            # Restore only while the registry still contains the value this
            # operation wrote. A concurrent user or installer change is left
            # untouched and reported through the original failure.
            if ((Get-RunValue) -eq $desiredValue) {
                $currentApproval = Get-CodexAutoRetryStartupApproval -RunName $RunName
                $currentApprovalBytes = if ($currentApproval.Present) { [byte[]]$currentApproval.Bytes } else { $null }
                $approvalWasOld = Test-CodexAutoRetryStartupApprovalBytes -Left $currentApprovalBytes -Right $(if ($oldApproval.Present) { [byte[]]$oldApproval.Bytes } else { $null })
                $approvalWasWritten = Test-CodexAutoRetryStartupApprovalBytes -Left $currentApprovalBytes -Right $expectedApprovalBytes
                if ($approvalWasOld -or $approvalWasWritten) {
                    Restore-ManagedStartupValue -Value $existing
                    if ($approvalWasWritten) {
                        if ($oldApproval.Present) {
                            Restore-CodexAutoRetryStartupApproval -RunName $RunName -Bytes ([byte[]]$oldApproval.Bytes)
                        }
                        else {
                            $null = Remove-CodexAutoRetryStartupApproval -RunName $RunName
                        }
                    }
                }
            }
        }
        catch {
            # Preserve the original failure. The next status/repair pass can
            # report the remaining registry inconsistency without masking it.
        }
        throw
    }
    return $actual
}

function Remove-ManagedStartup {
    $existing = Get-RunValue
    if (-not [string]::IsNullOrWhiteSpace($existing) -and -not (Test-OwnedStartupValue $existing)) {
        throw "The startup entry $RunName belongs to another command and was not removed."
    }
    $oldApproval = Get-CodexAutoRetryStartupApproval -RunName $RunName
    $removedRun = $false
    try {
        # Re-read immediately before deletion so a concurrent installer or
        # user cannot replace the checked owned command with a foreign one.
        $current = Get-RunValue
        if ($current -ne $existing) {
            throw "The startup entry $RunName changed while it was being removed."
        }
        if (-not [string]::IsNullOrWhiteSpace($existing)) {
            $key = Open-RunKey -Writable $true
            if ($null -ne $key) {
                try { $key.DeleteValue($RunName, $false); $removedRun = $true }
                finally { $key.Close() }
            }
        }
        if (-not [string]::IsNullOrWhiteSpace((Get-RunValue))) {
            throw 'The plugin startup entry is still present after removal.'
        }
        $removedApproval = Remove-CodexAutoRetryStartupApproval -RunName $RunName
        return $removedRun -or $removedApproval
    }
    catch {
        try {
            # Roll back only an unchanged post-delete state. Never overwrite a
            # foreign command that appeared while the removal was in flight.
            $currentRunAfterFailure = Get-RunValue
            $currentApproval = Get-CodexAutoRetryStartupApproval -RunName $RunName
            $currentApprovalBytes = if ($currentApproval.Present) { [byte[]]$currentApproval.Bytes } else { $null }
            $approvalWasOld = Test-CodexAutoRetryStartupApprovalBytes -Left $currentApprovalBytes -Right $(if ($oldApproval.Present) { [byte[]]$oldApproval.Bytes } else { $null })
            $approvalWasRemoved = -not $currentApproval.Present
            $runWasRemoved = [string]::IsNullOrWhiteSpace($currentRunAfterFailure)
            $runWasUnchanged = $currentRunAfterFailure -eq $existing
            if (($runWasRemoved -or $runWasUnchanged) -and ($approvalWasOld -or $approvalWasRemoved)) {
                if ($runWasRemoved) {
                    Restore-ManagedStartupValue -Value $existing
                }
                if ($approvalWasRemoved -and $oldApproval.Present) {
                    Restore-CodexAutoRetryStartupApproval -RunName $RunName -Bytes ([byte[]]$oldApproval.Bytes)
                }
            }
        }
        catch {
            # Preserve the original failure and leave status visible for a
            # subsequent explicit repair.
        }
        throw
    }
}

function Get-ManagerProcesses {
    if (-not (Test-Path -LiteralPath $watchdog -PathType Leaf)) { return @() }
    return @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
        Where-Object {
            $_.ExecutablePath -and
            [string]::Equals($_.ExecutablePath, $watchdog, [System.StringComparison]::OrdinalIgnoreCase)
        })
}

function Test-HeartbeatFresh {
    param(
        [Parameter(Mandatory = $true)]$Value,
        [TimeSpan]$MaxAge = ([TimeSpan]::FromSeconds(15))
    )
    try {
        $timestamp = if ($Value -is [DateTime]) {
            [DateTimeOffset]$Value
        }
        elseif ($Value -is [DateTimeOffset]) {
            $Value
        }
        else {
            [DateTimeOffset]::Parse([string]$Value)
        }
        $age = [DateTimeOffset]::UtcNow - $timestamp.ToUniversalTime()
        return $age -ge [TimeSpan]::Zero -and $age -le $MaxAge
    }
    catch {
        return $false
    }
}

function Read-JsonOrNull {
    param([Parameter(Mandatory = $true)][string]$Path)
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return $null }
    try { return (Get-Content -Raw -Encoding UTF8 -LiteralPath $Path | ConvertFrom-Json) }
    catch { return $null }
}

function Get-ManagerState {
    $status = Read-JsonOrNull -Path $statusPath
    $config = Read-JsonOrNull -Path $configPath
    $sharedState = Read-JsonOrNull -Path $sharedStatePath
    $sharedStateReadFailed = (Test-Path -LiteralPath $sharedStatePath -PathType Leaf) -and $null -eq $sharedState
    $processes = @(Get-ManagerProcesses)
    $heartbeatFresh = $false
    if ($status -and $status.last_scan_at) {
        $heartbeatFresh = Test-HeartbeatFresh -Value $status.last_scan_at
    }
    $serviceRunning = $null -ne $status -and [bool]$status.running -and $heartbeatFresh -and $processes.Count -gt 0
    $startupEntry = Get-RunValue
    $startupApproval = Get-CodexAutoRetryStartupApproval -RunName $RunName
    $startupMode = if ([string]::IsNullOrWhiteSpace($startupEntry)) {
        'missing'
    }
    elseif ($startupEntry -match '(?i)\bsupervise\b') {
        'supervise'
    }
    elseif ($startupEntry -match '(?i)\brun\b') {
        'run'
    }
    else {
        'unknown'
    }
    $expectedSharedPort = if ($config -and $config.PSObject.Properties['shared_app_server_port']) { [int]$config.shared_app_server_port } else { 0 }
    $sharedVerification = if ($sharedStateReadFailed) {
        [pscustomobject][ordered]@{ Status = 'unknown'; Reason = 'state_unreadable'; PID = $null; Endpoint = $null }
    } else {
        Get-CodexAutoRetrySharedServerStatus -State $sharedState -ExpectedPort $expectedSharedPort
    }
    $sharedStateStatus = [string]$sharedVerification.Status
    $endpoint = [Environment]::GetEnvironmentVariable('CODEX_APP_SERVER_WS_URL', 'User')
    $manifest = Read-JsonOrNull -Path (Join-Path $pluginTarget '.codex-plugin\plugin.json')
    return [pscustomobject][ordered]@{
        PluginInstalled = $null -ne $manifest -and [string]$manifest.name -eq 'codex-auto-retry'
        PluginVersion = if ($manifest) { [string]$manifest.version } else { $null }
        InstallDir = $installDir
        ServiceRunning = $serviceRunning
        ProcessCount = $processes.Count
        ProcessIds = @($processes | ForEach-Object { [int]$_.ProcessId })
        HeartbeatFresh = $heartbeatFresh
        RuntimeVersion = if ($status) { [string]$status.version } else { $null }
        LastScanAt = if ($status) { $status.last_scan_at } else { $null }
        StartupMode = $startupMode
        StartupEntry = if ([string]::IsNullOrWhiteSpace($startupEntry)) { $null } else { $startupEntry }
        StartupOwned = Test-OwnedStartupValue $startupEntry
        StartupApproved = $startupApproval.Status
        SharedModeEnabled = if ($config -and $config.PSObject.Properties['shared_app_server_enabled']) { [bool]$config.shared_app_server_enabled } else { $false }
        SharedEndpointConfigured = -not [string]::IsNullOrWhiteSpace($endpoint)
        SharedServerState = $sharedStateStatus
        SharedServerVerification = [string]$sharedVerification.Reason
        SharedAppServerMemoryUsageMB = if ($status) { $status.shared_app_server_memory_usage_mb } else { 0 }
        SharedAppServerMemoryLimitMB = if ($status) { $status.shared_app_server_memory_limit_mb } else { $null }
        SharedAppServerMemoryGuardTriggered = if ($status) { [bool]$status.shared_app_server_memory_guard_triggered } else { $false }
        RetrySafetyWarning = if ($status) { [string]$status.retry_safety_warning } else { $null }
        DataDirectoryExists = Test-Path -LiteralPath $installDir -PathType Container
    }
}

function Wait-ManagerServiceState {
    param([Parameter(Mandatory = $true)][bool]$Running, [int]$TimeoutSeconds = 15)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    do {
        Start-Sleep -Milliseconds 250
        $state = Get-ManagerState
        if ($state.ServiceRunning -eq $Running) { return $state }
    } while ((Get-Date) -lt $deadline)
    return Get-ManagerState
}

function Start-ManagedService {
    $current = Get-ManagerState
    if ($current.ServiceRunning) { return $current }
    if (-not (Test-Path -LiteralPath $watchdog -PathType Leaf)) {
        throw "The watchdog executable is missing: $watchdog"
    }
    New-Item -ItemType Directory -Force -Path $installDir | Out-Null
    Remove-Item -LiteralPath (Join-Path $installDir 'stop.signal') -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath (Join-Path $installDir 'supervisor.stop') -Force -ErrorAction SilentlyContinue
    Start-Process -FilePath $watchdog -ArgumentList @('supervise') -WorkingDirectory $installDir -WindowStyle Hidden | Out-Null
    $state = Wait-ManagerServiceState -Running $true
    if (-not $state.ServiceRunning) { throw 'The watchdog did not publish a fresh heartbeat after starting.' }
    return $state
}

function Stop-ManagedService {
    if (-not (Test-Path -LiteralPath $installDir -PathType Container)) { return Get-ManagerState }
    New-Item -ItemType File -Force -Path (Join-Path $installDir 'supervisor.stop') | Out-Null
    New-Item -ItemType File -Force -Path (Join-Path $installDir 'stop.signal') | Out-Null
    $deadline = (Get-Date).AddSeconds(12)
    do {
        $processes = @(Get-ManagerProcesses)
        if ($processes.Count -eq 0) { break }
        Start-Sleep -Milliseconds 250
    } while ((Get-Date) -lt $deadline)
    $remaining = @(Get-ManagerProcesses)
    if ($remaining.Count -gt 0) {
        throw 'The watchdog did not stop gracefully. No process was force-terminated; use the safe-disable action after Codex closes.'
    }
    return Get-ManagerState
}

function Invoke-ManagedScript {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [string[]]$Arguments = @()
    )
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { throw "Required maintenance script is missing: $Path" }
    $output = (& powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $Path @Arguments 2>&1 | Out-String)
    if ($LASTEXITCODE -ne 0) { throw "Maintenance script failed with exit code $LASTEXITCODE.`n$($output.Trim())" }
    return $output
}

function Invoke-ManagerSafeDisable {
    $safeDisable = Join-Path $pluginTarget 'scripts\safe-disable.ps1'
    if (-not (Test-Path -LiteralPath $safeDisable -PathType Leaf) -and -not [string]::IsNullOrWhiteSpace($ReleaseRoot)) {
        $safeDisable = Join-Path $ReleaseRoot 'payload\codex-auto-retry\scripts\safe-disable.ps1'
    }
    $null = Invoke-ManagedScript -Path $safeDisable -Arguments @('-DataDir', $installDir)
    return Get-ManagerState
}

function Invoke-ManagerUninstall {
    $uninstaller = ''
    if (-not [string]::IsNullOrWhiteSpace($ReleaseRoot)) {
        $uninstaller = Join-Path $ReleaseRoot 'uninstall-release.ps1'
    }
    if (-not (Test-Path -LiteralPath $uninstaller -PathType Leaf)) {
        throw 'For a complete uninstall, run this manager from the extracted release folder so it can remove the Codex plugin registration safely.'
    }
    $arguments = @('-UserProfileRoot', $profileRoot, '-LocalAppDataRoot', $localAppDataRoot)
    if ($RemoveData) { $arguments += '-RemoveData' }
    $null = Invoke-ManagedScript -Path $uninstaller -Arguments $arguments
    return [pscustomobject][ordered]@{
        Uninstalled = $true
        DataRemoved = [bool]$RemoveData
        StartupEntry = Get-RunValue
        RuntimePathExists = Test-Path -LiteralPath $installDir -PathType Container
        PluginPathExists = Test-Path -LiteralPath $pluginTarget -PathType Container
    }
}

function Invoke-ManagerAction {
    param([Parameter(Mandatory = $true)][string]$RequestedAction)
    switch ($RequestedAction) {
        'status' { return Get-ManagerState }
        'enable' { $null = Set-ManagedStartup; return Get-ManagerState }
        'disable' { $null = Remove-ManagedStartup; return Get-ManagerState }
        'start' { return Start-ManagedService }
        'stop' { return Stop-ManagedService }
        'safe-disable' { return Invoke-ManagerSafeDisable }
        'uninstall' {
            if ($RemoveData -and -not $NoPrompt) {
                throw 'Destructive data removal requires -NoPrompt when invoked without the graphical manager.'
            }
            return Invoke-ManagerUninstall
        }
        default { throw "Unsupported manager action: $RequestedAction" }
    }
}

function Format-ManagerState {
    param([Parameter(Mandatory = $true)]$State)
    $State | Format-List * | Out-String -Width 120
}

function Hide-ManagerConsoleWindow {
    # The .cmd entry point is intentionally simple and may be hosted by
    # Windows Terminal. Hide only that console after the Forms window is ready;
    # hiding the PowerShell process itself would also hide the manager dialog.
    try {
        if (-not ('CodexAutoRetry.NativeWindow' -as [type])) {
            Add-Type @'
using System;
using System.Runtime.InteropServices;
namespace CodexAutoRetry {
    public static class NativeWindow {
        [DllImport("kernel32.dll")]
        public static extern IntPtr GetConsoleWindow();

        [DllImport("user32.dll")]
        [return: MarshalAs(UnmanagedType.Bool)]
        public static extern bool ShowWindow(IntPtr window, int command);
    }
}
'@
        }
        $console = [CodexAutoRetry.NativeWindow]::GetConsoleWindow()
        if ($console -ne [IntPtr]::Zero) {
            [void][CodexAutoRetry.NativeWindow]::ShowWindow($console, 0)
        }
    }
    catch {
        # A host without a console or without the native window API can still
        # use the graphical manager; hiding the console is only cosmetic.
    }
}

function Show-Manager {
    Add-Type -AssemblyName System.Windows.Forms
    Add-Type -AssemblyName System.Drawing

    $form = New-Object System.Windows.Forms.Form
    $form.Text = 'Codex Auto Retry Startup Manager'
    $form.StartPosition = 'CenterScreen'
    $form.Size = New-Object System.Drawing.Size(720, 560)
    $form.MinimumSize = New-Object System.Drawing.Size(620, 480)

    $title = New-Object System.Windows.Forms.Label
    $title.Text = 'Codex Auto Retry Startup Manager'
    $title.Font = New-Object System.Drawing.Font('Segoe UI', 14, [System.Drawing.FontStyle]::Bold)
    $title.AutoSize = $true
    $title.Location = New-Object System.Drawing.Point(18, 15)
    $form.Controls.Add($title)

    $subtitle = New-Object System.Windows.Forms.Label
    $subtitle.Text = 'Inspect startup, service, and shared-backend state. Actions are limited to this plugin.'
    $subtitle.AutoSize = $true
    $subtitle.ForeColor = [System.Drawing.Color]::DimGray
    $subtitle.Location = New-Object System.Drawing.Point(20, 48)
    $form.Controls.Add($subtitle)

    $output = New-Object System.Windows.Forms.TextBox
    $output.Multiline = $true
    $output.ReadOnly = $true
    $output.ScrollBars = 'Vertical'
    $output.Font = New-Object System.Drawing.Font('Consolas', 10)
    $output.Anchor = 'Top,Bottom,Left,Right'
    $output.Location = New-Object System.Drawing.Point(18, 78)
    $output.Size = New-Object System.Drawing.Size(668, 330)
    $form.Controls.Add($output)

    $buttons = New-Object System.Windows.Forms.FlowLayoutPanel
    $buttons.FlowDirection = 'LeftToRight'
    $buttons.WrapContents = $true
    $buttons.Anchor = 'Bottom,Left,Right'
    $buttons.Location = New-Object System.Drawing.Point(18, 420)
    $buttons.Size = New-Object System.Drawing.Size(668, 84)
    $buttons.Padding = New-Object System.Windows.Forms.Padding(0)
    $form.Controls.Add($buttons)

    $refresh = New-Object System.Windows.Forms.Button
    $refresh.Text = 'Refresh status'
    $refresh.Width = 100
    $refresh.Height = 30
    $buttons.Controls.Add($refresh)

    $definitions = @(
        @('Enable startup', 'enable', $false),
        @('Disable startup', 'disable', $false),
        @('Start service', 'start', $false),
        @('Stop service', 'stop', $false),
        @('Safe-disable shared backend', 'safe-disable', $true),
        @('Uninstall, keep data', 'uninstall', $true),
        @('Uninstall and remove data', 'uninstall-remove', $true)
    )

    $refreshView = {
        try {
            $output.Text = Format-ManagerState -State (Get-ManagerState)
        }
        catch {
            $output.Text = $_.Exception.Message
        }
    }.GetNewClosure()

    function Add-ManagerButton {
        param([string]$Text, [string]$ButtonAction, [bool]$Danger, [scriptblock]$RefreshView)
        $button = New-Object System.Windows.Forms.Button
        $button.Text = $Text
        $button.Width = 142
        $button.Height = 30
        if ($Danger) { $button.ForeColor = [System.Drawing.Color]::DarkRed }
        $handler = {
            try {
                if ($ButtonAction -eq 'uninstall-remove') {
                    $confirm = [System.Windows.Forms.MessageBox]::Show(
                        'This removes the plugin, runtime state, settings, and logs. Chat data is not touched. Continue?',
                        'Confirm full uninstall',
                        [System.Windows.Forms.MessageBoxButtons]::YesNo,
                        [System.Windows.Forms.MessageBoxIcon]::Warning
                    )
                    if ($confirm -ne [System.Windows.Forms.DialogResult]::Yes) { return }
                    $script:RemoveData = $true
                    $script:NoPrompt = $true
                    $null = Invoke-ManagerAction -RequestedAction 'uninstall'
                    [System.Windows.Forms.MessageBox]::Show('Full uninstall completed.', 'Codex Auto Retry') | Out-Null
                    $form.Close()
                    return
                }
                if ($ButtonAction -eq 'uninstall') {
                    $confirm = [System.Windows.Forms.MessageBox]::Show(
                        'This removes the plugin, startup entry, and service, but keeps retry settings, state, and logs. Continue?',
                        'Confirm uninstall',
                        [System.Windows.Forms.MessageBoxButtons]::YesNo,
                        [System.Windows.Forms.MessageBoxIcon]::Question
                    )
                    if ($confirm -ne [System.Windows.Forms.DialogResult]::Yes) { return }
                    $script:RemoveData = $false
                    $script:NoPrompt = $true
                    $null = Invoke-ManagerAction -RequestedAction 'uninstall'
                    [System.Windows.Forms.MessageBox]::Show('Uninstall completed. Runtime data was kept.', 'Codex Auto Retry') | Out-Null
                    $form.Close()
                    return
                }
                $null = Invoke-ManagerAction -RequestedAction $ButtonAction
                & $RefreshView
            }
            catch {
                [System.Windows.Forms.MessageBox]::Show($_.Exception.Message, 'Action failed', [System.Windows.Forms.MessageBoxButtons]::OK, [System.Windows.Forms.MessageBoxIcon]::Error) | Out-Null
                try { & $RefreshView } catch { $output.Text = $_.Exception.Message }
            }
        }.GetNewClosure()
        $button.Add_Click($handler)
        $buttons.Controls.Add($button)
    }

    foreach ($definition in $definitions) {
        Add-ManagerButton -Text $definition[0] -ButtonAction $definition[1] -Danger ([bool]$definition[2]) -RefreshView $refreshView
    }
    $refresh.Add_Click({ & $refreshView }.GetNewClosure())
    $form.Add_Shown({
        Hide-ManagerConsoleWindow
        & $refreshView
        $form.Activate()
    }.GetNewClosure())
    [void]$form.ShowDialog()
}

if ($Action -eq 'gui') {
    Show-Manager
    return
}

$result = Invoke-ManagerAction -RequestedAction $Action
if ($null -ne $result) { $result | Format-List * }
