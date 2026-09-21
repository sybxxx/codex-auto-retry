[CmdletBinding()]
param()
$ErrorActionPreference = 'Stop'
$release = Join-Path $PSScriptRoot '..\release\windows'
. (Join-Path $release 'common.ps1')
. (Join-Path $release 'close-codex.ps1')

# Use only synthetic process records; do not close or signal real applications.
$script:fixture = @()
$script:queryError = $false
function Get-CimInstance {
    param($ClassName, $OperationTimeoutSec, $ErrorAction)
    if ($OperationTimeoutSec -ne 5) { throw 'Process probe needs a bounded timeout.' }
    if ($script:queryError) { throw 'simulated access denied' }
    return $script:fixture
}
if ((Get-InstallerDesktopState) -ne 'closed') { throw 'Empty process list was not accepted.' }
foreach ($name in @('ChatGPT.exe', 'Codex.exe')) {
    $script:fixture = @([pscustomobject]@{ Name=$name; ExecutablePath=('C:\Program Files\WindowsApps\OpenAI.Codex_test\app\'+$name); CommandLine='desktop' })
    if ((Get-InstallerDesktopState) -ne 'running') { throw 'Desktop was not detected.' }
}
$script:fixture = @([pscustomobject]@{Name='codex.exe';ExecutablePath='C:\CLI\codex.exe';CommandLine='app-server'})
if ((Get-InstallerDesktopState) -ne 'closed') { throw 'CLI alone was mistaken for Desktop.' }
$script:queryError = $true
if ((Get-InstallerDesktopState) -ne 'unknown') { throw 'Query failure was treated as a closed Desktop.' }

function Start-Process { throw 'No application may be started by the close gate.' }
function Stop-Process { throw 'No application may be terminated by the close gate.' }
function Get-InstallerDesktopState {
    $script:probes++
    $index = [Math]::Min($script:probes - 1, $script:states.Count - 1)
    return $script:states[$index]
}
function Show-CodexCloseNotice {
    param($State, $TimeoutSeconds)
    if ($TimeoutSeconds -lt 1 -or $TimeoutSeconds -gt 300) { throw 'Invalid dialog timeout.' }
    $script:prompts++
    $script:lastState = $State
    return $script:choice
}
foreach ($test in @(
    @{name='already-closed';states=@('closed');choice=4;want=$true;prompts=0},
    @{name='retry-after-close';states=@('running','closed');choice=4;want=$true;prompts=1},
    @{name='cancel';states=@('running');choice=2;want=$false;prompts=1},
    @{name='timeout';states=@('running');choice=-1;want=$false;prompts=1},
    @{name='unknown';states=@('unknown');choice=2;want=$false;prompts=1},
    @{name='query-recovers';states=@('unknown','closed');choice=4;want=$true;prompts=1},
    @{name='still-open';states=@('running');choice=4;want=$false;prompts=3}
)) {
    $script:states=$test.states; $script:choice=$test.choice; $script:prompts=0; $script:probes=0
    if ((Wait-CodexInstallerExit -MaxPrompts 3) -ne $test.want -or $script:prompts -ne $test.prompts) { throw ('Close gate failed: '+$test.name) }
}

$tokens=$null; $errors=$null
$ast=[System.Management.Automation.Language.Parser]::ParseFile((Join-Path $release 'deploy.ps1'),[ref]$tokens,[ref]$errors)
if ($errors.Count) { throw 'Deploy syntax error.' }
$gate=$ast.EndBlock.Statements | Where-Object { $_.Extent.Text -like 'if ($WaitForCodexExit*' } | Select-Object -First 1
$writes=$ast.EndBlock.Statements | Where-Object { $_.Extent.Text -like 'New-Item *$runtimePath*' } | Select-Object -First 1
if (-not $gate -or -not $writes -or $gate.Extent.StartOffset -gt $writes.Extent.StartOffset) { throw 'Close gate runs after runtime writes.' }
$ps=Join-Path $env:WINDIR 'System32\WindowsPowerShell\v1.0\powershell.exe'
foreach ($scenario in @('cancel','continue','automation')) {
    $enabled=if($scenario -eq 'automation'){'$false'}else{'$true'}
    $accepted=if($scenario -eq 'continue'){'$true'}else{'$false'}
    $source = '$WaitForCodexExit='+$enabled+"`nfunction Wait-CodexInstallerExit { return "+$accepted+" }`nfunction Write-Step { param([string]"+'$Message'+") [Console]::WriteLine("+'$Message'+") }`n"+$gate.Extent.Text+"`n"+'[Console]::WriteLine("continued")'
    $encoded=[Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($source))
    $r=Invoke-CodexCli -Path $ps -Arguments @('-NoProfile','-NonInteractive','-EncodedCommand',$encoded) -TimeoutMilliseconds 10000
    if ($scenario -eq 'cancel') {
        if ($r.ExitCode -ne 2 -or $r.Output -match 'continued' -or $r.ErrorOutput) { throw 'Cancellation produced an error or continued installing.' }
    } elseif ($r.ExitCode -ne 0 -or $r.Output -notmatch 'continued') { throw 'Accepted gate did not continue.' }
}
$launcher=Get-ChildItem -LiteralPath $release -Filter '*.cmd' | Where-Object { (Get-Content $_.FullName -Raw) -match 'deploy.ps1' } | Select-Object -First 1
$batch=Get-Content $launcher.FullName -Raw
if ($batch -notmatch '-WaitForCodexExit' -or $batch -notmatch '"%EXIT_CODE%"=="2"') { throw 'One-click cancellation or wait flag is missing.' }
[pscustomobject]@{Status='passed';ProcessProbe='running/closed/unknown';Scenarios=7;Retry='rechecks';CancelExitCode=2;Timeout='bounded';WritesBeforeConfirmation=$false;LiveCodex='untouched'}
