[CmdletBinding()]
param(
    [switch]$KeepArtifacts
)

$ErrorActionPreference = 'Stop'
$binary = Join-Path $PSScriptRoot 'bin\codex-auto-retry.exe'
$sourceDir = Join-Path $PSScriptRoot 'source'
if (-not (Test-Path -LiteralPath $binary)) {
    throw "Built watchdog not found: $binary"
}

$testRoot = Join-Path $env:TEMP ("codex-auto-retry-smoke-" + [guid]::NewGuid().ToString('N'))
$dataDir = Join-Path $testRoot 'runtime'
$codexHome = Join-Path $testRoot 'codex-home'
$sessions = Join-Path $codexHome 'sessions'
$mockBinary = Join-Path $testRoot 'mock-cdp.exe'
$mockPortPath = Join-Path $testRoot 'mock-cdp-port.json'
$invocationsPath = Join-Path $testRoot 'renderer-invocations.jsonl'
$mockStdoutPath = Join-Path $testRoot 'mock-cdp.stdout.log'
$mockStderrPath = Join-Path $testRoot 'mock-cdp.stderr.log'
$statusPath = Join-Path $dataDir 'status.json'
$statePath = Join-Path $dataDir 'state.json'
$process = $null
$mockProcess = $null

function Write-LifecycleEvent {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$Kind,
        [Parameter(Mandatory = $true)][string]$TurnId,
        $ErrorValue,
        $LastAgentMessage,
        [switch]$IncludeLastAgentMessage,
        [switch]$Append
    )
    $payload = [ordered]@{ type = $Kind; turn_id = $TurnId }
    if ($Kind -eq 'task_complete') {
        $payload.error = $ErrorValue
        if ($IncludeLastAgentMessage) { $payload.last_agent_message = $LastAgentMessage }
    }
    $line = [ordered]@{
        timestamp = (Get-Date).ToUniversalTime().ToString('o')
        type = 'event_msg'
        payload = $payload
    } | ConvertTo-Json -Compress -Depth 5
    if ($Append) {
        [System.IO.File]::AppendAllText($Path, $line + "`n", [System.Text.UTF8Encoding]::new($false))
    }
    else {
        [System.IO.File]::WriteAllText($Path, $line + "`n", [System.Text.UTF8Encoding]::new($false))
    }
}

function Write-TurnContext {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$Cwd,
        [Parameter(Mandatory = $true)][string]$Model,
        [Parameter(Mandatory = $true)][string]$Effort
    )
    $line = [ordered]@{
        timestamp = (Get-Date).ToUniversalTime().ToString('o')
        type = 'turn_context'
        payload = [ordered]@{
            turn_id = 'failed-turn'
            cwd = $Cwd
            workspace_roots = @($Cwd, $codexHome)
            approval_policy = 'never'
            approvals_reviewer = 'user'
            sandbox_policy = @{ type = 'danger-full-access' }
            permission_profile = @{ type = 'disabled' }
            model = $Model
            personality = 'pragmatic'
            effort = $Effort
            summary = 'auto'
            developer_instructions = 'private-smoke-setting-must-not-be-forwarded'
        }
    } | ConvertTo-Json -Compress -Depth 10
    [System.IO.File]::WriteAllText($Path, $line + "`n", [System.Text.UTF8Encoding]::new($false))
}

function Get-ThreadState {
    param([string]$ThreadId)
    if (-not (Test-Path -LiteralPath $statePath)) { return $null }
    try {
        $state = Get-Content -Raw -Encoding UTF8 -LiteralPath $statePath | ConvertFrom-Json
        $property = $state.threads.PSObject.Properties[$ThreadId]
        if ($property) { return $property.Value }
    }
    catch { }
    return $null
}

function Get-RendererInvocations {
    if (-not (Test-Path -LiteralPath $invocationsPath)) { return @() }
    return @(Get-Content -Encoding UTF8 -LiteralPath $invocationsPath |
        Where-Object { $_.Trim() } |
        ForEach-Object { $_ | ConvertFrom-Json })
}

