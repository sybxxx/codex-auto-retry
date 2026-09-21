[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$BinaryPath,
    [string]$ScreenshotDirectory = ''
)

$ErrorActionPreference = 'Stop'
$testRoot = Join-Path ([IO.Path]::GetTempPath()) ('codex-auto-retry-auth-ui-' + [guid]::NewGuid().ToString('N'))
$binaryFull = (Resolve-Path -LiteralPath $BinaryPath).Path
$form = $null
$timer = $null
try {
    New-Item -ItemType Directory -Path $testRoot | Out-Null
    $fixture = @{
        config_version = 4; retry_prompt = 'Continue'; max_recovery_attempts = 1000
        max_consecutive_retries = 100; auth_max_attempts = 6; memory_limit_mb = 1024
        initial_delay_seconds = 5; max_delay_seconds = 300; delay_increment_seconds = 2
        delay_strategy = 'fixed'; show_notifications = $false; shared_app_server_enabled = $false
        shared_app_server_requested = $false; shared_app_server_port = 49622
        include_default_home = $false; include_cockpit_homes = $false; session_roots = @()
    }
    [IO.File]::WriteAllText((Join-Path $testRoot 'config.json'), ($fixture | ConvertTo-Json), [Text.UTF8Encoding]::new($false))
    [IO.File]::WriteAllText((Join-Path $testRoot 'control.json'), '{"paused":false}', [Text.UTF8Encoding]::new($false))
    $fixtureState = @{ threads = @{ '019fa94e-0103-7183-b405-36bd307b6dca' = @{ stopped = @{
        class = 'auth_limited'; attempts = 19; consecutive_retries = 19
        max_attempts = 6; max_consecutive_retries = 6; reason = 'auth_attempt_limit'
        failed_at = [DateTimeOffset]::UtcNow.ToString('o'); stopped_at = [DateTimeOffset]::UtcNow.ToString('o')
    } } } }
    [IO.File]::WriteAllText((Join-Path $testRoot 'state.json'), ($fixtureState | ConvertTo-Json -Depth 6), [Text.UTF8Encoding]::new($false))

    # Load the actual form and event handlers, but keep its message loop owned
    # by this bounded test. Only save-settings is invoked, using the temp data dir.
    $source = [IO.File]::ReadAllText((Join-Path $PSScriptRoot 'source\ui\settings.ps1'))
    $loop = '[void]$form.ShowDialog()'
    if (-not $source.Contains($loop)) { throw 'Settings form entry point changed.' }
    . ([scriptblock]::Create($source.Replace($loop, ''))) -DataDir $testRoot -Executable $binaryFull -SmokeTest
    $form.Show()
    [System.Windows.Forms.Application]::DoEvents()
    $timer.Stop()
    if ($authBox.Value -ne 6) { throw 'Auth limit was not loaded from the fixture.' }
    foreach ($language in @('zh', 'en')) {
        $script:currentLanguage = $language
        Apply-Language
        Assert-SettingsLayout
        if ($authBox.Right -gt $settingsGroup.ClientSize.Width -or $authBox.Bottom -gt $settingsGroup.ClientSize.Height) {
            throw 'Auth limit control extends outside settings.'
        }
        $labelSize = [System.Windows.Forms.TextRenderer]::MeasureText($authLabel.Text, $authLabel.Font)
        if ($labelSize.Width -gt $authLabel.Width) { throw 'Auth limit label is clipped.' }
        $row = $taskList.Items[0]
        $expectedReason = if ($language -eq 'en') { 'Auth Limit' } else { -join ([char[]](0x767b,0x5f55,0x5f02,0x5e38,0x4e13,0x7528,0x4e0a,0x9650)) }
        if ($row.SubItems[3].Text -ne '19/6' -or $row.SubItems[4].Text -ne '19/6' -or
            $row.SubItems[1].Text -ne $expectedReason) {
            throw 'The form lost the historical counters or auth-specific reason.'
        }
        if ($ScreenshotDirectory) {
            New-Item -ItemType Directory -Path $ScreenshotDirectory -Force | Out-Null
            $bitmap = [System.Drawing.Bitmap]::new($form.Width, $form.Height)
            try {
                $form.DrawToBitmap($bitmap, [System.Drawing.Rectangle]::new(0, 0, $form.Width, $form.Height))
                $bitmap.Save((Join-Path $ScreenshotDirectory ("auth-settings-$language.png")))
            } finally { $bitmap.Dispose() }
        }
    }
    $authBox.Value = 40
    $saveButton.PerformClick()
    $saved = Get-Content -LiteralPath (Join-Path $testRoot 'config.json') -Raw | ConvertFrom-Json
    if ($saved.auth_max_attempts -ne 40 -or $saved.max_recovery_attempts -ne 1000 -or
        $saved.max_consecutive_retries -ne 100 -or $saved.shared_app_server_enabled) {
        throw 'Native save did not persist the auth limit independently.'
    }
    [pscustomobject]@{ Status = 'passed'; Languages = 'zh,en'; Counters = '19/6'; SavedAuthLimit = 40; LiveRuntimeChanged = $false }
}
finally {
    if ($timer) { $timer.Stop() }
    if ($form) { $form.Close(); $form.Dispose() }
    if ($timer) { $timer.Dispose() }
    $tempPrefix = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\') + '\'
    $resolvedRoot = [IO.Path]::GetFullPath($testRoot)
    if (-not $resolvedRoot.StartsWith($tempPrefix, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'Refusing to clean a path outside the temporary directory.'
    }
    if (Test-Path -LiteralPath $resolvedRoot) { Remove-Item -LiteralPath $resolvedRoot -Recurse -Force }
}
