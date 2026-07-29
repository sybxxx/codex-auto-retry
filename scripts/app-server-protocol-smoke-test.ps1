[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$testRoot = Join-Path $env:TEMP ("codex-auto-retry-protocol-" + [guid]::NewGuid().ToString('N'))
$testHome = Join-Path $testRoot 'codex-home'
$process = $null

function Find-CodexBinary {
    $npmRoot = Join-Path $env:APPDATA 'npm\node_modules\@openai\codex'
    if (Test-Path -LiteralPath $npmRoot) {
        $candidate = Get-ChildItem -LiteralPath $npmRoot -Recurse -Filter 'codex.exe' -File -ErrorAction SilentlyContinue |
            Select-Object -First 1
        if ($candidate) { return $candidate.FullName }
    }
    throw 'The standalone Codex CLI binary was not found for the isolated protocol test.'
}

function Start-AppServer {
    param([string]$CodexBinary, [string]$CodexHome)
    $startInfo = [System.Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $CodexBinary
    $startInfo.Arguments = 'app-server --stdio'
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.RedirectStandardInput = $true
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    $startInfo.StandardOutputEncoding = [System.Text.Encoding]::UTF8
    $startInfo.StandardErrorEncoding = [System.Text.Encoding]::UTF8
    $startInfo.EnvironmentVariables['CODEX_HOME'] = $CodexHome
    [Console]::InputEncoding = [System.Text.UTF8Encoding]::new($false)
    return [System.Diagnostics.Process]::Start($startInfo)
}

function Send-Request {
    param($Process, $Message)
    if ($Message -is [System.Collections.IDictionary] -and -not $Message.Contains('jsonrpc')) {
        $Message['jsonrpc'] = '2.0'
    }
    $Process.StandardInput.WriteLine(($Message | ConvertTo-Json -Compress -Depth 20))
    $Process.StandardInput.Flush()
}

function Read-UntilId {
    param($Process, [int]$Id, [int]$TimeoutMs = 10000)
    $deadline = [DateTime]::UtcNow.AddMilliseconds($TimeoutMs)
    $lines = @()
    while ([DateTime]::UtcNow -lt $deadline) {
        $remaining = [Math]::Max(1, [int]($deadline - [DateTime]::UtcNow).TotalMilliseconds)
        $read = $Process.StandardOutput.ReadLineAsync()
        if (-not $read.Wait($remaining)) { break }
        $line = $read.Result
        if ($null -eq $line) { break }
        $lines += $line
        $message = $line | ConvertFrom-Json
        if ($message.id -eq $Id) {
            if ($message.error) { throw "App-server request $Id failed: $($message.error.message)" }
            return [pscustomobject]@{ Response = $message; Lines = $lines }
        }
    }
    throw "Timed out waiting for app-server response $Id."
}

function Initialize-AppServer {
    param($Process)
    Send-Request $Process @{
        method = 'initialize'
        id = 1
        params = @{
            clientInfo = @{ name = 'codex_auto_retry_protocol_test'; version = '0.0.0' }
            capabilities = @{ experimentalApi = $true }
        }
    }
    $null = Read-UntilId $Process 1
    Send-Request $Process @{ method = 'initialized'; params = @{} }
}

function Stop-AppServer {
    param($Process)
    if (-not $Process) { return }
    try { $Process.StandardInput.Close() } catch { }
    if (-not $Process.WaitForExit(3000)) { $Process.Kill() }
    $Process.Dispose()
}

function Assert-ResumeSettingsPreserved {
    param($Started, $Resumed, [string]$Label)
    foreach ($field in @(
        'model', 'modelProvider', 'serviceTier', 'cwd', 'runtimeWorkspaceRoots',
        'approvalPolicy', 'approvalsReviewer', 'sandbox', 'activePermissionProfile',
        'reasoningEffort'
    )) {
        $before = $Started.$field | ConvertTo-Json -Compress -Depth 20
        $after = $Resumed.$field | ConvertTo-Json -Compress -Depth 20
        if ($before -ne $after) {
            throw "$Label resume changed $field (before=$before, after=$after)."
        }
    }
}

try {
    New-Item -ItemType Directory -Force -Path $testHome | Out-Null
    $secondaryWorkspace = Join-Path $testRoot 'secondary-workspace'
    New-Item -ItemType Directory -Force -Path $secondaryWorkspace | Out-Null
    $codexBinary = Find-CodexBinary

    $threadSettings = @{
        cwd = $testHome
        runtimeWorkspaceRoots = @($testHome, $secondaryWorkspace)
        approvalPolicy = 'never'
        approvalsReviewer = 'user'
        permissions = ':danger-full-access'
        model = 'gpt-5.3-codex'
        modelProvider = 'openai'
        personality = 'pragmatic'
        serviceTier = 'priority'
        config = @{
            model_reasoning_effort = 'high'
            model_reasoning_summary = 'auto'
        }
    }

    $process = Start-AppServer -CodexBinary $codexBinary -CodexHome $testHome
    Initialize-AppServer $process
    Send-Request $process @{ method = 'thread/start'; id = 2; params = $threadSettings }
    $goalThread = Read-UntilId $process 2
    $goalThreadId = $goalThread.Response.result.thread.id
    Send-Request $process @{ method = 'thread/goal/set'; id = 3; params = @{ threadId = $goalThreadId; objective = 'Isolated goal retry test'; status = 'paused' } }
    $pausedGoal = Read-UntilId $process 3
    Send-Request $process @{ method = 'thread/start'; id = 4; params = $threadSettings }
    $normalThread = Read-UntilId $process 4
    $normalThreadId = $normalThread.Response.result.thread.id
    Send-Request $process @{ method = 'thread/goal/set'; id = 5; params = @{ threadId = $normalThreadId; objective = 'Materialize normal retry test'; status = 'paused' } }
    $null = Read-UntilId $process 5
    Send-Request $process @{ method = 'thread/goal/clear'; id = 6; params = @{ threadId = $normalThreadId } }
    $null = Read-UntilId $process 6
    Send-Request $process @{ method = 'thread/start'; id = 7; params = $threadSettings }
    $heldConversationThread = Read-UntilId $process 7
    $heldConversationThreadId = $heldConversationThread.Response.result.thread.id
    Send-Request $process @{ method = 'thread/goal/set'; id = 8; params = @{ threadId = $heldConversationThreadId; objective = 'Held goal conversation test'; status = 'paused' } }
    $null = Read-UntilId $process 8
    Send-Request $process @{ method = 'thread/start'; id = 9; params = $threadSettings }
    $injectionThread = Read-UntilId $process 9
    $injectionThreadId = $injectionThread.Response.result.thread.id
    Send-Request $process @{ method = 'thread/goal/set'; id = 10; params = @{ threadId = $injectionThreadId; objective = 'Materialize injection protocol test'; status = 'paused' } }
    $null = Read-UntilId $process 10
    Send-Request $process @{ method = 'thread/goal/clear'; id = 11; params = @{ threadId = $injectionThreadId } }
    $null = Read-UntilId $process 11
    Stop-AppServer $process
    $process = $null

    $process = Start-AppServer -CodexBinary $codexBinary -CodexHome $testHome
    Initialize-AppServer $process
    $captured = @()
    $goalResumeParams = $threadSettings.Clone()
    $goalResumeParams.threadId = $goalThreadId
    $goalResumeParams.excludeTurns = $true
    Send-Request $process @{ method = 'thread/resume'; id = 2; params = $goalResumeParams }
    $response = Read-UntilId $process 2
    $captured += $response.Lines
    Assert-ResumeSettingsPreserved -Started $goalThread.Response.result -Resumed $response.Response.result -Label 'Goal task'
    Send-Request $process @{ method = 'thread/goal/get'; id = 3; params = @{ threadId = $goalThreadId } }
    $goalBefore = Read-UntilId $process 3
    $captured += $goalBefore.Lines
    Send-Request $process @{ method = 'thread/goal/set'; id = 4; params = @{ threadId = $goalThreadId; status = 'active' } }
    $goalActivated = Read-UntilId $process 4
    $captured += $goalActivated.Lines

    $normalResumeParams = $threadSettings.Clone()
    $normalResumeParams.threadId = $normalThreadId
    $normalResumeParams.excludeTurns = $true
    Send-Request $process @{ method = 'thread/resume'; id = 5; params = $normalResumeParams }
    $normalResume = Read-UntilId $process 5
    $captured += $normalResume.Lines
    Assert-ResumeSettingsPreserved -Started $normalThread.Response.result -Resumed $normalResume.Response.result -Label 'Normal task'
    Send-Request $process @{
        method = 'turn/start'
        id = 6
        params = @{ threadId = $normalThreadId; input = @() }
    }
    $normalTurn = Read-UntilId $process 6
    $captured += $normalTurn.Lines
    Start-Sleep -Milliseconds 800
    Send-Request $process @{ method = 'thread/read'; id = 7; params = @{ threadId = $normalThreadId; includeTurns = $false } }
    $finalRead = Read-UntilId $process 7
    $captured += $finalRead.Lines

    $heldResumeParams = $threadSettings.Clone()
    $heldResumeParams.threadId = $heldConversationThreadId
    $heldResumeParams.excludeTurns = $true
    Send-Request $process @{ method = 'thread/resume'; id = 8; params = $heldResumeParams }
    $heldResume = Read-UntilId $process 8
    $captured += $heldResume.Lines
    Assert-ResumeSettingsPreserved -Started $heldConversationThread.Response.result -Resumed $heldResume.Response.result -Label 'Held-goal conversation task'
    Send-Request $process @{ method = 'thread/goal/get'; id = 9; params = @{ threadId = $heldConversationThreadId } }
    $heldGoalBefore = Read-UntilId $process 9
    $captured += $heldGoalBefore.Lines
    Send-Request $process @{ method = 'turn/start'; id = 10; params = @{ threadId = $heldConversationThreadId; input = @() } }
    $heldTurn = Read-UntilId $process 10
    $captured += $heldTurn.Lines
    Start-Sleep -Milliseconds 800
    Send-Request $process @{ method = 'thread/goal/get'; id = 11; params = @{ threadId = $heldConversationThreadId } }
    $heldGoalAfter = Read-UntilId $process 11
    $captured += $heldGoalAfter.Lines

    $recoveryEventId = 'car-0123456789abcdef01234567'
    $noticePayload = @{
        parent_thread_id = $injectionThreadId
        child_thread_id = $normalThreadId
        recovery_event_id = $recoveryEventId
        action = 'resume_existing_child'
        spawn_replacement = $false
        instruction = 'The watchdog is resuming the exact existing child. Do not resume or spawn any child for this recovery event.'
    } | ConvertTo-Json -Compress
    $noticeText = 'codex-auto-retry:subagent-empty-response-recovery:v1:' + $noticePayload
    $injectionResumeParams = $threadSettings.Clone()
    $injectionResumeParams.threadId = $injectionThreadId
    $injectionResumeParams.excludeTurns = $true
    Send-Request $process @{ method = 'thread/resume'; id = 12; params = $injectionResumeParams }
    $injectionResume = Read-UntilId $process 12
    $captured += $injectionResume.Lines
    Send-Request $process @{
        method = 'thread/inject_items'
        id = 13
        params = @{
            threadId = $injectionThreadId
            items = @(@{
                type = 'message'
                id = 'msg_codex_auto_retry_0123456789abcdef01234567'
                role = 'developer'
                content = @(@{ type = 'input_text'; text = $noticeText })
            })
        }
    }
    $injected = Read-UntilId $process 13
    $captured += $injected.Lines

    Send-Request $process @{ method = 'thread/goal/set'; id = 14; params = @{ threadId = $goalThreadId; status = 'blocked' } }
    $goalBlocked = Read-UntilId $process 14
    $captured += $goalBlocked.Lines
    Send-Request $process @{ method = 'thread/goal/get'; id = 15; params = @{ threadId = $goalThreadId } }
    $goalAfterBlock = Read-UntilId $process 15
    $captured += $goalAfterBlock.Lines

    $startedThreads = @($captured | ForEach-Object {
        $message = $_ | ConvertFrom-Json
        if ($message.method -eq 'turn/started') { $message.params.threadId }
    } | Where-Object { $_ } | Select-Object -Unique)
    if ($pausedGoal.Response.result.goal.status -ne 'paused' -or $goalBefore.Response.result.goal.status -ne 'paused') {
        throw 'Paused goal state was not preserved across app-server restart.'
    }
    if ($goalActivated.Response.result.goal.status -ne 'active') {
        throw 'Goal activation was not accepted.'
    }
    if ($startedThreads -notcontains $goalThreadId) {
        throw 'Activating the interrupted goal did not start native goal continuation.'
    }
    if ($startedThreads -notcontains $normalThreadId) {
        throw 'Starting a normal continuation did not create a turn in the same task.'
    }
    if ($startedThreads -notcontains $heldConversationThreadId) {
        throw 'Starting a conversation after pausing its goal did not create a same-task turn.'
    }
    if ($heldGoalBefore.Response.result.goal.status -ne 'paused' -or $heldGoalAfter.Response.result.goal.status -ne 'paused') {
        throw 'The held-goal conversation continuation changed the goal pause state.'
    }
    if ($goalBlocked.Response.result.goal.status -ne 'blocked' -or $goalAfterBlock.Response.result.goal.status -ne 'blocked') {
        throw 'An active goal could not be changed to blocked at the retry limit.'
    }
    $parentRollouts = @(Get-ChildItem -LiteralPath (Join-Path $testHome 'sessions') -Recurse -Filter ("*-$injectionThreadId.jsonl") -File)
    if ($parentRollouts.Count -ne 1 -or -not (Select-String -LiteralPath $parentRollouts[0].FullName -SimpleMatch $recoveryEventId -Quiet)) {
        throw 'thread/inject_items did not persist the recovery event in the declared parent thread.'
    }

    [pscustomobject]@{
        Status = 'passed'
        IsolatedCodexHome = $true
        GoalStatePreserved = $true
        ThreadSettingsPreserved = $true
        GoalNativeContinuationStarted = $true
        SilentNormalSameTaskTurnStarted = $true
        HeldGoalConversationStarted = $true
        HeldGoalRemainedPaused = $true
        ParentRecoveryEventInjected = $true
        ActiveGoalBlockedAtLimit = $true
        CodexAppUIUsed = $false
    }
}
finally {
    Stop-AppServer $process
    if (Test-Path -LiteralPath $testRoot) {
        $resolvedTestRoot = [System.IO.Path]::GetFullPath($testRoot)
        $resolvedTemp = [System.IO.Path]::GetFullPath($env:TEMP).TrimEnd('\') + '\'
        if (-not $resolvedTestRoot.StartsWith($resolvedTemp, [System.StringComparison]::OrdinalIgnoreCase)) {
            throw "Refusing to remove unexpected protocol-test path: $resolvedTestRoot"
        }
        $cleanupDeadline = (Get-Date).AddSeconds(8)
        do {
            try {
                Remove-Item -LiteralPath $resolvedTestRoot -Recurse -Force -ErrorAction Stop
            }
            catch {
                if ((Get-Date) -ge $cleanupDeadline) { throw }
                Start-Sleep -Milliseconds 300
            }
        } while (Test-Path -LiteralPath $resolvedTestRoot)
    }
}
