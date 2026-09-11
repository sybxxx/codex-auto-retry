param(
    [Parameter(Mandatory = $true)][string]$DataDir,
    [Parameter(Mandatory = $true)][string]$Executable,
    [switch]$SmokeTest
)

$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
[System.Windows.Forms.Application]::EnableVisualStyles()

# A shared-backend health check can involve starting Codex and waiting for a
# WebSocket handshake. Keep the settings window responsive while it runs, and
# never allow a broken child process to hold the window open indefinitely.
$localCommandTimeoutMilliseconds = 35000
$localCommandExitPortReserved = 2
$localCommandExitPortConflict = 3
$script:localCommandProcess = $null
$script:localCommandTimedOut = $false
$script:localCommandInProgress = $false
$script:memoryGuardTriggered = $false

$configPath = Join-Path $DataDir 'config.json'
$controlPath = Join-Path $DataDir 'control.json'
$statusPath = Join-Path $DataDir 'status.json'
$statePath = Join-Path $DataDir 'state.json'
$smokeClosePath = Join-Path $DataDir 'settings-smoke-close.signal'
$uiLangPath = Join-Path $DataDir 'ui-language.json'

function Get-SharedModeRequested($Config) {
    if (-not $Config) { return $false }
    if ($Config.PSObject.Properties['shared_app_server_requested']) { return [bool]$Config.shared_app_server_requested }
    return [bool]$Config.shared_app_server_enabled
}

function Read-JsonFile {
    param([string]$Path)
    if (-not (Test-Path -LiteralPath $Path)) { return $null }
    $stream = $null
    $reader = $null
    try {
        $share = [System.IO.FileShare]([System.IO.FileShare]::ReadWrite -bor [System.IO.FileShare]::Delete)
        $stream = [System.IO.FileStream]::new(
            $Path,
            [System.IO.FileMode]::Open,
            [System.IO.FileAccess]::Read,
            $share
        )
        $reader = [System.IO.StreamReader]::new(
            $stream,
            [System.Text.UTF8Encoding]::new($false),
            $true,
            1024,
            $true
        )
        return $reader.ReadToEnd() | ConvertFrom-Json
    } catch {
        return $null
    } finally {
        if ($reader) { $reader.Dispose() }
        if ($stream) { $stream.Dispose() }
    }
}

$script:currentLanguage = 'zh'
if (Test-Path -LiteralPath $uiLangPath) {
    try {
        $savedLangRecord = Read-JsonFile $uiLangPath
        if ($savedLangRecord -and $savedLangRecord.language -eq 'en') {
            $script:currentLanguage = 'en'
        }
    } catch { }
}

