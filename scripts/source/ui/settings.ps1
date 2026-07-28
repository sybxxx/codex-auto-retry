param(
    [Parameter(Mandatory = $true)][string]$DataDir,
    [Parameter(Mandatory = $true)][string]$Executable,
    [switch]$SmokeTest
)

$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
[System.Windows.Forms.Application]::EnableVisualStyles()

$configPath = Join-Path $DataDir 'config.json'
$controlPath = Join-Path $DataDir 'control.json'
$statusPath = Join-Path $DataDir 'status.json'
$statePath = Join-Path $DataDir 'state.json'
$smokeClosePath = Join-Path $DataDir 'settings-smoke-close.signal'

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

function Start-LocalCommand {
    param([string]$Mode, [hashtable]$Environment)
    $info = [System.Diagnostics.ProcessStartInfo]::new()
    $info.FileName = $Executable
    $info.Arguments = $Mode
    $info.UseShellExecute = $false
    $info.CreateNoWindow = $true
    $info.EnvironmentVariables['CODEX_AUTO_RETRY_DATA_DIR'] = $DataDir
    foreach ($entry in $Environment.GetEnumerator()) {
        $info.EnvironmentVariables[[string]$entry.Key] = [string]$entry.Value
    }
    $process = [System.Diagnostics.Process]::Start($info)
    $process.WaitForExit()
    $exitCode = $process.ExitCode
    $process.Dispose()
    return $exitCode
}

function New-Label {
    param([string]$Text, [int]$X, [int]$Y, [int]$Width = 160, [int]$Height = 22)
    $label = [System.Windows.Forms.Label]::new()
    $label.Text = $Text
    $label.Location = [System.Drawing.Point]::new($X, $Y)
    $label.Size = [System.Drawing.Size]::new($Width, $Height)
    return $label
}

function New-NumberBox {
    param([int]$X, [int]$Y, [int]$Minimum, [int]$Maximum, [int]$Value, [int]$Width = 130)
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
    [System.Windows.Forms.MessageBox]::Show('无法读取自动重试设置。', 'Codex Auto Retry', 'OK', 'Error') | Out-Null
    exit 1
}

$form = [System.Windows.Forms.Form]::new()
$form.Text = 'Codex Auto Retry 设置'
$form.StartPosition = 'CenterScreen'
$form.FormBorderStyle = 'FixedDialog'
$form.MaximizeBox = $false
$form.MinimizeBox = $false
$form.ClientSize = [System.Drawing.Size]::new(620, 690)
$form.Font = [System.Drawing.Font]::new('Microsoft YaHei UI', 9)
$form.Icon = [System.Drawing.SystemIcons]::Application
if ($SmokeTest) {
    $form.Opacity = 0
    $form.ShowInTaskbar = $false
}

$title = New-Label 'Codex Auto Retry' 22 18 300 30
$title.Font = [System.Drawing.Font]::new('Microsoft YaHei UI', 15, [System.Drawing.FontStyle]::Bold)
$form.Controls.Add($title)
$versionText = ''
if ($runtimeStatus -and $runtimeStatus.version) { $versionText = 'v' + [string]$runtimeStatus.version }
$versionLabel = New-Label $versionText 500 24 95 22
$versionLabel.TextAlign = 'MiddleRight'
$versionLabel.ForeColor = [System.Drawing.Color]::DimGray
$form.Controls.Add($versionLabel)

$statusGroup = [System.Windows.Forms.GroupBox]::new()
$statusGroup.Text = '当前状态'
$statusGroup.Location = [System.Drawing.Point]::new(20, 58)
$statusGroup.Size = [System.Drawing.Size]::new(580, 105)
$form.Controls.Add($statusGroup)
$serviceValue = New-Label '正在读取…' 18 25 170 24
$serviceValue.Font = [System.Drawing.Font]::new('Microsoft YaHei UI', 10, [System.Drawing.FontStyle]::Bold)
$queueValue = New-Label '队列：--' 200 25 180 24
$nextValue = New-Label '下次重试：--' 390 25 170 24
$scanValue = New-Label '' 18 62 535 22
$scanValue.ForeColor = [System.Drawing.Color]::DimGray
$statusGroup.Controls.AddRange(@($serviceValue, $queueValue, $nextValue, $scanValue))