try {
    New-Item -ItemType Directory -Force -Path $dataDir, $sessions | Out-Null
    Push-Location $sourceDir
    try {
        & go build -o $mockBinary ./cmd/mock-cdp
        if ($LASTEXITCODE -ne 0) { throw 'Could not build the mock background channel.' }
    }
    finally {
        Pop-Location
    }

    $mockProcess = Start-Process -FilePath $mockBinary -ArgumentList @(
        '--port-file', $mockPortPath,
        '--invocations', $invocationsPath
    ) -WindowStyle Hidden -RedirectStandardOutput $mockStdoutPath -RedirectStandardError $mockStderrPath -PassThru
    $deadline = (Get-Date).AddSeconds(10)
    do {
        Start-Sleep -Milliseconds 100
    } while (-not (Test-Path -LiteralPath $mockPortPath) -and (Get-Date) -lt $deadline)
    if (-not (Test-Path -LiteralPath $mockPortPath)) {
        throw 'Mock background channel did not publish its port.'
    }
    $mockPort = [int](Get-Content -Raw -Encoding UTF8 -LiteralPath $mockPortPath | ConvertFrom-Json)

    $config = [ordered]@{
        config_version = 4
        poll_interval_seconds = 1
        initial_delay_seconds = 1
        max_delay_seconds = 4
        delay_strategy = 'exponential'
        max_consecutive_retries = 5
        max_recovery_attempts = 15
        max_parallel_retries = 2
        start_ack_timeout_seconds = 10
        auth_max_attempts = 3
        unknown_max_attempts = 2
        session_roots = @($codexHome)
        include_default_home = $false
        include_cockpit_homes = $false
        renderer_debug_port = $mockPort
        retry_prompt = 'Continue smoke test.'
        show_notifications = $false
    }
    [System.IO.File]::WriteAllText(
        (Join-Path $dataDir 'config.json'),
        ($config | ConvertTo-Json -Depth 5),
        [System.Text.UTF8Encoding]::new($false)
    )

    $process = Start-Process -FilePath $binary -ArgumentList @('run', '--data-dir', $dataDir, '--no-tray') -WindowStyle Hidden -PassThru
    $deadline = (Get-Date).AddSeconds(15)
    do {
        Start-Sleep -Milliseconds 200
        $initialized = $false
        if (Test-Path -LiteralPath $statePath) {
            try { $initialized = [bool](Get-Content -Raw -Encoding UTF8 -LiteralPath $statePath | ConvertFrom-Json).initialized } catch { }
        }
    } while (-not $initialized -and (Get-Date) -lt $deadline)
    if (-not $initialized) { throw 'Watchdog did not initialize its isolated session baseline.' }

    $badThread = '11111111-1111-4111-8111-111111111111'
    $badRollout = Join-Path $sessions ("rollout-2026-07-26T20-00-00-$badThread.jsonl")
    Write-LifecycleEvent -Path $badRollout -Kind 'task_complete' -TurnId 'bad-turn' -ErrorValue 'HTTP 400 invalid request'
    Start-Sleep -Seconds 3
    if ((Get-RendererInvocations).Count -ne 0) { throw 'Non-retryable HTTP 400 event invoked the background channel.' }

    $retryThreads = @(
        [pscustomobject]@{
            Id = '22222222-2222-4222-8222-222222222222'
            Path = Join-Path $sessions 'rollout-2026-07-26T20-00-01-22222222-2222-4222-8222-222222222222.jsonl'
            RetryTurn = 'app-retry-turn-a'
            Cwd = Join-Path $testRoot 'workspace-a'
            Model = 'model-alpha'
            Effort = 'high'
            FailureClass = 'server'
            EmptyResponse = $false
        },
        [pscustomobject]@{
            Id = '33333333-3333-4333-8333-333333333333'
            Path = Join-Path $sessions 'rollout-2026-07-26T20-00-02-33333333-3333-4333-8333-333333333333.jsonl'
            RetryTurn = 'app-retry-turn-b'
            Cwd = Join-Path $testRoot 'workspace-b'
            Model = 'model-beta'
            Effort = 'max'
            FailureClass = 'empty_response'
            EmptyResponse = $true
        }
    )
    foreach ($thread in $retryThreads) {
        New-Item -ItemType Directory -Force -Path $thread.Cwd | Out-Null
        Write-TurnContext -Path $thread.Path -Cwd $thread.Cwd -Model $thread.Model -Effort $thread.Effort
        if ($thread.EmptyResponse) {
            Write-LifecycleEvent -Path $thread.Path -Kind 'task_complete' -TurnId 'failed-turn' -ErrorValue $null -LastAgentMessage $null -IncludeLastAgentMessage -Append
        }
        else {
            Write-LifecycleEvent -Path $thread.Path -Kind 'task_complete' -TurnId 'failed-turn' -ErrorValue 'HTTP 503 Service Unavailable' -Append
        }
    }

    $deadline = (Get-Date).AddSeconds(15)
    do {
        Start-Sleep -Milliseconds 200
        $invocations = Get-RendererInvocations
    } while ($invocations.Count -lt 2 -and (Get-Date) -lt $deadline)
    if ($invocations.Count -lt 2) { throw 'Two retryable failures did not produce two independent background dispatches.' }
    $expressions = ($invocations | ForEach-Object { $_.expression }) -join "`n"
    foreach ($thread in $retryThreads) {
        $matchingInvocations = @($invocations | Where-Object { $_.expression.Contains($thread.Id) })
        if ($matchingInvocations.Count -ne 1) { throw "Background dispatch count for task $($thread.Id) was $($matchingInvocations.Count)." }
        $taskExpression = $matchingInvocations[0].expression
        if (-not $taskExpression.Contains($thread.Model) -or -not $taskExpression.Contains(('"effort":"{0}"' -f $thread.Effort))) {
            throw "Background dispatch mixed settings for task $($thread.Id)."
        }
    }
    if (-not $expressions.Contains('Continue smoke test.')) { throw 'Continuation prompt was not encoded in the same-task request.' }
    if (-not $expressions.Contains('input: []')) { throw 'Normal recovery did not prefer a silent empty-input continuation.' }
    foreach ($setting in @('model_reasoning_effort', ':danger-full-access', 'runtime_workspace_roots')) {
        if (-not $expressions.Contains($setting)) { throw "Background dispatch omitted preserved setting $setting." }
    }
    if ($expressions.Contains('private-smoke-setting-must-not-be-forwarded')) {
        throw 'Background dispatch forwarded private non-setting context.'
    }
    if ($expressions -match '(?i)codex://|window\.open|location\.(?:assign|replace)|\bcodex\s+exec\b') {
        throw 'The watchdog attempted visible navigation or an external Codex task.'
    }

    foreach ($thread in $retryThreads) {
        $deadline = (Get-Date).AddSeconds(5)
        do {
            Start-Sleep -Milliseconds 100
            $threadState = Get-ThreadState -ThreadId $thread.Id
        } while ((-not $threadState -or -not $threadState.awaiting) -and (Get-Date) -lt $deadline)
        if (-not $threadState -or -not $threadState.awaiting) {
            throw "Dispatched retry was not persisted for task $($thread.Id)."
        }
        if ($threadState.awaiting.class -ne $thread.FailureClass) {
            throw "Task $($thread.Id) was classified as $($threadState.awaiting.class), expected $($thread.FailureClass)."
        }
    }

    Write-LifecycleEvent -Path $retryThreads[0].Path -Kind 'task_complete' -TurnId 'unrelated-turn' -ErrorValue $null -Append
    Start-Sleep -Seconds 2
    if (-not (Get-ThreadState -ThreadId $retryThreads[0].Id).awaiting) {
        throw 'An unrelated successful turn incorrectly cleared the retry chain.'
    }

    foreach ($thread in $retryThreads) {
        Write-LifecycleEvent -Path $thread.Path -Kind 'task_started' -TurnId $thread.RetryTurn -ErrorValue $null -Append
        Write-LifecycleEvent -Path $thread.Path -Kind 'task_complete' -TurnId $thread.RetryTurn -ErrorValue $null -Append
    }
    $deadline = (Get-Date).AddSeconds(8)
    do {
        Start-Sleep -Milliseconds 200
        $recovered = $true
        foreach ($thread in $retryThreads) {
            $threadState = Get-ThreadState -ThreadId $thread.Id
            if (-not $threadState -or $threadState.pending -or $threadState.awaiting -or
                [int]$threadState.recovery_attempts -ne 0 -or [int]$threadState.consecutive_retries -ne 0) {
                $recovered = $false
            }
        }
    } while (-not $recovered -and (Get-Date) -lt $deadline)
    if (-not $recovered) { throw 'Both matching retry turns did not recover independently.' }

    New-Item -ItemType File -Force -Path (Join-Path $dataDir 'stop.signal') | Out-Null
    $deadline = (Get-Date).AddSeconds(10)
    do {
        Start-Sleep -Milliseconds 200
        $process.Refresh()
    } while (-not $process.HasExited -and (Get-Date) -lt $deadline)
    if (-not $process.HasExited) { throw 'Watchdog did not stop after stop.signal.' }

    $status = Get-Content -Raw -Encoding UTF8 -LiteralPath $statusPath | ConvertFrom-Json
    if ($status.running) { throw 'Final status still reports running.' }

    $null = Invoke-WebRequest -UseBasicParsing -Method Post -Uri "http://127.0.0.1:$mockPort/shutdown"
    if (-not $mockProcess.WaitForExit(5000)) { throw 'Mock background channel did not stop cleanly.' }
    $mockProcess.Dispose()
    $mockProcess = $null

    [pscustomobject]@{
        Status = 'passed'
        NonRetryableSuppressed = $true
        EmptyResponseDetected = $true
        IndependentTasksDispatched = 2
        IndependentTaskSettingsPreserved = 2
        VisibleNavigationForbidden = $true
        ExternalCliForbidden = $true
        UnrelatedSuccessIgnored = $true
        MatchingTurnsRecovered = 2
        SilentProcessStopped = $true
    }
}
finally {
    if ($process -and -not $process.HasExited) {
        Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
    }
    if ($mockProcess -and -not $mockProcess.HasExited) {
        Stop-Process -Id $mockProcess.Id -Force -ErrorAction SilentlyContinue
    }
    if (-not $KeepArtifacts -and (Test-Path -LiteralPath $testRoot)) {
        $resolvedTestRoot = [System.IO.Path]::GetFullPath($testRoot)
        $resolvedTemp = [System.IO.Path]::GetFullPath($env:TEMP).TrimEnd('\') + '\'
        if (-not $resolvedTestRoot.StartsWith($resolvedTemp, [System.StringComparison]::OrdinalIgnoreCase)) {
            throw "Refusing to remove unexpected smoke-test path: $resolvedTestRoot"
        }
        Remove-Item -LiteralPath $resolvedTestRoot -Recurse -Force
    }
}