$script:i18n = @{
    'form_title'              = @{ zh = 'Codex Auto Retry 设置'; en = 'Codex Auto Retry Settings' }
    'lang_button'             = @{ zh = 'English'; en = '中文' }
    'status_group'            = @{ zh = '当前状态'; en = 'Current Status' }
    'status_loading'          = @{ zh = '正在读取…'; en = 'Loading...' }
    'status_not_running'      = @{ zh = '后台服务未运行'; en = 'Service Not Running' }
    'status_disconnected'     = @{ zh = '未接入共享后台'; en = 'Shared Backend Offline' }
    'status_exited'           = @{ zh = 'Codex 已退出，重试已停止'; en = 'Codex Exited (Stopped)' }
    'status_shared_temp_unavail' = @{ zh = '共享后台暂不可用'; en = 'Shared Backend Unavailable' }
    'status_shared_disabled'  = @{ zh = '共享后台已关闭'; en = 'Shared Backend Disabled' }
    'status_port_reserved'    = @{ zh = '共享端口被 Windows 保留，重试未执行'; en = 'Port Reserved by Windows' }
    'status_port_conflict'    = @{ zh = '首选共享端口不可用，启用时将自动选择安全端口'; en = 'Port Conflict (Auto-Selecting)' }
    'status_migration_deferred' = @{ zh = '等待 Codex 关闭后完成共享后台迁移'; en = 'Waiting for Codex to Exit' }
    'status_config_invalid'   = @{ zh = '共享后台配置不兼容，已切回官方后台'; en = 'Shared Config Reverted' }
    'status_paused'           = @{ zh = '已暂停'; en = 'Paused' }
    'status_running'          = @{ zh = '运行中'; en = 'Running' }
    'queue_summary'           = @{ zh = '队列：{0} 等待 / {1} 执行 / {2} 停止'; en = 'Queue: {0} wait / {1} run / {2} stop' }
    'next_waiting_service'    = @{ zh = '下次重试：等待服务启动'; en = 'Next Retry: Waiting for Service' }
    'next_waiting_resume'     = @{ zh = '下次重试：等待恢复'; en = 'Next Retry: Waiting for Resume' }
    'next_seconds'            = @{ zh = '下次重试：{0} 秒'; en = 'Next Retry: {0}s' }
    'next_running'            = @{ zh = '下次重试：正在执行'; en = 'Next Retry: Running' }
    'next_none'               = @{ zh = '下次重试：--'; en = 'Next Retry: --' }
    'last_scan'               = @{ zh = '最近扫描：'; en = 'Last Scan: ' }
    'queue_group'             = @{ zh = '任务队列（仅显示任务编号，不读取对话内容）'; en = 'Task Queue (Task IDs only, conversation content not read)' }
    'col_task'                = @{ zh = '任务'; en = 'Task' }
    'col_status'              = @{ zh = '状态'; en = 'Status' }
    'col_countdown'           = @{ zh = '倒计时'; en = 'Countdown' }
    'col_recovery'            = @{ zh = '本次恢复'; en = 'Recoveries' }
    'col_consecutive'         = @{ zh = '连续重试'; en = 'Repeats' }
    'col_class'               = @{ zh = '故障类型'; en = 'Type' }
    'btn_retry_now'           = @{ zh = '立即重试'; en = 'Retry Now' }
    'btn_cancel_retry'        = @{ zh = '取消等待'; en = 'Cancel Wait' }
    'btn_restart_retry'       = @{ zh = '重新开始'; en = 'Restart' }
    'settings_group'          = @{ zh = '自动重试设置'; en = 'Auto Retry Settings' }
    'check_enabled'           = @{ zh = '启用自动重试'; en = 'Enable Auto Retry' }
    'check_shared'            = @{ zh = '启用共享 Codex 后台（健康检查）'; en = 'Enable Shared Codex Backend' }
    'shared_port_prefix'      = @{ zh = '当前共享端口：'; en = 'Shared Port: ' }
    'check_notifications'     = @{ zh = '达到重试上限时显示插件通知'; en = 'Show Alert on Retry Limit' }
    'label_prompt'            = @{ zh = '后备重试文字'; en = 'Fallback Retry Prompt' }
    'label_recovery'          = @{ zh = '本次故障恢复上限'; en = 'Outage Recovery Limit' }
    'label_consecutive'       = @{ zh = '连续无进展重试上限'; en = 'No-Progress Limit' }
    'label_memory'            = @{ zh = '内存保护上限（MB）'; en = 'Memory Limit (MB)' }
    'label_strategy'          = @{ zh = '等待策略'; en = 'Wait Strategy' }
    'strategy_exponential'    = @{ zh = '翻倍递增'; en = 'Exponential' }
    'strategy_linear'         = @{ zh = '等差递增'; en = 'Linear' }
    'strategy_fixed'          = @{ zh = '固定间隔'; en = 'Fixed Interval' }
    'label_initial_delay'     = @{ zh = '首次等待（秒）'; en = 'Initial Wait (s)' }
    'label_fixed_interval'    = @{ zh = '固定间隔（秒）'; en = 'Fixed Interval (s)' }
    'label_max_delay'         = @{ zh = '最大等待（秒）'; en = 'Max Wait (s)' }
    'label_increment'         = @{ zh = '每次增加（秒）'; en = 'Increment (s)' }
    'wait_seq_prefix'         = @{ zh = '等待序列：'; en = 'Wait Sequence: ' }
    'unit_second'             = @{ zh = ' 秒'; en = 's' }
    'unit_minute'             = @{ zh = ' 分钟'; en = 'm' }
    'unit_hour'               = @{ zh = ' 小时'; en = 'h' }
    'btn_save'                = @{ zh = '保存设置'; en = 'Save Settings' }
    'btn_close'               = @{ zh = '关闭'; en = 'Close' }
    'busy_checking'           = @{ zh = '检查中…'; en = 'Checking...' }
    'busy_notice'             = @{ zh = '正在执行设置检查，请稍候…'; en = 'Checking settings, please wait...' }
    'save_saved'              = @{ zh = '设置已保存，将在下一次扫描时生效。'; en = 'Settings saved; will take effect on next scan.' }
    'save_checking_health'    = @{ zh = '正在执行共享后台健康检查，Codex 仍保持原后台…'; en = 'Running shared backend health check; Codex remains on official backend...' }
    'save_closing_shared'     = @{ zh = '正在关闭共享后台并恢复官方后台…'; en = 'Disabling shared backend and reverting to official backend...' }
    'save_saving'             = @{ zh = '正在保存设置…'; en = 'Saving settings...' }
    'save_timeout'            = @{ zh = '设置检查超时，Codex 后台未切换，设置未保存。'; en = 'Settings check timed out; Codex backend not changed, settings not saved.' }
    'save_validation_failed'  = @{ zh = '设置校验失败'; en = 'Settings validation failed' }
    'save_fail_reserved'      = @{ zh = '保存失败：端口 {0} 被 Windows 保留，共享后台未启用。'; en = 'Save failed: Port {0} is reserved by Windows; shared backend not enabled.' }
    'save_fail_conflict'      = @{ zh = '保存失败：端口 {0} 被其他程序占用，共享后台未启用。'; en = 'Save failed: Port {0} is in use; shared backend not enabled.' }
    'save_fail_timeout'       = @{ zh = '保存超时：共享后台健康检查未完成，Codex 仍使用原后台。'; en = 'Save timed out: Health check did not finish; Codex remains on official backend.' }
    'save_fail_health'        = @{ zh = '保存失败：共享后台健康检查未通过，Codex 仍使用原后台。'; en = 'Save failed: Health check did not pass; Codex remains on official backend.' }
    'save_fail_close'         = @{ zh = '保存失败：共享后台未能关闭，设置未完成。'; en = 'Save failed: Shared backend could not be disabled.' }
    'save_fail_range'         = @{ zh = '保存失败，请检查设置范围。'; en = 'Save failed: please verify settings range.' }
    'msg_prompt_empty'        = @{ zh = '后备重试文字不能为空。'; en = 'Fallback retry prompt cannot be empty.' }
    'msg_max_less_initial'    = @{ zh = '最大等待时间不能小于首次等待时间。'; en = 'Maximum wait time cannot be less than initial wait time.' }
    'msg_action_failed'       = @{ zh = '操作没有生效，任务状态可能已经改变。'; en = 'Action did not take effect; task state may have changed.' }
    'memory_guard_msg'        = @{ zh = '设置窗口私有内存已达到 {0} MB，超过上限 {1} MB。窗口将关闭，Codex 任务数据未被删除。'; en = 'Settings window memory reached {0} MB, exceeding limit of {1} MB. Window will close; Codex task data is preserved.' }
    'memory_guard_title'      = @{ zh = 'Codex Auto Retry 内存保护'; en = 'Codex Auto Retry Memory Guard' }
    'layout_overlap_err'      = @{ zh = '设置布局发生遮挡：'; en = 'Layout overlap detected: ' }
    'layout_bounds_err'       = @{ zh = '设置输入框超出可见区域。'; en = 'Control bounds exceed visible area.' }
    'config_read_err'         = @{ zh = '无法读取自动重试设置。'; en = 'Cannot read auto-retry settings.' }
}

function T($Key) {
    $item = $script:i18n[$Key]
    if ($item) {
        $val = $item[$script:currentLanguage]
        if ($val) { return $val }
    }
    return $Key
}

function Stop-LocalCommandProcess {
    param($Process)
    if ($null -eq $Process) { return }
    try {
        if ($Process.HasExited) { return }
    } catch { return }
    try {
        $killer = Start-Process -FilePath 'taskkill.exe' -ArgumentList @('/PID', [string]$Process.Id, '/T', '/F') -WindowStyle Hidden -Wait -PassThru
        if ($killer) { $killer.Dispose() }
    } catch { }
    try {
        if (-not $Process.HasExited) { $Process.Kill() }
    } catch { }
}

function Start-LocalCommand {
    param(
        [string]$Mode,
        [hashtable]$Environment,
        [int]$TimeoutMilliseconds = $localCommandTimeoutMilliseconds
    )
    $script:localCommandTimedOut = $false
    $info = [System.Diagnostics.ProcessStartInfo]::new()
    $info.FileName = $Executable
    $info.Arguments = $Mode
    $info.UseShellExecute = $false
    $info.CreateNoWindow = $true
    $info.EnvironmentVariables['CODEX_AUTO_RETRY_DATA_DIR'] = $DataDir
    foreach ($entry in $Environment.GetEnumerator()) {
        $info.EnvironmentVariables[[string]$entry.Key] = [string]$entry.Value
    }
    $process = $null
    try {
        $process = [System.Diagnostics.Process]::Start($info)
        $script:localCommandProcess = $process
        $deadline = [DateTimeOffset]::UtcNow.AddMilliseconds($TimeoutMilliseconds)
        while (-not $process.HasExited) {
            [System.Windows.Forms.Application]::DoEvents()
            Start-Sleep -Milliseconds 50
            if ([DateTimeOffset]::UtcNow -ge $deadline) {
                $script:localCommandTimedOut = $true
                Stop-LocalCommandProcess $process
                return -2
            }
        }
        return $process.ExitCode
    } catch {
        return -1
    } finally {
        $script:localCommandProcess = $null
        if ($process) { $process.Dispose() }
    }
}

