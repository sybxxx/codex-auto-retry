function Get-InstallerDesktopState {
    try {
        $main = @(Get-CimInstance Win32_Process -OperationTimeoutSec 5 -ErrorAction Stop | Where-Object {
            ($_.Name -eq 'ChatGPT.exe' -or ($_.Name -eq 'Codex.exe' -and
                $_.ExecutablePath -match '\\app\\Codex\.exe$')) -and
            (-not $_.CommandLine -or $_.CommandLine -notmatch '(?:^|\s)--type=')
        })
        if ($main.Count -gt 0) { return 'running' }
        return 'closed'
    }
    catch {
        # Unknown process state is not permission to replace a live runtime.
        return 'unknown'
    }
}

function Show-CodexCloseNotice {
    param([string]$State, [int]$TimeoutSeconds)
    # ASCII source keeps the Chinese prompt valid in Windows PowerShell 5.1.
    $text = @'
{
  "running": "\u68c0\u6d4b\u5230 Codex \u4ecd\u5728\u8fd0\u884c\u3002\n\n\u8bf7\u5148\u4fdd\u5b58\u5de5\u4f5c\uff0c\u7136\u540e\u5b8c\u5168\u9000\u51fa Codex\uff08\u5305\u62ec\u6258\u76d8\uff09\u3002\n\u9000\u51fa\u540e\u70b9\u51fb\u201c\u91cd\u8bd5\u201d\uff0c\u5b89\u88c5\u7a0b\u5e8f\u4f1a\u91cd\u65b0\u68c0\u67e5\u5e76\u7ee7\u7eed\u3002\n\n\u4e0d\u4f1a\u5f3a\u5236\u5173\u95ed Codex\u3002\u70b9\u51fb\u201c\u53d6\u6d88\u201d\u53ef\u5b89\u5168\u9000\u51fa\u5b89\u88c5\u3002",
  "unknown": "\u65e0\u6cd5\u786e\u8ba4 Codex \u662f\u5426\u5df2\u9000\u51fa\uff08\u8fdb\u7a0b\u67e5\u8be2\u5931\u8d25\uff09\u3002\n\n\u4e3a\u907f\u514d\u4e2d\u65ad\u4efb\u52a1\uff0c\u6682\u4e0d\u66f4\u65b0\u3002\u8bf7\u786e\u8ba4\u5df2\u9000\u51fa Codex \u540e\u70b9\u51fb\u201c\u91cd\u8bd5\u201d\uff0c\u6216\u70b9\u51fb\u201c\u53d6\u6d88\u201d\u7ed3\u675f\u5b89\u88c5\u3002",
  "timeout": "\n\n\u7b49\u5f85\u8d85\u65f6\u5c06\u81ea\u52a8\u53d6\u6d88\u672c\u6b21\u5b89\u88c5\u3002"
}
'@ | ConvertFrom-Json
    $message = if ($State -eq 'running') { $text.running } else { $text.unknown }
    $shell = $null
    try {
        $shell = New-Object -ComObject WScript.Shell
        # Retry/Cancel, warning icon. 4 = Retry, 2 = Cancel, -1 = timeout.
        return [int]$shell.Popup(($message + $text.timeout), $TimeoutSeconds, 'Codex Auto Retry', 53)
    }
    catch {
        Write-Host 'Close Codex completely and rerun this installer. No process was closed.'
        return 2
    }
    finally {
        if ($shell -and [Runtime.InteropServices.Marshal]::IsComObject($shell)) {
            [void][Runtime.InteropServices.Marshal]::FinalReleaseComObject($shell)
        }
    }
}

function Wait-CodexInstallerExit {
    param(
        [ValidateRange(1, 600)][int]$TimeoutSeconds = 300,
        [ValidateRange(1, 50)][int]$MaxPrompts = 20
    )
    $watch = [Diagnostics.Stopwatch]::StartNew()
    try {
        for ($attempt = 0; $attempt -le $MaxPrompts; $attempt++) {
            $state = Get-InstallerDesktopState
            if ($state -eq 'closed') { return $true }
            $remaining = [int][Math]::Floor($TimeoutSeconds - $watch.Elapsed.TotalSeconds)
            if ($remaining -le 0 -or $attempt -eq $MaxPrompts) { return $false }
            Write-Host '[Codex Auto Retry] Waiting for Codex to close. Save your work, exit Codex, then choose Retry; Cancel leaves the installation unchanged.'
            if ((Show-CodexCloseNotice -State $state -TimeoutSeconds $remaining) -ne 4) { return $false }
        }
        return $false
    }
    finally { $watch.Stop() }
}
