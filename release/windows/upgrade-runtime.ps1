function Assert-UpgradePlainPath {
    param([string]$Path)
    $current = Get-FullPath $Path
    while ($current) {
        if (Test-Path -LiteralPath $current) {
            $item = Get-Item -LiteralPath $current -Force
            if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw 'Upgrade backup/restore refuses linked paths.'
            }
        }
        $current = Split-Path -Parent $current
    }
}

function Backup-UpgradeRuntime {
    param([string]$RuntimePath, [string]$TransactionRoot, [string]$RunName = 'CodexAutoRetry')
    Assert-UpgradePlainPath $RuntimePath
    Assert-UpgradePlainPath $TransactionRoot
    $backup = Join-Path $TransactionRoot 'runtime-backup'
    New-Item -ItemType Directory -Path $backup | Out-Null
    $files = foreach ($name in @('codex-auto-retry.exe', 'codex-auto-retry-mcp.exe', 'settings.ps1')) {
        $path = Join-Path $RuntimePath $name
        Assert-UpgradePlainPath $path
        $present = Test-Path -LiteralPath $path -PathType Leaf
        $hash = ''
        if ($present) {
            Copy-Item -LiteralPath $path -Destination (Join-Path $backup $name)
            $hash = (Get-FileHash -LiteralPath (Join-Path $backup $name)).Hash
        }
        [pscustomobject]@{ name = $name; present = $present; hash = $hash }
    }
    $key = Open-CodexAutoRetryRunKey -Writable $false
    $run = $null
    try { if ($key) { $run = $key.GetValue($RunName, $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames) } }
    finally { if ($key) { $key.Close() } }
    $approval = Get-CodexAutoRetryStartupApproval -RunName $RunName
    $record = [pscustomobject]@{
        schema_version = 1; files = @($files); run_value = $run
        approval = if ($approval.Present) { [Convert]::ToBase64String([byte[]]$approval.Bytes) } else { $null }
    }
    Write-JsonAtomic -Path (Join-Path $backup 'snapshot.json') -Value $record
}

function Restore-UpgradeRuntime {
    param([string]$RuntimePath, [string]$TransactionRoot, [string]$RunName = 'CodexAutoRetry')
    Assert-UpgradePlainPath $RuntimePath
    Assert-UpgradePlainPath $TransactionRoot
    $backup = Join-Path $TransactionRoot 'runtime-backup'
    $record = Read-JsonDocument (Join-Path $backup 'snapshot.json')
    if ($null -eq $record -or $record.schema_version -ne 1) { throw 'Runtime rollback snapshot is missing or invalid.' }
    $names = @('codex-auto-retry.exe', 'codex-auto-retry-mcp.exe', 'settings.ps1')
    if (@($record.files).Count -ne $names.Count) { throw 'Runtime rollback file list is invalid.' }
    # Verify every backup before changing any target. Never guess after damage.
    foreach ($name in $names) {
        $entries = @($record.files | Where-Object name -eq $name)
        if ($entries.Count -ne 1) { throw 'Runtime rollback file identity is invalid.' }
        $entry = $entries[0]
        $source = Join-Path $backup $name
        Assert-UpgradePlainPath $source
        Assert-UpgradePlainPath (Join-Path $RuntimePath $name)
        if ($entry.present -and ((-not (Test-Path -LiteralPath $source -PathType Leaf)) -or
            (Get-FileHash -LiteralPath $source).Hash -ne $entry.hash)) { throw 'Runtime rollback backup checksum failed.' }
    }
    foreach ($entry in $record.files) {
        $target = Join-Path $RuntimePath $entry.name
        if ($entry.present) {
            Copy-Item -LiteralPath (Join-Path $backup $entry.name) -Destination $target -Force
            if ((Get-FileHash -LiteralPath $target).Hash -ne $entry.hash) { throw 'Runtime rollback verification failed.' }
        } else {
            Remove-Item -LiteralPath $target -Force -ErrorAction SilentlyContinue
            if (Test-Path -LiteralPath $target) { throw 'New runtime file could not be retired.' }
        }
    }
    $desired = '"{0}" supervise' -f (Join-Path $RuntimePath 'codex-auto-retry.exe')
    $key = Open-CodexAutoRetryRunKey -Writable $true
    try {
        $current = $key.GetValue($RunName, $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
        if ($current -eq $desired -or $current -eq $record.run_value) {
            $approval = Get-CodexAutoRetryStartupApproval -RunName $RunName
            $old = if ($null -ne $record.approval) { [Convert]::FromBase64String($record.approval) } else { $null }
            $actual = if ($approval.Present) { [byte[]]$approval.Bytes } else { $null }
            $written = Get-CodexAutoRetryStartupApprovalEnabledBytes -ExistingBytes $old
            if ((Test-CodexAutoRetryStartupApprovalBytes -Left $actual -Right $old) -or
                (Test-CodexAutoRetryStartupApprovalBytes -Left $actual -Right $written)) {
                if ($null -eq $record.run_value) { $key.DeleteValue($RunName, $false) }
                else { $key.SetValue($RunName, [string]$record.run_value, [Microsoft.Win32.RegistryValueKind]::String) }
                Restore-CodexAutoRetryStartupApproval -RunName $RunName -Bytes $old
            } else { Write-Warning 'Concurrent startup approval was preserved during rollback.' }
        } else { Write-Warning 'Concurrent startup entry was preserved during rollback.' }
    } finally { if ($key) { $key.Close() } }
    # State, control, config, logs and shared-server ownership are deliberately
    # not copied back. Fail-open cleanup owns routing; the worker stays stopped.
}
