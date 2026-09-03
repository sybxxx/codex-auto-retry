$script:CodexAutoRetryStartupApprovedSubKey = 'Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run'
$script:CodexAutoRetryRunSubKey = 'Software\Microsoft\Windows\CurrentVersion\Run'

function Open-CodexAutoRetryRunKey {
    param([bool]$Writable)

    $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey(
        $script:CodexAutoRetryRunSubKey,
        $Writable
    )
    if ($null -eq $key -and $Writable) {
        # CreateSubKey opens or creates the key without the PowerShell
        # provider's -Force behavior, which can replace the whole key on
        # Windows PowerShell 5.1 and remove unrelated startup values.
        $key = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey(
            $script:CodexAutoRetryRunSubKey,
            $true
        )
    }
    return $key
}

function Open-CodexAutoRetryStartupApprovedKey {
    param([bool]$Writable)
    return [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey(
        $script:CodexAutoRetryStartupApprovedSubKey,
        $Writable
    )
}

function Get-CodexAutoRetryStartupApprovalStatus {
    param([AllowNull()][byte[]]$Bytes)

    if ($null -eq $Bytes -or $Bytes.Length -lt 4) { return 'unknown' }
    if ($Bytes[1] -ne 0 -or $Bytes[2] -ne 0 -or $Bytes[3] -ne 0) { return 'unknown' }
    if ($Bytes[0] -eq 2) { return 'enabled' }
    if ($Bytes[0] -eq 3) { return 'disabled' }
    return 'unknown'
}

function Get-CodexAutoRetryStartupApproval {
    param([string]$RunName = 'CodexAutoRetry')

    $key = Open-CodexAutoRetryStartupApprovedKey -Writable $false
    if ($null -eq $key) {
        return [pscustomobject][ordered]@{ Status = 'unknown'; Present = $false; Bytes = $null }
    }
    try {
        $value = $key.GetValue($RunName, $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
        if ($null -eq $value -or $value -isnot [byte[]]) {
            return [pscustomobject][ordered]@{ Status = 'unknown'; Present = $false; Bytes = $null }
        }
        $bytes = [byte[]]$value
        $status = Get-CodexAutoRetryStartupApprovalStatus -Bytes $bytes
        return [pscustomobject][ordered]@{ Status = $status; Present = $true; Bytes = $bytes }
    }
    finally {
        $key.Close()
    }
}

function Test-CodexAutoRetryStartupApprovalBytes {
    param(
        [AllowNull()][byte[]]$Left,
        [AllowNull()][byte[]]$Right
    )

    if ($null -eq $Left -or $null -eq $Right) {
        return $null -eq $Left -and $null -eq $Right
    }
    if ($Left.Length -ne $Right.Length) { return $false }
    for ($index = 0; $index -lt $Left.Length; $index++) {
        if ($Left[$index] -ne $Right[$index]) { return $false }
    }
    return $true
}

function Get-CodexAutoRetryStartupApprovalEnabledBytes {
    param([AllowNull()][byte[]]$ExistingBytes)

    if ($null -ne $ExistingBytes -and $ExistingBytes.Length -ge 12) {
        $bytes = [byte[]]$ExistingBytes.Clone()
    }
    else {
        $bytes = [byte[]](0..11 | ForEach-Object { [byte]0 })
    }
    $bytes[0] = 2
    $bytes[1] = 0
    $bytes[2] = 0
    $bytes[3] = 0
    return $bytes
}

function Set-CodexAutoRetryStartupApprovalEnabled {
    param([string]$RunName = 'CodexAutoRetry')

    $existing = Get-CodexAutoRetryStartupApproval -RunName $RunName
    [byte[]]$bytes = @(Get-CodexAutoRetryStartupApprovalEnabledBytes -ExistingBytes $(if ($existing.Present) { [byte[]]$existing.Bytes } else { $null }))

    $key = Open-CodexAutoRetryStartupApprovedKey -Writable $true
    if ($null -eq $key) {
        $key = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey($script:CodexAutoRetryStartupApprovedSubKey, $true)
    }
    if ($null -eq $key) { throw 'The current-user startup approval registry key could not be opened.' }
    try {
        $key.SetValue($RunName, $bytes, [Microsoft.Win32.RegistryValueKind]::Binary)
    }
    finally {
        $key.Close()
    }
    $actual = Get-CodexAutoRetryStartupApproval -RunName $RunName
    if ($actual.Status -ne 'enabled') {
        throw "The startup approval for $RunName could not be enabled."
    }
    return $actual
}

function Remove-CodexAutoRetryStartupApproval {
    param([string]$RunName = 'CodexAutoRetry')

    $key = Open-CodexAutoRetryStartupApprovedKey -Writable $true
    if ($null -eq $key) { return $false }
    try {
        $value = $key.GetValue($RunName, $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
        if ($null -eq $value) { return $false }
        $key.DeleteValue($RunName, $false)
    }
    finally {
        $key.Close()
    }
    if ((Get-CodexAutoRetryStartupApproval -RunName $RunName).Present) {
        throw "The startup approval for $RunName is still present after removal."
    }
    return $true
}

function Restore-CodexAutoRetryStartupApproval {
    param(
        [string]$RunName = 'CodexAutoRetry',
        [AllowNull()][byte[]]$Bytes
    )

    if ($null -eq $Bytes -or $Bytes.Length -eq 0) {
        $null = Remove-CodexAutoRetryStartupApproval -RunName $RunName
        return
    }
    $key = Open-CodexAutoRetryStartupApprovedKey -Writable $true
    if ($null -eq $key) {
        $key = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey($script:CodexAutoRetryStartupApprovedSubKey, $true)
    }
    if ($null -eq $key) { throw 'The current-user startup approval registry key could not be opened.' }
    try {
        $key.SetValue($RunName, $Bytes, [Microsoft.Win32.RegistryValueKind]::Binary)
    }
    finally {
        $key.Close()
    }
}