function New-Label {
    param([string]$Text, [int]$X, [int]$Y, [int]$Width, [int]$Height)
    $label = [System.Windows.Forms.Label]::new()
    $label.Text = $Text
    $label.Location = [System.Drawing.Point]::new($X, $Y)
    $label.Size = [System.Drawing.Size]::new($Width, $Height)
    return $label
}

function New-NumberBox {
    param([int]$X, [int]$Y, [int]$Minimum, [int]$Maximum, [int]$Value, [int]$Width = 100)
    $box = [System.Windows.Forms.NumericUpDown]::new()
    $box.Location = [System.Drawing.Point]::new($X, $Y)
    $box.Size = [System.Drawing.Size]::new($Width, 24)
    $box.Minimum = $Minimum
    $box.Maximum = $Maximum
    $box.Value = [Math]::Min($Maximum, [Math]::Max($Minimum, $Value))
    return $box
}

$config = Read-JsonFile $configPath
$control = Read-JsonFile $controlPath
$runtimeStatus = Read-JsonFile $statusPath
if (-not $config) {
    [System.Windows.Forms.MessageBox]::Show((T 'config_read_err'), 'Codex Auto Retry', 'OK', 'Error') | Out-Null
    exit 1
}

$form = [System.Windows.Forms.Form]::new()
$form.Text = T 'form_title'
$form.StartPosition = 'CenterScreen'
$form.FormBorderStyle = 'FixedDialog'
$form.MaximizeBox = $false
$form.MinimizeBox = $false
$form.ClientSize = [System.Drawing.Size]::new(620, 840)
$form.Font = [System.Drawing.Font]::new('Microsoft YaHei UI', 9)
$form.Icon = [System.Drawing.SystemIcons]::Application
if ($SmokeTest) {
    $form.Opacity = 0
    $form.ShowInTaskbar = $false
}

$title = New-Label 'Codex Auto Retry' 22 18 280 30
$title.Font = [System.Drawing.Font]::new('Microsoft YaHei UI', 15, [System.Drawing.FontStyle]::Bold)
$form.Controls.Add($title)

$versionText = ''
if ($runtimeStatus -and $runtimeStatus.version) { $versionText = 'v' + [string]$runtimeStatus.version }
$versionLabel = New-Label $versionText 415 22 80 22
$versionLabel.TextAlign = 'MiddleRight'
$versionLabel.ForeColor = [System.Drawing.Color]::DimGray
$form.Controls.Add($versionLabel)

$langButton = [System.Windows.Forms.Button]::new()
$langButton.Location = [System.Drawing.Point]::new(505, 18)
$langButton.Size = [System.Drawing.Size]::new(95, 26)
$langButton.Text = T 'lang_button'
$langButton.Font = [System.Drawing.Font]::new('Microsoft YaHei UI', 8.5)
$langButton.FlatStyle = 'Standard'
$langButton.Cursor = [System.Windows.Forms.Cursors]::Hand
$form.Controls.Add($langButton)

$statusGroup = [System.Windows.Forms.GroupBox]::new()
$statusGroup.Text = T 'status_group'
$statusGroup.Location = [System.Drawing.Point]::new(20, 58)
$statusGroup.Size = [System.Drawing.Size]::new(580, 105)
$form.Controls.Add($statusGroup)
$serviceValue = New-Label (T 'status_loading') 18 25 180 24
$serviceValue.Font = [System.Drawing.Font]::new('Microsoft YaHei UI', 10, [System.Drawing.FontStyle]::Bold)
$queueValue = New-Label ([string]::Format((T 'queue_summary'), '--', '--', '--')) 204 25 195 24
$nextValue = New-Label (T 'next_none') 404 25 165 24
$scanValue = New-Label '' 18 62 535 22
$scanValue.ForeColor = [System.Drawing.Color]::DimGray
$statusGroup.Controls.AddRange(@($serviceValue, $queueValue, $nextValue, $scanValue))

$queueGroup = [System.Windows.Forms.GroupBox]::new()
$queueGroup.Text = T 'queue_group'
$queueGroup.Location = [System.Drawing.Point]::new(20, 175)
$queueGroup.Size = [System.Drawing.Size]::new(580, 190)
$form.Controls.Add($queueGroup)
$taskList = [System.Windows.Forms.ListView]::new()
$taskList.Location = [System.Drawing.Point]::new(14, 25)
$taskList.Size = [System.Drawing.Size]::new(550, 120)
$taskList.View = 'Details'
$taskList.FullRowSelect = $true
$taskList.GridLines = $true
$taskList.HideSelection = $false
[void]$taskList.Columns.Add((T 'col_task'), 65)
[void]$taskList.Columns.Add((T 'col_status'), 110)
[void]$taskList.Columns.Add((T 'col_countdown'), 90)
[void]$taskList.Columns.Add((T 'col_recovery'), 85)
[void]$taskList.Columns.Add((T 'col_consecutive'), 85)
[void]$taskList.Columns.Add((T 'col_class'), 85)
$queueGroup.Controls.Add($taskList)
$retryNowButton = [System.Windows.Forms.Button]::new()
$retryNowButton.Text = T 'btn_retry_now'
$retryNowButton.Location = [System.Drawing.Point]::new(284, 153)
$retryNowButton.Size = [System.Drawing.Size]::new(86, 27)
$cancelRetryButton = [System.Windows.Forms.Button]::new()
$cancelRetryButton.Text = T 'btn_cancel_retry'
$cancelRetryButton.Location = [System.Drawing.Point]::new(378, 153)
$cancelRetryButton.Size = [System.Drawing.Size]::new(86, 27)
$restartRetryButton = [System.Windows.Forms.Button]::new()
$restartRetryButton.Text = T 'btn_restart_retry'
$restartRetryButton.Location = [System.Drawing.Point]::new(472, 153)
$restartRetryButton.Size = [System.Drawing.Size]::new(86, 27)
$queueGroup.Controls.AddRange(@($retryNowButton, $cancelRetryButton, $restartRetryButton))

$settingsGroup = [System.Windows.Forms.GroupBox]::new()
$settingsGroup.Text = T 'settings_group'
$settingsGroup.Location = [System.Drawing.Point]::new(20, 378)
$settingsGroup.Size = [System.Drawing.Size]::new(580, 390)
$form.Controls.Add($settingsGroup)

