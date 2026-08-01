[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$binary = Join-Path $PSScriptRoot 'bin\codex-auto-retry-mcp.exe'
$testRoot = Join-Path $env:TEMP ("codex-auto-retry-mcp-" + [guid]::NewGuid().ToString('N'))
$dataDir = Join-Path $testRoot 'data'
$process = $null

function Send-MCPMessage {
    param($Process, $Message)
    $json = ($Message | ConvertTo-Json -Compress -Depth 30) + "`n"
    $bytes = [System.Text.UTF8Encoding]::new($false).GetBytes($json)
    $Process.StandardInput.BaseStream.Write($bytes, 0, $bytes.Length)
    $Process.StandardInput.BaseStream.Flush()
}

function Read-MCPResponse {
    param($Process, [int]$Id, [int]$TimeoutMs = 10000)
    $deadline = [DateTime]::UtcNow.AddMilliseconds($TimeoutMs)
    while ([DateTime]::UtcNow -lt $deadline) {
        $remaining = [Math]::Max(1, [int]($deadline - [DateTime]::UtcNow).TotalMilliseconds)
        $read = $Process.StandardOutput.ReadLineAsync()
        if (-not $read.Wait($remaining)) { break }
        $line = $read.Result
        if ($null -eq $line) { break }
        $message = $line | ConvertFrom-Json
        if ($message.id -eq $Id) {
            if ($message.error) { throw "MCP request $Id failed: $($message.error.message)" }
            return $message.result
        }
    }
    if ($Process.HasExited) {
        $stderr = $Process.StandardError.ReadToEnd()
        throw "MCP process exited while waiting for response $Id (exit=$($Process.ExitCode), stderr=$stderr)."
    }
    throw "Timed out waiting for MCP response $Id."
}

function Call-MCPTool {
    param($Process, [int]$Id, [string]$Name, $Arguments = @{})
    Send-MCPMessage $Process @{
        jsonrpc = '2.0'
        id = $Id
        method = 'tools/call'
        params = @{ name = $Name; arguments = $Arguments }
    }
    return Read-MCPResponse $Process $Id
}

try {
    if (-not (Test-Path -LiteralPath $binary)) {
        throw "MCP binary not found: $binary"
    }
    New-Item -ItemType Directory -Force -Path $dataDir | Out-Null

    $threadId = '019f9d5d-9c82-75b1-b7c0-20a658af0423'
    $stoppedThreadId = '019f9d5d-9c82-75b1-b7c0-20a658af0424'
    $state = @{
        version = 4
        initialized = $true
        files = @{}
        processed_events = @{}
        threads = @{
            $threadId = @{
                recovery_attempts = 4
                consecutive_retries = 1
                pending = @{
                    event_key = 'smoke-event'
                    turn_id = 'failed-turn'
                    class = 'server'
                    due_at = [DateTime]::UtcNow.AddMinutes(1).ToString('o')
                    codex_home = $dataDir
                    attempt = 4
                    max_attempts = 15
                    consecutive_retry = 1
                    max_consecutive_retries = 5
                }
            }
            $stoppedThreadId = @{
                recovery_attempts = 15
                consecutive_retries = 5
                stopped = @{
                    event_key = 'stopped-event'
                    failed_turn_id = 'failed-turn'
                    class = 'server'
                    stopped_at = [DateTime]::UtcNow.ToString('o')
                    attempts = 15
                    max_attempts = 15
                    consecutive_retries = 5
                    max_consecutive_retries = 5
                    reason = 'recovery_attempt_limit'
                }
            }
        }
    }
    $stateJson = $state | ConvertTo-Json -Depth 20
    [System.IO.File]::WriteAllText(
        (Join-Path $dataDir 'state.json'),
        $stateJson,
        [System.Text.UTF8Encoding]::new($false)
    )
    $status = @{
        version = '0.7.1'
        running = $true
        pid = $PID
        started_at = [DateTime]::UtcNow.AddMinutes(-1).ToString('o')
        last_scan_at = [DateTime]::UtcNow.ToString('o')
        watched_roots = 1
        pending_retries = 1
        active_retries = 0
        paused = $false
        log_path = (Join-Path $dataDir 'logs\daemon.log')
    }
    [System.IO.File]::WriteAllText(
        (Join-Path $dataDir 'status.json'),
        ($status | ConvertTo-Json -Depth 5),
        [System.Text.UTF8Encoding]::new($false)
    )

    $startInfo = [System.Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $binary
    $startInfo.Arguments = ('mcp --data-dir "{0}"' -f $dataDir)
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.RedirectStandardInput = $true
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    $startInfo.StandardOutputEncoding = [System.Text.Encoding]::UTF8
    $startInfo.StandardErrorEncoding = [System.Text.Encoding]::UTF8
    [Console]::InputEncoding = [System.Text.UTF8Encoding]::new($false)
    $process = [System.Diagnostics.Process]::Start($startInfo)

    Send-MCPMessage $process @{
        jsonrpc = '2.0'
        id = 1
        method = 'initialize'
        params = @{
            protocolVersion = '2025-06-18'
            capabilities = @{}
            clientInfo = @{ name = 'codex-auto-retry-smoke'; version = '1.0.0' }
        }
    }
    $initialize = Read-MCPResponse $process 1
    if ($initialize.serverInfo.name -ne 'codex-auto-retry') { throw 'Unexpected MCP server identity.' }
    Send-MCPMessage $process @{ jsonrpc = '2.0'; method = 'notifications/initialized'; params = @{} }

    Send-MCPMessage $process @{ jsonrpc = '2.0'; id = 2; method = 'tools/list'; params = @{} }
    $tools = Read-MCPResponse $process 2
    $expectedTools = @(
        'get_auto_retry_status',
        'set_retry_prompt',
        'set_retry_settings',
        'set_auto_retry_paused',
        'retry_now',
        'cancel_retry',
        'restart_retry'
    )
    foreach ($name in $expectedTools) {
        $tool = @($tools.tools | Where-Object name -eq $name)
        if ($tool.Count -ne 1) { throw "MCP tool missing: $name" }
        if ($tool[0]._meta.ui.resourceUri -ne 'ui://codex-auto-retry/management-panel') {
            throw "Nested UI metadata missing for tool: $name"
        }
        if ($tool[0]._meta.'ui/resourceUri' -ne 'ui://codex-auto-retry/management-panel') {
            throw "Compatibility UI metadata missing for tool: $name"
        }
    }

    Send-MCPMessage $process @{ jsonrpc = '2.0'; id = 3; method = 'resources/read'; params = @{ uri = 'ui://codex-auto-retry/management-panel' } }
    $resource = Read-MCPResponse $process 3
    $content = @($resource.contents)[0]
    if ($content.mimeType -ne 'text/html;profile=mcp-app' -or $content.text.Length -lt 50000) {
        throw 'Embedded MCP App resource is missing or incomplete.'
    }
    if (-not $content.text.Contains('Codex Auto Retry')) { throw 'Embedded panel identity is missing.' }
    $recoveryCounterLabel = ([char]0x672c).ToString() + [char]0x6b21 + [char]0x6545 + [char]0x969c + [char]0x6062 + [char]0x590d
    $consecutiveCounterLabel = ([char]0x8fde).ToString() + [char]0x7eed + [char]0x65e0 + [char]0x8fdb + [char]0x5c55
    if (-not $content.text.Contains($recoveryCounterLabel) -or -not $content.text.Contains($consecutiveCounterLabel)) {
        throw 'Embedded panel does not distinguish the two retry counters.'
    }

    $defaultPrompt = ([char]0x7ee7).ToString() + [char]0x7eed
    $updatedPrompt = $defaultPrompt + [char]0x5904 + [char]0x7406
    $status = Call-MCPTool $process 4 'get_auto_retry_status'
    $pendingRetry = @($status.structuredContent.retries | Where-Object thread_id -eq $threadId)[0]
    if ($status.structuredContent.retry_prompt -ne $defaultPrompt -or
        $status.structuredContent.pending_retries -ne 1 -or
        $status.structuredContent.stopped_retries -ne 1 -or
        $status.structuredContent.max_consecutive_retries -ne 5 -or
        $status.structuredContent.max_recovery_attempts -ne 15 -or
        $status.structuredContent.delay_strategy -ne 'exponential' -or
        $pendingRetry.recovery_attempt -ne 4 -or
        $pendingRetry.consecutive_retry -ne 1) {
        throw "Status tool returned unexpected management state (prompt=$($status.structuredContent.retry_prompt), pending=$($status.structuredContent.pending_retries))."
    }
    $updated = Call-MCPTool $process 5 'set_retry_prompt' @{ prompt = $updatedPrompt }
    if ($updated.structuredContent.retry_prompt -ne $updatedPrompt) { throw 'Prompt update did not take effect.' }
    $settings = Call-MCPTool $process 6 'set_retry_settings' @{
        retry_prompt = $updatedPrompt
        max_consecutive_retries = 100
        max_recovery_attempts = 1000
        initial_delay_seconds = 9
        max_delay_seconds = 120
        delay_increment_seconds = 7
        delay_strategy = 'fixed'
        show_notifications = $false
    }
    if ($settings.structuredContent.max_consecutive_retries -ne 100 -or
        $settings.structuredContent.max_recovery_attempts -ne 1000 -or
        $settings.structuredContent.initial_delay_seconds -ne 9 -or
        $settings.structuredContent.max_delay_seconds -ne 120 -or
        $settings.structuredContent.delay_increment_seconds -ne 7 -or
        $settings.structuredContent.delay_strategy -ne 'fixed' -or
        $settings.structuredContent.show_notifications) {
        throw "Full settings update did not take effect: $($settings.structuredContent | ConvertTo-Json -Compress -Depth 10)"
    }
    $paused = Call-MCPTool $process 7 'set_auto_retry_paused' @{ paused = $true }
    if (-not $paused.structuredContent.paused) { throw 'Pause control did not take effect.' }
    $null = Call-MCPTool $process 8 'retry_now' @{ thread_id = $threadId }
    $null = Call-MCPTool $process 9 'cancel_retry' @{ thread_id = $threadId }
    $null = Call-MCPTool $process 10 'restart_retry' @{ thread_id = $stoppedThreadId }
    $commands = @(Get-ChildItem -LiteralPath (Join-Path $dataDir 'commands') -Filter '*.json' -File)
    if ($commands.Count -ne 3) { throw 'Thread controls were not queued atomically.' }

    [pscustomobject]@{
        Status = 'passed'
        Tools = $expectedTools.Count
        EmbeddedPanel = $true
        PromptUpdated = $true
        SettingsUpdated = $true
        PauseUpdated = $true
        CommandsQueued = $commands.Count
    }
}
finally {
    if ($process) {
        try { $process.StandardInput.Close() } catch { }
        if (-not $process.WaitForExit(3000)) { $process.Kill() }
        $process.Dispose()
    }
    if (Test-Path -LiteralPath $testRoot) {
        $resolvedTestRoot = [System.IO.Path]::GetFullPath($testRoot)
        $resolvedTemp = [System.IO.Path]::GetFullPath($env:TEMP).TrimEnd('\') + '\'
        if (-not $resolvedTestRoot.StartsWith($resolvedTemp, [System.StringComparison]::OrdinalIgnoreCase)) {
            throw "Refusing to remove unexpected smoke-test path: $resolvedTestRoot"
        }
        Remove-Item -LiteralPath $resolvedTestRoot -Recurse -Force
    }
}
