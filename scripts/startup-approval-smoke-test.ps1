[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$approvalScript = Join-Path $PSScriptRoot 'startup-approval.ps1'
if (-not (Test-Path -LiteralPath $approvalScript -PathType Leaf)) { throw 'startup-approval.ps1 is missing.' }
. $approvalScript

$enabled = [byte[]](2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
$disabled = [byte[]](3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
$malformed = [byte[]](3, 1, 0, 0)
if ((Get-CodexAutoRetryStartupApprovalStatus -Bytes $enabled) -ne 'enabled') {
    throw 'StartupApproved enabled marker was not recognized.'
}
if ((Get-CodexAutoRetryStartupApprovalStatus -Bytes $disabled) -ne 'disabled') {
    throw 'StartupApproved disabled marker was not recognized.'
}
if ((Get-CodexAutoRetryStartupApprovalStatus -Bytes $malformed) -ne 'unknown' -or
    (Get-CodexAutoRetryStartupApprovalStatus -Bytes $null) -ne 'unknown') {
    throw 'Malformed or absent StartupApproved data was not reported as unknown.'
}
$enabledCopy = Get-CodexAutoRetryStartupApprovalEnabledBytes -ExistingBytes $disabled
if (-not (Test-CodexAutoRetryStartupApprovalBytes -Left $enabledCopy -Right $enabledCopy) -or
    (Test-CodexAutoRetryStartupApprovalBytes -Left $enabledCopy -Right $disabled)) {
    throw 'StartupApproved byte comparison or enabled-byte construction is incorrect.'
}

[pscustomobject]@{
    Status = 'passed'
    Enabled = $true
    Disabled = $true
    Unknown = $true
    RegistryMutated = $false
}