$enabledCheck = [System.Windows.Forms.CheckBox]::new()
$enabledCheck.Text = T 'check_enabled'
$enabledCheck.Location = [System.Drawing.Point]::new(18, 25)
$enabledCheck.Size = [System.Drawing.Size]::new(160, 24)
$enabledCheck.Checked = -not [bool]$control.paused
$sharedCheck = [System.Windows.Forms.CheckBox]::new()
$sharedCheck.Text = T 'check_shared'
$sharedCheck.Location = [System.Drawing.Point]::new(18, 52)
$sharedCheck.Size = [System.Drawing.Size]::new(260, 24)
$sharedCheck.Checked = Get-SharedModeRequested $config
$sharedPortValue = New-Label ((T 'shared_port_prefix') + [int]$config.shared_app_server_port) 300 25 255 24
$sharedPortValue.ForeColor = [System.Drawing.Color]::DimGray
$notificationsCheck = [System.Windows.Forms.CheckBox]::new()
$notificationsCheck.Text = T 'check_notifications'
$notificationsCheck.Location = [System.Drawing.Point]::new(300, 52)
$notificationsCheck.Size = [System.Drawing.Size]::new(255, 24)
$notificationsCheck.Checked = [bool]$config.show_notifications
$settingsGroup.Controls.AddRange(@($enabledCheck, $sharedCheck, $sharedPortValue, $notificationsCheck))

$promptLabel = New-Label (T 'label_prompt') 18 85 180 22
$settingsGroup.Controls.Add($promptLabel)
$promptBox = [System.Windows.Forms.TextBox]::new()
$promptBox.Location = [System.Drawing.Point]::new(18, 108)
$promptBox.Size = [System.Drawing.Size]::new(540, 54)
$promptBox.Multiline = $true
$promptBox.MaxLength = 500
$promptBox.ScrollBars = 'Vertical'
$promptBox.Text = [string]$config.retry_prompt
$settingsGroup.Controls.Add($promptBox)

$recoveryLabel = New-Label (T 'label_recovery') 18 176 145 22
$recoveryBox = New-NumberBox 168 173 1 1000 ([int]$config.max_recovery_attempts) 120
$consecutiveLabel = New-Label (T 'label_consecutive') 310 176 128 22
$consecutiveBox = New-NumberBox 438 173 1 100 ([int]$config.max_consecutive_retries) 120
$memoryLabel = New-Label (T 'label_memory') 18 302 145 22
$memoryBox = New-NumberBox 168 299 128 65536 ([int]$config.memory_limit_mb) 120
$settingsGroup.Controls.AddRange(@($recoveryLabel, $recoveryBox, $consecutiveLabel, $consecutiveBox, $memoryLabel, $memoryBox))

$strategyLabel = New-Label (T 'label_strategy') 18 218 145 22
$strategyBox = [System.Windows.Forms.ComboBox]::new()
$strategyBox.Location = [System.Drawing.Point]::new(168, 215)
$strategyBox.Size = [System.Drawing.Size]::new(120, 24)
$strategyBox.DropDownStyle = 'DropDownList'
[void]$strategyBox.Items.Add((T 'strategy_exponential'))
[void]$strategyBox.Items.Add((T 'strategy_linear'))
[void]$strategyBox.Items.Add((T 'strategy_fixed'))
$strategyBox.SelectedIndex = switch ([string]$config.delay_strategy) {
    'linear' { 1 }
    'fixed' { 2 }
    default { 0 }
}

function Test-SettingsMemoryLimit {
    if ($script:memoryGuardTriggered) { return $true }
    $limitMB = 1024
    try {
        if ($config.memory_limit_mb) { $limitMB = [int]$config.memory_limit_mb }
        $process = Get-Process -Id $PID -ErrorAction Stop
        $usageMB = [int][Math]::Ceiling($process.PrivateMemorySize64 / 1MB)
        if ($limitMB -ge 128 -and $usageMB -ge $limitMB) {
            $script:memoryGuardTriggered = $true
            $logPath = Join-Path $DataDir 'logs\daemon.log'
            New-Item -ItemType Directory -Force -Path (Split-Path -Parent $logPath) | Out-Null
            Add-Content -LiteralPath $logPath -Value ("{0} memory guard triggered component=settings pid={1} private_memory_mb={2} limit_mb={3}" -f ([DateTime]::UtcNow.ToString('o')), $PID, $usageMB, $limitMB)
            [System.Windows.Forms.MessageBox]::Show(
                ([string]::Format((T 'memory_guard_msg'), $usageMB, $limitMB)),
                (T 'memory_guard_title'),
                [System.Windows.Forms.MessageBoxButtons]::OK,
                [System.Windows.Forms.MessageBoxIcon]::Warning
            ) | Out-Null
            $form.Close()
            return $true
        }
    }
    catch { }
    return $false
}
$initialDelayLabel = New-Label (T 'label_initial_delay') 310 218 118 22
$initialDelayBox = New-NumberBox 438 215 1 3600 ([int]$config.initial_delay_seconds) 120
$settingsGroup.Controls.AddRange(@($strategyLabel, $strategyBox, $initialDelayLabel, $initialDelayBox))

$maxDelayLabel = New-Label (T 'label_max_delay') 18 260 145 22
$maxDelayBox = New-NumberBox 168 257 1 86400 ([int]$config.max_delay_seconds) 120
$incrementLabel = New-Label (T 'label_increment') 310 260 128 22
$incrementBox = New-NumberBox 438 257 1 3600 ([int]$config.delay_increment_seconds) 120
$previewLabel = New-Label '' 18 335 540 38
$previewLabel.ForeColor = [System.Drawing.Color]::DimGray
$settingsGroup.Controls.AddRange(@($maxDelayLabel, $maxDelayBox, $incrementLabel, $incrementBox, $previewLabel))

function Assert-SettingsLayout {
    foreach ($pair in @(
        @($recoveryLabel, $recoveryBox, (T 'label_recovery')),
        @($consecutiveLabel, $consecutiveBox, (T 'label_consecutive')),
        @($memoryLabel, $memoryBox, (T 'label_memory')),
        @($strategyLabel, $strategyBox, (T 'label_strategy')),
        @($initialDelayLabel, $initialDelayBox, (T 'label_initial_delay')),
        @($maxDelayLabel, $maxDelayBox, (T 'label_max_delay')),
        @($incrementLabel, $incrementBox, (T 'label_increment'))
    )) {
        if ($pair[0].Right -gt $pair[1].Left) {
            throw ((T 'layout_overlap_err') + [string]$pair[2])
        }
    }
    foreach ($box in @($recoveryBox, $consecutiveBox, $strategyBox, $initialDelayBox, $maxDelayBox, $incrementBox)) {
        if ($box.Left -lt 0 -or $box.Right -gt $settingsGroup.ClientSize.Width) {
            throw (T 'layout_bounds_err')
        }
    }
}
Assert-SettingsLayout

