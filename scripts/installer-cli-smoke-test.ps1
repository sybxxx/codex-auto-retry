[CmdletBinding()]
param()
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot '..\release\windows\common.ps1')
$ps = Join-Path $env:WINDIR 'System32\WindowsPowerShell\v1.0\powershell.exe'
function Invoke-Fixture {
    param([string]$Source, [int]$Timeout = 10000)
    $encoded = [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($Source))
    Invoke-CodexCli -Path $ps -Arguments @('-NoLogo', '-NoProfile', '-NonInteractive', '-EncodedCommand', $encoded) -TimeoutMilliseconds $Timeout
}
$warning = Invoke-Fixture '[Console]::Error.WriteLine("synthetic warning"); [Console]::Out.Write("{""installed"":[]}"); exit 0'
if ($warning.ExitCode -ne 0 -or $warning.ErrorOutput -notmatch 'synthetic warning' -or
    @((ConvertFrom-Json $warning.Output).installed).Count -ne 0) { throw 'stderr warning corrupted status or JSON.' }
$failed = Invoke-Fixture '[Console]::Error.WriteLine("network unavailable SECRET_TEST_TOKEN"); exit 23'
if ($failed.ExitCode -ne 23 -or (Get-ReleaseCommandFailure $failed) -ne 'connection_failure') { throw 'Real exit status was lost.' }
$timeout = Invoke-Fixture 'Start-Sleep -Seconds 30' -Timeout 500
if ($timeout.ExitCode -ne -1 -or $timeout.Failure -ne 'timeout') { throw 'Timeout was not bounded.' }
$missing = Invoke-CodexCli -Path (Join-Path $env:TEMP ('missing-' + [guid]::NewGuid().ToString('N') + '.exe')) -Arguments @('--help')
if ($missing.ExitCode -eq 0 -or $missing.Failure -ne 'process_io') { throw 'Missing executable was reported as success.' }
$large = Invoke-Fixture '[Console]::Error.Write(("w" * 70000)); [Console]::Out.Write(("x" * 5000000)); exit 0'
if ($large.Failure -ne 'output_limit' -or $large.Output.Length -ne 4194304 -or $large.ErrorOutput.Length -ne 32768) { throw 'Pipe capture was not bounded.' }
$quoted = Invoke-CodexCli -Path $ps -Arguments @('-NoProfile', '-Command', '[Console]::Write("quote:"";space;trail:\")')
if ($quoted.ExitCode -ne 0 -or $quoted.Output -ne 'quote:";space;trail:\') { throw 'Argument quoting was not preserved.' }

$tokens = $null; $errors = $null
$ast = [System.Management.Automation.Language.Parser]::ParseFile((Join-Path $PSScriptRoot '..\release\windows\deploy.ps1'), [ref]$tokens, [ref]$errors)
if ($errors.Count) { throw 'Deploy parser failed.' }
foreach ($name in @('Get-VerifiedPluginList', 'Verify-Installation')) {
    $fn = $ast.Find({param($n) $n -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $n.Name -eq $name}.GetNewClosure(), $true)
    . ([scriptblock]::Create($fn.Extent.Text))
}
$script:calls = 0
$script:scenario = 'warning'
function Invoke-CodexCli {
    param($Path, $Arguments, $TimeoutMilliseconds)
    $script:calls++
    if ($Arguments -join ' ' -ne 'plugin list --marketplace personal --json') { throw 'Listing was not scoped to personal marketplace.' }
    if ($script:scenario -eq 'transient' -and $script:calls -eq 1) { return [pscustomobject]@{ExitCode=2;Output='';ErrorOutput='network unavailable SECRET_TEST_TOKEN';Failure=''} }
    if ($script:scenario -eq 'failure') { return [pscustomobject]@{ExitCode=23;Output='';ErrorOutput='SECRET_TEST_TOKEN';Failure=''} }
    if ($script:scenario -eq 'invalid') { return [pscustomobject]@{ExitCode=0;Output='invalid SECRET_TEST_TOKEN';ErrorOutput='';Failure=''} }
    return [pscustomobject]@{ExitCode=0;Output='{"installed":[]}';ErrorOutput='synthetic warning';Failure=''}
}
$null = Get-VerifiedPluginList -Cli 'fake.exe' -PluginId 'codex-auto-retry@personal'
if ($script:calls -ne 1) { throw 'Successful command was retried.' }
$script:scenario = 'transient'; $script:calls = 0
$null = Get-VerifiedPluginList -Cli 'fake.exe' -PluginId 'codex-auto-retry@personal'
if ($script:calls -ne 2) { throw 'Transient verification was not retried once.' }
foreach ($scenario in @('failure', 'invalid')) {
    $script:scenario = $scenario; $script:calls = 0; $message = ''
    try { $null = Get-VerifiedPluginList -Cli 'fake.exe' -PluginId 'codex-auto-retry@personal' } catch { $message = $_.Exception.Message }
    if (-not $message -or $message -match 'SECRET_TEST_TOKEN' -or $script:calls -ne 1) { throw 'Failure diagnostic leaked output or retried a permanent failure.' }
}
$script:scenario = 'failure'
$preflight = $ast.EndBlock.Statements | Where-Object { $_ -is [System.Management.Automation.Language.TryStatementAst] -and $_.Extent.Text -match 'Checking plugin listing support' } | Select-Object -First 1
$transaction = $ast.EndBlock.Statements | Where-Object { $_.Extent.Text -like '$transactionRoot = Join-Path*' } | Select-Object -First 1
if (-not $preflight -or $preflight.Extent.StartOffset -gt $transaction.Extent.StartOffset) { throw 'CLI preflight runs after mutation.' }
$SkipRuntimeInstall = $true; $SkipPluginRegistration = $false
$cli = 'fixture.exe'; $pluginId = 'codex-auto-retry@personal'; $marketplacePath = $PSCommandPath
function Write-Step { param($Message) }
$script:disposed = $false
$upgradeLock = [pscustomobject]@{}
$upgradeLock | Add-Member ScriptMethod Dispose { $script:disposed = $true }
$message = ''
try { . ([scriptblock]::Create($preflight.Extent.Text)) } catch { $message = $_.Exception.Message }
if (-not $message -or -not $script:disposed) { throw 'Failed preflight did not release the upgrade lock.' }
[pscustomobject]@{Status='passed'; WarningWithExitZero='accepted'; StdoutJson='isolated'; RealExitCode=23; Timeout='bounded'; Output='bounded'; Diagnostics='privacy-safe'; ReadRetry='bounded'; Preflight='before-mutation'}