$queueGroup = [System.Windows.Forms.GroupBox]::new()
$queueGroup.Text = '任务队列（仅显示任务编号，不读取对话内容）'
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
[void]$taskList.Columns.Add('任务', 105)
[void]$taskList.Columns.Add('状态', 110)
[void]$taskList.Columns.Add('倒计时', 105)
[void]$taskList.Columns.Add('次数', 85)
[void]$taskList.Columns.Add('故障类型', 125)
$queueGroup.Controls.Add($taskList)
$retryNowButton = [System.Windows.Forms.Button]::new()
$retryNowButton.Text = '立即重试'
$retryNowButton.Location = [System.Drawing.Point]::new(284, 153)
$retryNowButton.Size = [System.Drawing.Size]::new(86, 27)
$cancelRetryButton = [System.Windows.Forms.Button]::new()
$cancelRetryButton.Text = '取消等待'
$cancelRetryButton.Location = [System.Drawing.Point]::new(378, 153)
$cancelRetryButton.Size = [System.Drawing.Size]::new(86, 27)
$restartRetryButton = [System.Windows.Forms.Button]::new()
$restartRetryButton.Text = '重新开始'
$restartRetryButton.Location = [System.Drawing.Point]::new(472, 153)
$restartRetryButton.Size = [System.Drawing.Size]::new(86, 27)
$queueGroup.Controls.AddRange(@($retryNowButton, $cancelRetryButton, $restartRetryButton))

$settingsGroup = [System.Windows.Forms.GroupBox]::new()
$settingsGroup.Text = '自动重试设置'
$settingsGroup.Location = [System.Drawing.Point]::new(20, 378)
$settingsGroup.Size = [System.Drawing.Size]::new(580, 245)
$form.Controls.Add($settingsGroup)

$enabledCheck = [System.Windows.Forms.CheckBox]::new()
$enabledCheck.Text = '启用自动重试'
$enabledCheck.Location = [System.Drawing.Point]::new(18, 25)
$enabledCheck.Size = [System.Drawing.Size]::new(160, 24)
$enabledCheck.Checked = -not [bool]$control.paused
$notificationsCheck = [System.Windows.Forms.CheckBox]::new()
$notificationsCheck.Text = '达到上限时显示 Windows 通知'
$notificationsCheck.Location = [System.Drawing.Point]::new(250, 25)
$notificationsCheck.Size = [System.Drawing.Size]::new(285, 24)
$notificationsCheck.Checked = [bool]$config.show_notifications
$settingsGroup.Controls.AddRange(@($enabledCheck, $notificationsCheck))

$settingsGroup.Controls.Add((New-Label '后备重试文字' 18 60 180 22))
$promptBox = [System.Windows.Forms.TextBox]::new()
$promptBox.Location = [System.Drawing.Point]::new(18, 83)
$promptBox.Size = [System.Drawing.Size]::new(540, 54)
$promptBox.Multiline = $true
$promptBox.MaxLength = 500
$promptBox.ScrollBars = 'Vertical'
$promptBox.Text = [string]$config.retry_prompt
$settingsGroup.Controls.Add($promptBox)

$attemptLabel = New-Label '最多连续重试次数' 18 151 145 22
$attemptBox = New-NumberBox 168 148 1 20 ([int]$config.max_retry_attempts) 120
$settingsGroup.Controls.Add($attemptLabel)
$settingsGroup.Controls.Add($attemptBox)
$initialDelayLabel = New-Label '首次等待（秒）' 310 151 110 22
$initialDelayBox = New-NumberBox 428 148 1 3600 ([int]$config.initial_delay_seconds) 130
$settingsGroup.Controls.Add($initialDelayLabel)
$settingsGroup.Controls.Add($initialDelayBox)
$maxDelayLabel = New-Label '最大等待（秒）' 18 193 145 22
$maxDelayBox = New-NumberBox 168 190 1 86400 ([int]$config.max_delay_seconds) 120
$settingsGroup.Controls.Add($maxDelayLabel)
$settingsGroup.Controls.Add($maxDelayBox)
$hint = New-Label '目标模式仍使用 Codex 原生恢复，不会发送上面的文字。' 310 193 248 42
$hint.ForeColor = [System.Drawing.Color]::DimGray
$settingsGroup.Controls.Add($hint)