function Get-DelayStrategy {
    switch ($strategyBox.SelectedIndex) {
        1 { return 'linear' }
        2 { return 'fixed' }
        default { return 'exponential' }
    }
}

function Format-PreviewDelay {
    param([long]$Seconds)
    if ($Seconds -lt 60) { return ([string]$Seconds + (T 'unit_second')) }
    if ($Seconds % 3600 -eq 0) { return ([string]($Seconds / 3600) + (T 'unit_hour')) }
    if ($Seconds % 60 -eq 0) { return ([string]($Seconds / 60) + (T 'unit_minute')) }
    return ([string]$Seconds + (T 'unit_second'))
}

function Update-DelayPreview {
    $strategy = Get-DelayStrategy
    $initial = [long]$initialDelayBox.Value
    $maximum = [long]$maxDelayBox.Value
    $increment = [long]$incrementBox.Value
    $count = [Math]::Min([int]$consecutiveBox.Value, 6)
    $values = @()
    $delay = $initial
    for ($index = 0; $index -lt $count; $index++) {
        $value = if ($strategy -eq 'fixed') { $initial } else { [Math]::Min($delay, $maximum) }
        $values += (Format-PreviewDelay $value)
        if ($strategy -eq 'exponential') { $delay = [Math]::Min($delay * 2, $maximum) }
        if ($strategy -eq 'linear') { $delay = [Math]::Min($delay + $increment, $maximum) }
    }
    $sep = if ($script:currentLanguage -eq 'en') { ', ' } else { '，' }
    $suffix = ''
    if ([int]$consecutiveBox.Value -gt $count) {
        $suffix = if ($script:currentLanguage -eq 'en') { ', ...' } else { '，…' }
    }
    $previewLabel.Text = (T 'wait_seq_prefix') + ($values -join $sep) + $suffix
    $maxDelayBox.Enabled = $strategy -ne 'fixed'
    $incrementBox.Enabled = $strategy -eq 'linear'
    $initialDelayLabel.Text = if ($strategy -eq 'fixed') { T 'label_fixed_interval' } else { T 'label_initial_delay' }
}

Update-DelayPreview

$noticeLabel = New-Label '' 22 782 375 28
$noticeLabel.ForeColor = [System.Drawing.Color]::SeaGreen
$form.Controls.Add($noticeLabel)
$saveButton = [System.Windows.Forms.Button]::new()
$saveButton.Text = T 'btn_save'
$saveButton.Location = [System.Drawing.Point]::new(405, 782)
$saveButton.Size = [System.Drawing.Size]::new(100, 30)
$saveButton.BackColor = [System.Drawing.Color]::FromArgb(35, 39, 37)
$saveButton.ForeColor = [System.Drawing.Color]::White
$saveButton.FlatStyle = 'Flat'
$closeButton = [System.Windows.Forms.Button]::new()
$closeButton.Text = T 'btn_close'
$closeButton.Location = [System.Drawing.Point]::new(515, 782)
$closeButton.Size = [System.Drawing.Size]::new(85, 30)
$form.Controls.AddRange(@($saveButton, $closeButton))
$form.CancelButton = $closeButton

$settingsInputControls = @(
    $enabledCheck, $sharedCheck, $notificationsCheck, $promptBox,
    $recoveryBox, $consecutiveBox, $strategyBox, $initialDelayBox,
    $maxDelayBox, $incrementBox, $memoryBox
)

function Set-SettingsBusy {
    param([bool]$Busy)
    $taskList.Enabled = -not $Busy
    $retryNowButton.Enabled = -not $Busy
    $cancelRetryButton.Enabled = -not $Busy
    $restartRetryButton.Enabled = -not $Busy
    foreach ($control in $settingsInputControls) {
        $control.Enabled = -not $Busy
    }
    $saveButton.Enabled = -not $Busy
    $closeButton.Enabled = -not $Busy
    if ($Busy) {
        $saveButton.Text = T 'busy_checking'
        $noticeLabel.Text = T 'busy_notice'
        $noticeLabel.ForeColor = [System.Drawing.Color]::DarkOrange
    } else {
        $saveButton.Text = T 'btn_save'
        Update-DelayPreview
        Update-ActionButtons
    }
}

function Get-StateText {
    param([string]$State)
    $lang = $script:currentLanguage
    switch ($State) {
        'pending'  { if ($lang -eq 'en') { return 'Pending' } else { return '等待中' } }
        'starting' { if ($lang -eq 'en') { return 'Starting' } else { return '启动中' } }
        'running'  { if ($lang -eq 'en') { return 'Running' } else { return '执行中' } }
        'stopped'  { if ($lang -eq 'en') { return 'Limit Reached' } else { return '达到上限' } }
        default { return $State }
    }
}

function Get-StoppedStateText {
    param([string]$Reason)
    $lang = $script:currentLanguage
    if ($Reason -eq 'codex_not_running') {
        if ($lang -eq 'en') { return 'Codex Exited' } else { return 'Codex 已退出' }
    }
    if ($Reason -eq 'shared_app_server_disabled') {
        if ($lang -eq 'en') { return 'Shared Backend Disabled' } else { return '共享后台已关闭' }
    }
    if ($Reason -eq 'codex_restart_required') {
        if ($lang -eq 'en') { return 'Shared Backend Disconnected' } else { return '未接入共享后台' }
    }
    if ($Reason -eq 'codex_home_not_shared') {
        if ($lang -eq 'en') { return 'Task Dir Not Shared' } else { return '任务目录未接入' }
    }
    if ($Reason -eq 'shared_app_server_port_conflict') {
        if ($lang -eq 'en') { return 'Port Conflict' } else { return '恢复端口冲突' }
    }
    if ($Reason -eq 'shared_app_server_port_reserved') {
        if ($lang -eq 'en') { return 'Port Reserved by Windows' } else { return '端口被 Windows 保留' }
    }
    if ($Reason -eq 'shared_app_server_config_invalid') {
        if ($lang -eq 'en') { return 'Config Incompatible; Reverted' } else { return '共享后台配置不兼容，已切回官方后台' }
    }
    if ($Reason -like 'controller_*' -or $Reason -like 'codex_background_*' -or $Reason -eq 'app_server_request_failed') {
        if ($lang -eq 'en') { return 'Recovery Channel Failed' } else { return '恢复通道失败' }
    }
    if ($Reason -eq 'goal_empty_response_limit_block_failed') {
        if ($lang -eq 'en') { return 'Goal Stop Failed' } else { return '目标停止失败' }
    }
    if ($Reason -eq 'goal_empty_response_limit') {
        if ($lang -eq 'en') { return 'Goal Stopped (Empty Replies)' } else { return '目标空回复已停止' }
    }
    if ($lang -eq 'en') { return 'Limit Reached' } else { return '达到上限' }
}

