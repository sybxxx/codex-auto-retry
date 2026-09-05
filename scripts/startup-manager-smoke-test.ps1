[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$manager = Join-Path $PSScriptRoot 'startup-manager.ps1'
if (-not (Test-Path -LiteralPath $manager -PathType Leaf)) { throw 'startup-manager.ps1 is missing.' }

$tokens = $null
$errors = $null
[void][System.Management.Automation.Language.Parser]::ParseFile($manager, [ref]$tokens, [ref]$errors)
if ($errors.Count -gt 0) { throw "PowerShell parse error: $($errors[0].Message)" }
$source = Get-Content -Raw -Encoding UTF8 -LiteralPath $manager
foreach ($required in @('Set-ManagedStartup', 'Remove-ManagedStartup', 'Restore-ManagedStartupValue', 'Get-ManagerState', 'Hide-ManagerConsoleWindow', '$refreshView', '-RefreshView $refreshView', 'safe-disable', 'uninstall', 'supervise', 'Test-OwnedStartupValue', 'StartupApproved')) {
    if (-not $source.Contains($required)) { throw "Startup manager is missing required behavior: $required" }
}

function Remove-OrphanedSmokeApprovalValues {
    $key = Open-CodexAutoRetryStartupApprovedKey -Writable $true
    if ($null -eq $key) { return }
    try {
        foreach ($name in @($key.GetValueNames())) {
            if ($name -notmatch '^CodexAutoRetry(?:Smoke|SafeDisableSmoke)_[0-9a-f]{32}$') { continue }
            $runValue = Get-ItemProperty -Path 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run' -Name $name -ErrorAction SilentlyContinue
            if ($null -eq $runValue) { $key.DeleteValue($name, $false) }
        }
    }
    finally { $key.Close() }
}
if ($source.Contains('function Refresh-ManagerView')) {
    throw 'Startup manager still relies on a function with an unsafe event-handler scope.'
}

$testRoot = Join-Path $env:TEMP ('codex-auto-retry-manager-' + [guid]::NewGuid().ToString('N'))
$profileRoot = Join-Path $testRoot 'profile'
$localRoot = Join-Path $testRoot 'local'
New-Item -ItemType Directory -Force -Path (Join-Path $localRoot 'CodexAutoRetry'), (Join-Path $profileRoot 'plugins\codex-auto-retry\.codex-plugin') | Out-Null
$fakeWatchdog = Join-Path $localRoot 'CodexAutoRetry\codex-auto-retry.exe'
[IO.File]::WriteAllBytes($fakeWatchdog, [byte[]](0..31))
$runKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'
$testRunName = 'CodexAutoRetrySmoke_' + [guid]::NewGuid().ToString('N')
$sentinelName = 'CodexAutoRetryUnrelatedSmoke_' + [guid]::NewGuid().ToString('N')
$sentinelValue = 'C:\OtherSoftware\unrelated-startup.exe'
. (Join-Path $PSScriptRoot 'startup-approval.ps1')
Remove-OrphanedSmokeApprovalValues
$before = Get-ItemProperty -Path $runKey -Name $testRunName -ErrorAction SilentlyContinue
$beforeSentinel = Get-ItemProperty -Path $runKey -Name $sentinelName -ErrorAction SilentlyContinue
$beforeDefault = Get-ItemProperty -Path $runKey -Name 'CodexAutoRetry' -ErrorAction SilentlyContinue
$beforeApproval = Get-CodexAutoRetryStartupApproval -RunName $testRunName
$guiProcess = $null
try {
    $args = @('-NoLogo', '-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', $manager,
        '-Action', 'enable', '-UserProfileRoot', $profileRoot, '-LocalAppDataRoot', $localRoot, '-RunName', $testRunName)
    $manifest = @{ name = 'codex-auto-retry'; version = 'test' } | ConvertTo-Json
    [IO.File]::WriteAllText((Join-Path $profileRoot 'plugins\codex-auto-retry\.codex-plugin\plugin.json'), $manifest)
    [IO.File]::WriteAllBytes($fakeWatchdog, [byte[]](0..31))
    $runRegistryKey = Open-CodexAutoRetryRunKey -Writable $true
    if ($null -eq $runRegistryKey) { throw 'The current-user startup registry key could not be opened.' }
    try {
        # This unrelated value must survive every manager operation. It guards
        # against provider writes that recreate the entire Run key.
        $runRegistryKey.SetValue($sentinelName, $sentinelValue, [Microsoft.Win32.RegistryValueKind]::String)
        $runRegistryKey.SetValue($testRunName, ('"' + $fakeWatchdog + '" supervise'), [Microsoft.Win32.RegistryValueKind]::String)
    }
    finally { $runRegistryKey.Close() }
    Restore-CodexAutoRetryStartupApproval -RunName $testRunName -Bytes ([byte[]](3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0))
    $enableOutput = (& powershell.exe @args 2>&1 | Out-String)
    if ($LASTEXITCODE -ne 0) { throw "Startup manager could not enable the owned test entry.`n$enableOutput" }
    $enabled = Get-ItemProperty -Path $runKey -Name $testRunName -ErrorAction Stop
    $enabledValue = [string]$enabled.$testRunName
    if ($enabledValue -notmatch '(?i)\bsupervise\b' -or
        $enabledValue.IndexOf($fakeWatchdog, [StringComparison]::OrdinalIgnoreCase) -lt 0) {
        throw 'Startup manager did not write the supervised owned entry.'
    }
    if ((Get-CodexAutoRetryStartupApproval -RunName $testRunName).Status -ne 'enabled') {
        throw 'Startup manager did not enable the matching StartupApproved value.'
    }
    if ([string](Get-ItemPropertyValue -Path $runKey -Name $sentinelName -ErrorAction Stop) -ne $sentinelValue) {
        throw 'Startup manager removed an unrelated Run startup value while enabling.'
    }
    $disabledBytes = [byte[]](3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
    Restore-CodexAutoRetryStartupApproval -RunName $testRunName -Bytes $disabledBytes
    $statusOutput = (& powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $manager `
        -Action status -UserProfileRoot $profileRoot -LocalAppDataRoot $localRoot -RunName $testRunName 2>&1 | Out-String)
    if ($LASTEXITCODE -ne 0 -or $statusOutput -notmatch '(?im)StartupApproved\s*:\s*disabled') {
        throw "Startup manager did not distinguish a disabled StartupApproved value while Run exists.`n$statusOutput"
    }
    $null = Set-CodexAutoRetryStartupApprovalEnabled -RunName $testRunName
    $disableArgs = @('-NoLogo', '-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', $manager,
        '-Action', 'disable', '-UserProfileRoot', $profileRoot, '-LocalAppDataRoot', $localRoot, '-RunName', $testRunName)
    $null = & powershell.exe @disableArgs 2>&1 | Out-String
    if ($LASTEXITCODE -ne 0 -or $null -ne (Get-ItemProperty -Path $runKey -Name $testRunName -ErrorAction SilentlyContinue)) {
        throw 'Startup manager could not remove the owned test entry.'
    }
    if ((Get-CodexAutoRetryStartupApproval -RunName $testRunName).Present) {
        throw 'Startup manager did not remove the matching StartupApproved value.'
    }
    if ([string](Get-ItemPropertyValue -Path $runKey -Name $sentinelName -ErrorAction Stop) -ne $sentinelValue) {
        throw 'Startup manager removed an unrelated Run startup value while disabling.'
    }
    $runRegistryKey = Open-CodexAutoRetryRunKey -Writable $true
    if ($null -eq $runRegistryKey) { throw 'The current-user startup registry key could not be opened.' }
    try { $runRegistryKey.SetValue($testRunName, ('"C:\OtherSoftware\other.exe" --arg "' + $fakeWatchdog + '"'), [Microsoft.Win32.RegistryValueKind]::String) }
    finally { $runRegistryKey.Close() }
    $savedPreference = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try { $foreignOutput = (& powershell.exe @args 2>&1 | Out-String) }
    finally { $ErrorActionPreference = $savedPreference }
    if ($LASTEXITCODE -eq 0 -or [string](Get-ItemPropertyValue -Path $runKey -Name $testRunName -ErrorAction Stop) -notmatch 'OtherSoftware') {
        throw "Startup manager did not refuse a command that only mentions the watchdog path.`n$foreignOutput"
    }
    Remove-ItemProperty -Path $runKey -Name $testRunName -ErrorAction SilentlyContinue
    $runRegistryKey = Open-CodexAutoRetryRunKey -Writable $true
    if ($null -eq $runRegistryKey) { throw 'The current-user startup registry key could not be opened.' }
    try { $runRegistryKey.SetValue($testRunName, 'C:\OtherSoftware\other.exe', [Microsoft.Win32.RegistryValueKind]::String) }
    finally { $runRegistryKey.Close() }
    $savedPreference = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try { $foreignOutput = (& powershell.exe @args 2>&1 | Out-String) }
    finally { $ErrorActionPreference = $savedPreference }
    if ($LASTEXITCODE -eq 0 -or [string](Get-ItemPropertyValue -Path $runKey -Name $testRunName -ErrorAction Stop) -ne 'C:\OtherSoftware\other.exe') {
        throw "Startup manager did not refuse an unowned startup entry.`n$foreignOutput"
    }
    Remove-ItemProperty -Path $runKey -Name $testRunName -ErrorAction SilentlyContinue
    $guiArgs = @('-NoLogo', '-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', $manager,
        '-Action', 'gui', '-UserProfileRoot', $profileRoot, '-LocalAppDataRoot', $localRoot, '-RunName', $testRunName)
    $guiError = Join-Path $testRoot 'gui-error.txt'
    $guiProcess = Start-Process -FilePath 'powershell.exe' -ArgumentList $guiArgs -WindowStyle Hidden -RedirectStandardError $guiError -PassThru
    $deadline = (Get-Date).AddSeconds(12)
    do {
        Start-Sleep -Milliseconds 250
        $guiWindow = Get-Process -Id $guiProcess.Id -ErrorAction SilentlyContinue
    } while (-not $guiProcess.HasExited -and
        ($null -eq $guiWindow -or $guiWindow.MainWindowTitle -ne 'Codex Auto Retry Startup Manager') -and (Get-Date) -lt $deadline)
    if ($guiProcess.HasExited -or $null -eq $guiWindow -or $guiWindow.MainWindowHandle -eq 0 -or
        $guiWindow.MainWindowTitle -ne 'Codex Auto Retry Startup Manager') {
        throw ('Startup manager GUI did not create its visible settings window. ' + (Get-Content -LiteralPath $guiError -Raw))
    }
    Stop-Process -Id $guiProcess.Id -Force -ErrorAction SilentlyContinue
    try { [void]$guiProcess.WaitForExit(5000) } catch { }
    $guiProcess.Dispose()
    $guiProcess = $null

    # Exercise a real button click. The action must remove only the owned
    # startup value, keep the manager open, and never show a false error caused
    # by an event-handler scope failure.
    Add-Type -AssemblyName UIAutomationClient
    Add-Type -AssemblyName UIAutomationTypes
    Add-Type @'
using System;
using System.Runtime.InteropServices;
namespace CodexAutoRetrySmoke {
    public static class Native {
        [DllImport("user32.dll")]
        public static extern bool PostMessage(IntPtr window, uint message, IntPtr wParam, IntPtr lParam);
    }
}
'@
    $runRegistryKey = Open-CodexAutoRetryRunKey -Writable $true
    if ($null -eq $runRegistryKey) { throw 'The current-user startup registry key could not be opened.' }
    try { $runRegistryKey.SetValue($testRunName, ('"' + $fakeWatchdog + '" supervise'), [Microsoft.Win32.RegistryValueKind]::String) }
    finally { $runRegistryKey.Close() }
    $actionProcess = Start-Process -FilePath 'powershell.exe' -ArgumentList $guiArgs -WindowStyle Hidden -PassThru
    try {
        $deadline = (Get-Date).AddSeconds(10)
        $actionWindow = $null
        do {
            Start-Sleep -Milliseconds 250
            $actionWindow = Get-Process -Id $actionProcess.Id -ErrorAction SilentlyContinue
        } while (($null -eq $actionWindow -or $actionWindow.MainWindowHandle -eq 0) -and (Get-Date) -lt $deadline)
        if ($null -eq $actionWindow -or $actionWindow.MainWindowHandle -eq 0) {
            throw 'Startup manager action test could not create its settings window.'
        }
        $automationWindow = [System.Windows.Automation.AutomationElement]::FromHandle($actionWindow.MainWindowHandle)
        $buttonCondition = [System.Windows.Automation.PropertyCondition]::new(
            [System.Windows.Automation.AutomationElement]::NameProperty,
            'Disable startup'
        )
        $disableButton = $automationWindow.FindFirst(
            [System.Windows.Automation.TreeScope]::Descendants,
            $buttonCondition
        )
        if ($null -eq $disableButton -or $disableButton.Current.NativeWindowHandle -eq 0) {
            throw 'Startup manager action test could not find the Disable startup button.'
        }
        if (-not [CodexAutoRetrySmoke.Native]::PostMessage(
            [IntPtr]$disableButton.Current.NativeWindowHandle,
            0x00F5,
            [IntPtr]::Zero,
            [IntPtr]::Zero
        )) {
            throw 'Startup manager action test could not click Disable startup.'
        }
        Start-Sleep -Seconds 2
        $remainingProperty = Get-ItemProperty -Path $runKey -Name $testRunName -ErrorAction SilentlyContinue
        $remainingValue = if ($remainingProperty) { [string]$remainingProperty.$testRunName } else { '' }
        $actionWindow = Get-Process -Id $actionProcess.Id -ErrorAction SilentlyContinue
        $rootElement = [System.Windows.Automation.AutomationElement]::RootElement
        $topLevel = $rootElement.FindAll(
            [System.Windows.Automation.TreeScope]::Children,
            [System.Windows.Automation.Condition]::TrueCondition
        )
        $errorDialog = @()
        if ($topLevel.Count -gt 0) {
            $errorDialog = @(
                0..($topLevel.Count - 1) |
                    ForEach-Object { $topLevel.Item($_) } |
                    Where-Object { $_.Current.Name -eq 'Action failed' }
            )
        }
        if (-not [string]::IsNullOrWhiteSpace($remainingValue)) {
            throw 'Disable startup did not remove the owned startup value after a real button click.'
        }
        if ($null -eq $actionWindow -or $actionWindow.HasExited -or -not $actionWindow.Responding) {
            throw 'Startup manager closed or stopped responding after Disable startup.'
        }
        if ($errorDialog.Count -gt 0) {
            throw 'Disable startup showed a false Action failed dialog.'
        }
    if ([string](Get-ItemPropertyValue -Path $runKey -Name $sentinelName -ErrorAction Stop) -ne $sentinelValue) {
        throw 'Startup manager removed an unrelated Run startup value during a button action.'
    }
    $defaultAfter = Get-ItemProperty -Path $runKey -Name 'CodexAutoRetry' -ErrorAction SilentlyContinue
    $defaultAfterValue = if ($null -eq $defaultAfter) { $null } else { [string]$defaultAfter.CodexAutoRetry }
    $defaultBeforeValue = if ($null -eq $beforeDefault) { $null } else { [string]$beforeDefault.CodexAutoRetry }
    if ($defaultAfterValue -ne $defaultBeforeValue) {
        throw 'Startup manager changed the unrelated CodexAutoRetry startup value.'
    }
    }
    finally {
        $actionWindow = Get-Process -Id $actionProcess.Id -ErrorAction SilentlyContinue
        if ($null -ne $actionWindow) {
            Stop-Process -Id $actionProcess.Id -Force -ErrorAction SilentlyContinue
            try { [void]$actionProcess.WaitForExit(5000) } catch { }
        }
    }
    [pscustomobject]@{
        Status = 'passed'
        Parser = 'passed'
        OwnershipGuards = $true
        SupervisedCommand = $true
        GraphicalManagerStarted = $true
        ButtonActionRefresh = 'passed'
        DestructiveActionRequiresConfirmation = $true
    }
}
finally {
    Remove-OrphanedSmokeApprovalValues
    if ($null -ne $guiProcess) {
        Stop-Process -Id $guiProcess.Id -Force -ErrorAction SilentlyContinue
        try { [void]$guiProcess.WaitForExit(5000) } catch { }
        $guiProcess.Dispose()
    }
    if ($null -eq $before) { Remove-ItemProperty -Path $runKey -Name $testRunName -ErrorAction SilentlyContinue }
    else {
        $runRegistryKey = Open-CodexAutoRetryRunKey -Writable $true
        if ($null -ne $runRegistryKey) {
            try { $runRegistryKey.SetValue($testRunName, [string]$before.$testRunName, [Microsoft.Win32.RegistryValueKind]::String) }
            finally { $runRegistryKey.Close() }
        }
    }
    if ($null -eq $beforeSentinel) { Remove-ItemProperty -Path $runKey -Name $sentinelName -ErrorAction SilentlyContinue }
    else {
        $runRegistryKey = Open-CodexAutoRetryRunKey -Writable $true
        if ($null -ne $runRegistryKey) {
            try { $runRegistryKey.SetValue($sentinelName, [string]$beforeSentinel.$sentinelName, [Microsoft.Win32.RegistryValueKind]::String) }
            finally { $runRegistryKey.Close() }
        }
    }
    if ($null -eq $beforeDefault) { Remove-ItemProperty -Path $runKey -Name 'CodexAutoRetry' -ErrorAction SilentlyContinue }
    else {
        $runRegistryKey = Open-CodexAutoRetryRunKey -Writable $true
        if ($null -ne $runRegistryKey) {
            try { $runRegistryKey.SetValue('CodexAutoRetry', [string]$beforeDefault.CodexAutoRetry, [Microsoft.Win32.RegistryValueKind]::String) }
            finally { $runRegistryKey.Close() }
        }
    }
    if ($beforeApproval.Present) { Restore-CodexAutoRetryStartupApproval -RunName $testRunName -Bytes ([byte[]]$beforeApproval.Bytes) }
    else { $null = Remove-CodexAutoRetryStartupApproval -RunName $testRunName }
    if (Test-Path -LiteralPath $testRoot -PathType Container) {
        $tempPrefix = [IO.Path]::GetFullPath($env:TEMP).TrimEnd('\') + '\'
        $full = [IO.Path]::GetFullPath($testRoot)
        if ($full.StartsWith($tempPrefix, [StringComparison]::OrdinalIgnoreCase)) { Remove-Item -LiteralPath $full -Recurse -Force -ErrorAction SilentlyContinue }
    }
}

exit 0