function Assert-SettingsLayout {
    foreach ($pair in @(
        @($attemptLabel, $attemptBox, '最多连续重试次数'),
        @($initialDelayLabel, $initialDelayBox, '首次等待'),
        @($maxDelayLabel, $maxDelayBox, '最大等待')
    )) {
        if ($pair[0].Right -gt $pair[1].Left) {
            throw ("设置布局发生遮挡：" + [string]$pair[2])
        }
    }
    foreach ($box in @($attemptBox, $initialDelayBox, $maxDelayBox)) {
        if ($box.Left -lt 0 -or $box.Right -gt $settingsGroup.ClientSize.Width) {
            throw '设置输入框超出可见区域。'
        }
    }
}
Assert-SettingsLayout

$noticeLabel = New-Label '' 22 637 390 28
$noticeLabel.ForeColor = [System.Drawing.Color]::SeaGreen
$form.Controls.Add($noticeLabel)
$saveButton = [System.Windows.Forms.Button]::new()
$saveButton.Text = '保存设置'
$saveButton.Location = [System.Drawing.Point]::new(420, 637)
$saveButton.Size = [System.Drawing.Size]::new(85, 30)
$saveButton.BackColor = [System.Drawing.Color]::FromArgb(35, 39, 37)
$saveButton.ForeColor = [System.Drawing.Color]::White
$saveButton.FlatStyle = 'Flat'
$closeButton = [System.Windows.Forms.Button]::new()
$closeButton.Text = '关闭'
$closeButton.Location = [System.Drawing.Point]::new(515, 637)
$closeButton.Size = [System.Drawing.Size]::new(85, 30)
$form.Controls.AddRange(@($saveButton, $closeButton))
$form.CancelButton = $closeButton

function Get-StateText {
    param([string]$State)
    switch ($State) {
        'pending' { '等待中' }
        'starting' { '启动中' }
        'running' { '执行中' }
        'stopped' { '达到上限' }
        default { $State }
    }
}

function Get-ClassText {
    param([string]$Class)
    switch ($Class) {
        'transient' { '连接中断' }
        'rate_limit' { '请求限流' }
        'server' { '供应商故障' }
        'auth_transient' { '登录服务异常' }
        'auth_limited' { '登录异常' }
        'unknown' { '未知故障' }
        default { '未分类' }
    }
}

function Update-ActionButtons {
    $retryNowButton.Enabled = $false
    $cancelRetryButton.Enabled = $false
    $restartRetryButton.Enabled = $false
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
    $running = $false
    if ($status -and [bool]$status.running -and [int]$status.pid -gt 0) {
        try {
            $process = Get-Process -Id ([int]$status.pid) -ErrorAction Stop
            $running = [string]::Equals($process.Path, $Executable, [System.StringComparison]::OrdinalIgnoreCase)
        } catch { $running = $false }
    }
    $paused = if ($currentControl) { [bool]$currentControl.paused } else { $false }
    if (-not $running) {
        $serviceValue.Text = '后台服务未运行'
        $serviceValue.ForeColor = [System.Drawing.Color]::Firebrick
    } elseif ($paused) {
        $serviceValue.Text = '已暂停'
        $serviceValue.ForeColor = [System.Drawing.Color]::DarkOrange
    } else {
        $serviceValue.Text = '运行中'
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
            $seconds = $null
            if (-not $running -and ($thread.pending -or $thread.awaiting)) { continue }
            if ($thread.pending) {
                $rowState = 'pending'
                $pendingCount++
                $failureClass = [string]$thread.pending.class
                $attempt = [int]$thread.pending.attempt
                $maximum = [int]$thread.pending.max_attempts
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
            } elseif ($thread.stopped) {
                $rowState = 'stopped'
                $stoppedCount++
                $failureClass = [string]$thread.stopped.class
                $attempt = [int]$thread.stopped.attempts
                $maximum = [int]$thread.stopped.max_attempts
            } else { continue }
            $shortID = if ($threadID.Length -gt 8) { $threadID.Substring(0, 8) } else { $threadID }
            $countdown = if ($null -ne $seconds) { ([int]$seconds).ToString() + ' 秒' } else { '--' }
            $attemptText = if ($maximum -gt 0) { "$attempt/$maximum" } else { [string]$attempt }
            $item = [System.Windows.Forms.ListViewItem]::new($shortID)
            [void]$item.SubItems.Add((Get-StateText $rowState))
            [void]$item.SubItems.Add($countdown)
            [void]$item.SubItems.Add($attemptText)
            [void]$item.SubItems.Add((Get-ClassText $failureClass))
            $item.Tag = [pscustomobject]@{ ThreadID = $threadID; State = $rowState }
            [void]$taskList.Items.Add($item)
            if ($threadID -eq $selectedID) { $item.Selected = $true }
        }
    }
    $taskList.EndUpdate()
    $queueValue.Text = "队列：$pendingCount 等待 / $activeCount 执行 / $stoppedCount 停止"
    if (-not $running) {
        $nextValue.Text = '下次重试：等待服务启动'
    } elseif ($paused -and $pendingCount -gt 0) {
        $nextValue.Text = '下次重试：等待恢复'
    } elseif ($null -ne $nextSeconds) {
        $nextValue.Text = ('下次重试：' + [int]$nextSeconds + ' 秒')
    } elseif ($activeCount -gt 0) {
        $nextValue.Text = '下次重试：正在执行'
    } else {
        $nextValue.Text = '下次重试：--'
    }
    if ($status -and $status.last_scan_at) {
        try { $scanValue.Text = '最近扫描：' + ([DateTimeOffset]::Parse([string]$status.last_scan_at).ToLocalTime().ToString('HH:mm:ss')) } catch { $scanValue.Text = '' }
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
        [System.Windows.Forms.MessageBox]::Show('操作没有生效，任务状态可能已经改变。', 'Codex Auto Retry', 'OK', 'Warning') | Out-Null
    }
    Start-Sleep -Milliseconds 250
    Update-RuntimeView
}