function Get-ClassText {
    param([string]$Class)
    $lang = $script:currentLanguage
    switch ($Class) {
        'transient'      { if ($lang -eq 'en') { return 'Connection Dropped' } else { return '连接中断' } }
        'rate_limit'     { if ($lang -eq 'en') { return 'Rate Limited' } else { return '请求限流' } }
        'server'         { if ($lang -eq 'en') { return 'Provider Failure' } else { return '供应商故障' } }
        'auth_transient' { if ($lang -eq 'en') { return 'Auth Service Error' } else { return '登录服务异常' } }
        'auth_limited'   { if ($lang -eq 'en') { return 'Auth Error' } else { return '登录异常' } }
        'empty_response' { if ($lang -eq 'en') { return 'Empty Model Reply' } else { return '模型空回复' } }
        'unknown'        { if ($lang -eq 'en') { return 'Unknown Fault' } else { return '未知故障' } }
        default          { if ($lang -eq 'en') { return 'Unclassified' } else { return '未分类' } }
    }
}

$stoppedRetryDisplayWindow = [TimeSpan]::FromHours(1)

function Test-StoppedRetryVisible {
    param($Stopped)
    if (-not $Stopped -or [bool]$Stopped.historical) { return $false }
    $now = [DateTimeOffset]::UtcNow
    foreach ($timestamp in @([string]$Stopped.failed_at, [string]$Stopped.stopped_at)) {
        if ([string]::IsNullOrWhiteSpace($timestamp)) { continue }
        try {
            $age = $now - [DateTimeOffset]::Parse($timestamp)
            if ($age -ge [TimeSpan]::Zero -and $age -gt $stoppedRetryDisplayWindow) { return $false }
        } catch { }
    }
    return $true
}

function Update-ActionButtons {
    $retryNowButton.Enabled = $false
    $cancelRetryButton.Enabled = $false
    $restartRetryButton.Enabled = $false
    if ($script:localCommandInProgress) { return }
    if ($taskList.SelectedItems.Count -eq 0) { return }
    $stateName = [string]$taskList.SelectedItems[0].Tag.State
    $retryNowButton.Enabled = $stateName -eq 'pending'
    $cancelRetryButton.Enabled = $stateName -eq 'pending'
    $restartRetryButton.Enabled = $stateName -eq 'stopped'
}

function Update-RuntimeView {
    $status = Read-JsonFile $statusPath
    $state = Read-JsonFile $statePath
    $currentControl = Read-JsonFile $controlPath
    $currentConfig = Read-JsonFile $configPath
    if ($currentConfig -and [int]$currentConfig.shared_app_server_port -gt 0) {
        $sharedPortValue.Text = (T 'shared_port_prefix') + [int]$currentConfig.shared_app_server_port
    }
    $running = $false
    if ($status -and [bool]$status.running -and [int]$status.pid -gt 0) {
        try {
            $process = Get-Process -Id ([int]$status.pid) -ErrorAction Stop
            $running = [string]::Equals($process.Path, $Executable, [System.StringComparison]::OrdinalIgnoreCase)
        } catch { $running = $false }
    }
    $paused = if ($currentControl) { [bool]$currentControl.paused } else { $false }
    if (-not $running) {
        $serviceValue.Text = T 'status_not_running'
        $serviceValue.ForeColor = [System.Drawing.Color]::Firebrick
    } elseif ([string]$status.controller_state -eq 'codex_restart_required') {
        $serviceValue.Text = T 'status_disconnected'
        $serviceValue.ForeColor = [System.Drawing.Color]::DarkOrange
    } elseif ([string]$status.controller_state -eq 'codex_not_running') {
        $serviceValue.Text = T 'status_exited'
        $serviceValue.ForeColor = [System.Drawing.Color]::Firebrick
    } elseif ([string]$status.controller_state -eq 'shared_app_server_disabled') {
        $serviceValue.Text = if (Get-SharedModeRequested (Read-JsonFile $configPath)) { T 'status_shared_temp_unavail' } else { T 'status_shared_disabled' }
        $serviceValue.ForeColor = [System.Drawing.Color]::DarkOrange
    } elseif ([string]$status.controller_state -eq 'shared_app_server_port_reserved') {
        $serviceValue.Text = T 'status_port_reserved'
        $serviceValue.ForeColor = [System.Drawing.Color]::Firebrick
    } elseif ([string]$status.controller_state -eq 'shared_app_server_port_conflict') {
        $serviceValue.Text = T 'status_port_conflict'
        $serviceValue.ForeColor = [System.Drawing.Color]::DarkOrange
    } elseif ([string]$status.controller_state -eq 'shared_app_server_migration_deferred') {
        $serviceValue.Text = T 'status_migration_deferred'
        $serviceValue.ForeColor = [System.Drawing.Color]::DarkOrange
    } elseif ([string]$status.controller_state -eq 'shared_app_server_config_invalid') {
        $serviceValue.Text = T 'status_config_invalid'
        $serviceValue.ForeColor = [System.Drawing.Color]::Firebrick
    } elseif ($paused) {
        $serviceValue.Text = T 'status_paused'
        $serviceValue.ForeColor = [System.Drawing.Color]::DarkOrange
    } else {
        $serviceValue.Text = T 'status_running'
        $serviceValue.ForeColor = [System.Drawing.Color]::SeaGreen
    }

    $selectedID = if ($taskList.SelectedItems.Count -gt 0) { [string]$taskList.SelectedItems[0].Tag.ThreadID } else { '' }
    $taskList.BeginUpdate()
    $taskList.Items.Clear()
    $pendingCount = 0
    $activeCount = 0
    $stoppedCount = 0
    $nextSeconds = $null
    if ($state -and $state.threads) {
        foreach ($property in $state.threads.PSObject.Properties) {
            $threadID = [string]$property.Name
            $thread = $property.Value
            $rowState = ''
            $failureClass = ''
            $attempt = 0
            $maximum = 0
            $consecutive = 0
            $maxConsecutive = 0
            $seconds = $null
            $stopReason = ''
            if (-not $running -and ($thread.pending -or $thread.awaiting)) { continue }
            if ($thread.pending) {
                $rowState = 'pending'
                $pendingCount++
                $failureClass = [string]$thread.pending.class
                $attempt = [int]$thread.pending.attempt
                $maximum = [int]$thread.pending.max_attempts
                $consecutive = [int]$thread.pending.consecutive_retry
                $maxConsecutive = [int]$thread.pending.max_consecutive_retries
                try {
                    $dueAt = [DateTimeOffset]::Parse([string]$thread.pending.due_at)
                    $seconds = [Math]::Max(0, [Math]::Ceiling(($dueAt - [DateTimeOffset]::UtcNow).TotalSeconds))
                    if ($null -eq $nextSeconds -or $seconds -lt $nextSeconds) { $nextSeconds = $seconds }
                } catch { $seconds = 0 }
            } elseif ($thread.awaiting) {
                $rowState = if ([string]$thread.awaiting.retry_turn_id) { 'running' } else { 'starting' }
                $activeCount++
                $failureClass = [string]$thread.awaiting.class
                $attempt = [int]$thread.awaiting.attempt
                $maximum = [int]$thread.awaiting.max_attempts
                $consecutive = [int]$thread.awaiting.consecutive_retry
                $maxConsecutive = [int]$thread.awaiting.max_consecutive_retries
            } elseif ($thread.stopped) {
                if (-not (Test-StoppedRetryVisible $thread.stopped)) { continue }
                $rowState = 'stopped'
                $stoppedCount++
                $failureClass = [string]$thread.stopped.class
                $attempt = [int]$thread.stopped.attempts
                $maximum = [int]$thread.stopped.max_attempts
                $consecutive = [int]$thread.stopped.consecutive_retries
                $maxConsecutive = [int]$thread.stopped.max_consecutive_retries
                $stopReason = [string]$thread.stopped.reason
            } else { continue }
            $shortID = if ($threadID.Length -gt 8) { $threadID.Substring(0, 8) } else { $threadID }
            $countdown = if ($null -ne $seconds) { ([int]$seconds).ToString() + (T 'unit_second') } else { '--' }
            $recoveryText = if ($maximum -gt 0) { "$attempt/$maximum" } else { [string]$attempt }
            $consecutiveText = if ($maxConsecutive -gt 0) { "$consecutive/$maxConsecutive" } else { [string]$consecutive }
            $item = [System.Windows.Forms.ListViewItem]::new($shortID)
            $stateText = if ($rowState -eq 'stopped') { Get-StoppedStateText $stopReason } else { Get-StateText $rowState }
            [void]$item.SubItems.Add($stateText)
            [void]$item.SubItems.Add($countdown)
            [void]$item.SubItems.Add($recoveryText)
            [void]$item.SubItems.Add($consecutiveText)
            [void]$item.SubItems.Add((Get-ClassText $failureClass))
            $item.Tag = [pscustomobject]@{ ThreadID = $threadID; State = $rowState }
            [void]$taskList.Items.Add($item)
            if ($threadID -eq $selectedID) { $item.Selected = $true }
        }
    }
    $taskList.EndUpdate()
    $queueValue.Text = [string]::Format((T 'queue_summary'), $pendingCount, $activeCount, $stoppedCount)
    if (-not $running) {
        $nextValue.Text = T 'next_waiting_service'
    } elseif ($paused -and $pendingCount -gt 0) {
        $nextValue.Text = T 'next_waiting_resume'
    } elseif ($null -ne $nextSeconds) {
        $nextValue.Text = [string]::Format((T 'next_seconds'), [int]$nextSeconds)
    } elseif ($activeCount -gt 0) {
        $nextValue.Text = T 'next_running'
    } else {
        $nextValue.Text = T 'next_none'
    }
    if ($status -and $status.last_scan_at) {
        try { $scanValue.Text = (T 'last_scan') + ([DateTimeOffset]::Parse([string]$status.last_scan_at).ToLocalTime().ToString('HH:mm:ss')) } catch { $scanValue.Text = '' }
    } else { $scanValue.Text = '' }
    Update-ActionButtons
}

function Invoke-TaskAction {
    param([string]$Action)
    if ($taskList.SelectedItems.Count -eq 0) { return }
    $threadID = [string]$taskList.SelectedItems[0].Tag.ThreadID
    $exitCode = Start-LocalCommand 'control' @{
        CODEX_AUTO_RETRY_ACTION = $Action
        CODEX_AUTO_RETRY_THREAD_ID = $threadID
    }
    if ($exitCode -ne 0) {
        [System.Windows.Forms.MessageBox]::Show((T 'msg_action_failed'), 'Codex Auto Retry', 'OK', 'Warning') | Out-Null
    }
    Start-Sleep -Milliseconds 250
    Update-RuntimeView
}

function Apply-Language {
    $form.Text = T 'form_title'
    $langButton.Text = T 'lang_button'
    $statusGroup.Text = T 'status_group'
    $queueGroup.Text = T 'queue_group'
    $taskList.Columns[0].Text = T 'col_task'
    $taskList.Columns[0].Width = 65
    $taskList.Columns[1].Text = T 'col_status'
    $taskList.Columns[1].Width = 110
    $taskList.Columns[2].Text = T 'col_countdown'
    $taskList.Columns[2].Width = 90
    $taskList.Columns[3].Text = T 'col_recovery'
    $taskList.Columns[3].Width = 85
    $taskList.Columns[4].Text = T 'col_consecutive'
    $taskList.Columns[4].Width = 85
    $taskList.Columns[5].Text = T 'col_class'
    $taskList.Columns[5].Width = 85
    $retryNowButton.Text = T 'btn_retry_now'
    $cancelRetryButton.Text = T 'btn_cancel_retry'
    $restartRetryButton.Text = T 'btn_restart_retry'
    $settingsGroup.Text = T 'settings_group'
    $enabledCheck.Text = T 'check_enabled'
    $sharedCheck.Text = T 'check_shared'
    $notificationsCheck.Text = T 'check_notifications'
    $promptLabel.Text = T 'label_prompt'
    $recoveryLabel.Text = T 'label_recovery'
    $consecutiveLabel.Text = T 'label_consecutive'
    $strategyLabel.Text = T 'label_strategy'
    $maxDelayLabel.Text = T 'label_max_delay'
    $incrementLabel.Text = T 'label_increment'
    $memoryLabel.Text = T 'label_memory'
    $saveButton.Text = if ($script:localCommandInProgress) { T 'busy_checking' } else { T 'btn_save' }
    $closeButton.Text = T 'btn_close'

    $prevStrategyIndex = $strategyBox.SelectedIndex
    $strategyBox.Items.Clear()
    [void]$strategyBox.Items.Add((T 'strategy_exponential'))
    [void]$strategyBox.Items.Add((T 'strategy_linear'))
    [void]$strategyBox.Items.Add((T 'strategy_fixed'))
    $strategyBox.SelectedIndex = if ($prevStrategyIndex -ge 0 -and $prevStrategyIndex -le 2) { $prevStrategyIndex } else { 0 }

    Update-DelayPreview
    Update-RuntimeView
}