$taskList.add_SelectedIndexChanged({ Update-ActionButtons })
$retryNowButton.add_Click({ Invoke-TaskAction 'retry_now' })
$cancelRetryButton.add_Click({ Invoke-TaskAction 'cancel_retry' })
$restartRetryButton.add_Click({ Invoke-TaskAction 'restart_retry' })
$closeButton.add_Click({ $form.Close() })
$saveButton.add_Click({
    $prompt = $promptBox.Text.Trim()
    if (-not $prompt) {
        [System.Windows.Forms.MessageBox]::Show('后备重试文字不能为空。', 'Codex Auto Retry', 'OK', 'Warning') | Out-Null
        return
    }
    if ([int]$maxDelayBox.Value -lt [int]$initialDelayBox.Value) {
        [System.Windows.Forms.MessageBox]::Show('最大等待时间不能小于首次等待时间。', 'Codex Auto Retry', 'OK', 'Warning') | Out-Null
        return
    }
    $payload = [ordered]@{
        retry_prompt = $prompt
        max_retry_attempts = [int]$attemptBox.Value
        initial_delay_seconds = [int]$initialDelayBox.Value
        max_delay_seconds = [int]$maxDelayBox.Value
        show_notifications = [bool]$notificationsCheck.Checked
        paused = -not [bool]$enabledCheck.Checked
    }
    $requestPath = Join-Path $DataDir ('settings-request-' + [guid]::NewGuid().ToString('N') + '.json')
    try {
        [System.IO.File]::WriteAllText($requestPath, ($payload | ConvertTo-Json -Depth 4), [System.Text.UTF8Encoding]::new($false))
        $exitCode = Start-LocalCommand 'save-settings' @{
            CODEX_AUTO_RETRY_SETTINGS_FILE = $requestPath
        }
        if ($exitCode -ne 0) { throw '设置校验失败' }
        $noticeLabel.Text = '设置已保存，将在下一次扫描时生效。'
        $noticeLabel.ForeColor = [System.Drawing.Color]::SeaGreen
        Update-RuntimeView
    } catch {
        $noticeLabel.Text = '保存失败，请检查设置范围。'
        $noticeLabel.ForeColor = [System.Drawing.Color]::Firebrick
    } finally {
        Remove-Item -LiteralPath $requestPath -Force -ErrorAction SilentlyContinue
    }
})

$timer = [System.Windows.Forms.Timer]::new()
$timer.Interval = if ($SmokeTest) { 100 } else { 1000 }
$smokeDeadline = [DateTimeOffset]::UtcNow.AddSeconds(15)
$timer.add_Tick({
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