$langButton.add_Click({
    $script:currentLanguage = if ($script:currentLanguage -eq 'zh') { 'en' } else { 'zh' }
    try {
        $langConfig = [ordered]@{ language = $script:currentLanguage }
        [System.IO.File]::WriteAllText($uiLangPath, ($langConfig | ConvertTo-Json), [System.Text.UTF8Encoding]::new($false))
    } catch { }
    Apply-Language
})

$taskList.add_SelectedIndexChanged({ Update-ActionButtons })
$retryNowButton.add_Click({ Invoke-TaskAction 'retry_now' })
$cancelRetryButton.add_Click({ Invoke-TaskAction 'cancel_retry' })
$restartRetryButton.add_Click({ Invoke-TaskAction 'restart_retry' })
$strategyBox.add_SelectedIndexChanged({ Update-DelayPreview })
$initialDelayBox.add_ValueChanged({ Update-DelayPreview })
$maxDelayBox.add_ValueChanged({ Update-DelayPreview })
$incrementBox.add_ValueChanged({ Update-DelayPreview })
$consecutiveBox.add_ValueChanged({ Update-DelayPreview })
$closeButton.add_Click({ $form.Close() })
$saveButton.add_Click({
    if ($script:localCommandInProgress) { return }
    $prompt = $promptBox.Text.Trim()
    if (-not $prompt) {
        [System.Windows.Forms.MessageBox]::Show((T 'msg_prompt_empty'), 'Codex Auto Retry', 'OK', 'Warning') | Out-Null
        return
    }
    $delayStrategy = Get-DelayStrategy
    if ($delayStrategy -ne 'fixed' -and [int]$maxDelayBox.Value -lt [int]$initialDelayBox.Value) {
        [System.Windows.Forms.MessageBox]::Show((T 'msg_max_less_initial'), 'Codex Auto Retry', 'OK', 'Warning') | Out-Null
        return
    }
    $payload = [ordered]@{
        retry_prompt = $prompt
        max_recovery_attempts = [int]$recoveryBox.Value
        max_consecutive_retries = [int]$consecutiveBox.Value
        initial_delay_seconds = [int]$initialDelayBox.Value
        max_delay_seconds = [int]$maxDelayBox.Value
        delay_increment_seconds = [int]$incrementBox.Value
        memory_limit_mb = [int]$memoryBox.Value
        delay_strategy = $delayStrategy
        show_notifications = [bool]$notificationsCheck.Checked
        paused = -not [bool]$enabledCheck.Checked
        shared_app_server_enabled = [bool]$sharedCheck.Checked
    }
    $currentConfig = Read-JsonFile $configPath
    $storedSharedEnabled = if ($currentConfig) { Get-SharedModeRequested $currentConfig } else { Get-SharedModeRequested $config }
    $storedSharedPort = if ($currentConfig) { [int]$currentConfig.shared_app_server_port } else { [int]$config.shared_app_server_port }
    $sharedModeChanged = [bool]$sharedCheck.Checked -ne $storedSharedEnabled
    $sharedModeEnabling = [bool]$sharedCheck.Checked -and -not $storedSharedEnabled
    $requestPath = Join-Path $DataDir ('settings-request-' + [guid]::NewGuid().ToString('N') + '.json')
    $script:localCommandInProgress = $true
    Set-SettingsBusy $true
    if ($sharedModeEnabling) {
        $noticeLabel.Text = T 'save_checking_health'
    } elseif ($sharedModeChanged) {
        $noticeLabel.Text = T 'save_closing_shared'
    } else {
        $noticeLabel.Text = T 'save_saving'
    }
    try {
        [System.IO.File]::WriteAllText($requestPath, ($payload | ConvertTo-Json -Depth 4), [System.Text.UTF8Encoding]::new($false))
        $exitCode = Start-LocalCommand 'save-settings' @{
            CODEX_AUTO_RETRY_SETTINGS_FILE = $requestPath
        }
        if ($exitCode -eq -2) {
            throw (T 'save_timeout')
        }
        if ($exitCode -ne 0) { throw (T 'save_validation_failed') }
        $noticeLabel.Text = T 'save_saved'
        $noticeLabel.ForeColor = [System.Drawing.Color]::SeaGreen
        Update-RuntimeView
    } catch {
        if ($sharedModeChanged) {
            $latestConfig = Read-JsonFile $configPath
            if ($latestConfig) {
                $sharedCheck.Checked = Get-SharedModeRequested $latestConfig
            } else {
                $sharedCheck.Checked = $storedSharedEnabled
            }
        }
        if ($sharedModeEnabling -and $exitCode -eq $localCommandExitPortReserved) {
            $noticeLabel.Text = [string]::Format((T 'save_fail_reserved'), $storedSharedPort)
        } elseif ($sharedModeEnabling -and $exitCode -eq $localCommandExitPortConflict) {
            $noticeLabel.Text = [string]::Format((T 'save_fail_conflict'), $storedSharedPort)
        } elseif ($script:localCommandTimedOut) {
            $noticeLabel.Text = T 'save_fail_timeout'
        } elseif ($sharedModeEnabling) {
            $noticeLabel.Text = T 'save_fail_health'
        } elseif ($sharedModeChanged) {
            $noticeLabel.Text = T 'save_fail_close'
        } else {
            $noticeLabel.Text = T 'save_fail_range'
        }
        $noticeLabel.ForeColor = [System.Drawing.Color]::Firebrick
    } finally {
        Remove-Item -LiteralPath $requestPath -Force -ErrorAction SilentlyContinue
        $script:localCommandInProgress = $false
        Set-SettingsBusy $false
    }
})

$form.add_FormClosing({
    if ($script:localCommandProcess) {
        Stop-LocalCommandProcess $script:localCommandProcess
    }
})

$timer = [System.Windows.Forms.Timer]::new()
$timer.Interval = if ($SmokeTest) { 100 } else { 1000 }
$smokeDeadline = [DateTimeOffset]::UtcNow.AddSeconds(15)
$timer.add_Tick({
    if (Test-SettingsMemoryLimit) { return }
    if ($SmokeTest) {
        Update-RuntimeView
        if ((Test-Path -LiteralPath $smokeClosePath) -or [DateTimeOffset]::UtcNow -ge $smokeDeadline) {
            $form.Close()
        }
        return
    }
    Update-RuntimeView
})
$form.add_Shown({
    Apply-Language
    Update-RuntimeView
    if ($SmokeTest) {
        [System.IO.File]::WriteAllText(
            (Join-Path $DataDir 'settings-smoke.ok'),
            'passed',
            [System.Text.UTF8Encoding]::new($false)
        )
        [System.IO.File]::WriteAllText(
            (Join-Path $DataDir 'settings-layout-smoke.ok'),
            'separated',
            [System.Text.UTF8Encoding]::new($false)
        )
    }
    $timer.Start()
})
$form.add_FormClosed({ $timer.Stop(); $timer.Dispose() })
[void]$form.ShowDialog()
