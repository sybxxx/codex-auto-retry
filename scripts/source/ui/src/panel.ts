import {
  App,
  applyDocumentTheme,
  applyHostFonts,
  applyHostStyleVariables,
  type McpUiHostContext,
} from "@modelcontextprotocol/ext-apps";
import {
  Activity,
  Clock,
  Play,
  RefreshCw,
  RotateCcw,
  Save,
  X,
  createIcons,
} from "lucide";
import "./panel.css";

type FailureClass =
  | "transient"
  | "rate_limit"
  | "server"
  | "auth_transient"
  | "auth_limited"
  | "empty_response"
  | "unknown"
  | "none";

type ManagedRetry = {
  thread_id: string;
  label: string;
  state: "pending" | "starting" | "running" | "stopped";
  class: FailureClass;
  due_at?: string;
  seconds_remaining: number;
  recovery_attempt: number;
  max_recovery_attempts?: number;
  consecutive_retry: number;
  max_consecutive_retries?: number;
  action?: string;
  can_retry_now: boolean;
  can_cancel: boolean;
  can_restart: boolean;
  stop_reason?: string;
};

type ManagementSnapshot = {
  version: string;
  running: boolean;
  heartbeat_stale: boolean;
  paused: boolean;
  shared_app_server_enabled: boolean;
  retry_prompt: string;
  max_recovery_attempts: number;
  max_consecutive_retries: number;
  initial_delay_seconds: number;
  max_delay_seconds: number;
  delay_increment_seconds: number;
  delay_strategy: "fixed" | "linear" | "exponential";
  show_notifications: boolean;
  now: string;
  last_scan_at?: string;
  pending_retries: number;
  active_retries: number;
  stopped_retries: number;
  watched_roots: number;
  controller_state?: string;
  last_error?: string;
  notice?: string;
  retries: ManagedRetry[];
};

type ToolResult = {
  structuredContent?: unknown;
  isError?: boolean;
  content?: Array<{ type: string; text?: string }>;
};

const iconSet = { Activity, Clock, Play, RefreshCw, RotateCcw, Save, X };
const elements = {
  shell: required<HTMLElement>("app-shell"),
  serviceLine: required<HTMLElement>("service-line"),
  serviceStatus: required<HTMLElement>("service-status"),
  version: required<HTMLElement>("version"),
  refreshButton: required<HTMLButtonElement>("refresh-button"),
  notice: required<HTMLElement>("notice"),
  queueCount: required<HTMLElement>("queue-count"),
  nextRetry: required<HTMLElement>("next-retry"),
  queueSummary: required<HTMLElement>("queue-summary"),
  queueList: required<HTMLElement>("queue-list"),
  scanTime: required<HTMLElement>("scan-time"),
  pauseToggle: required<HTMLInputElement>("pause-toggle"),
  sharedAppServerToggle: required<HTMLInputElement>("shared-app-server-toggle"),
  sharedAppServerDescription: required<HTMLElement>("shared-app-server-description"),
  pauseDescription: required<HTMLElement>("pause-description"),
  retryPrompt: required<HTMLTextAreaElement>("retry-prompt"),
  promptCount: required<HTMLElement>("prompt-count"),
  promptError: required<HTMLElement>("prompt-error"),
  savePrompt: required<HTMLButtonElement>("save-prompt"),
  maxRecoveryAttempts: required<HTMLInputElement>("max-recovery-attempts"),
  maxConsecutiveRetries: required<HTMLInputElement>("max-consecutive-retries"),
  delayStrategies: requiredAll<HTMLInputElement>('input[name="delay-strategy"]'),
  initialDelayLabel: required<HTMLElement>("initial-delay-label"),
  initialDelay: required<HTMLInputElement>("initial-delay"),
  maxDelay: required<HTMLInputElement>("max-delay"),
  delayIncrement: required<HTMLInputElement>("delay-increment"),
  delayPreview: required<HTMLElement>("delay-preview"),
  settingsError: required<HTMLElement>("settings-error"),
  notificationsToggle: required<HTMLInputElement>("notifications-toggle"),
  saveSettings: required<HTMLButtonElement>("save-settings"),
};

let app: App | null = null;
let snapshot: ManagementSnapshot | null = null;
let savedPrompt = "";
let savedSettings = "";
let busyCount = 0;
let noticeTimer = 0;

function required<T extends HTMLElement>(id: string): T {
  const element = document.getElementById(id);
  if (!element) throw new Error(`Missing element: ${id}`);
  return element as T;
}

function requiredAll<T extends Element>(selector: string): T[] {
  const values = Array.from(document.querySelectorAll<T>(selector));
  if (values.length === 0) throw new Error(`Missing elements: ${selector}`);
  return values;
}

function refreshIcons(): void {
  createIcons({ icons: iconSet });
}

function handleHostContext(context: McpUiHostContext): void {
  if (context.theme) applyDocumentTheme(context.theme);
  if (context.styles?.variables) applyHostStyleVariables(context.styles.variables);
  if (context.styles?.css?.fonts) applyHostFonts(context.styles.css.fonts);
  if (context.safeAreaInsets) {
    const { top, right, bottom, left } = context.safeAreaInsets;
    elements.shell.style.paddingTop = `${Math.max(14, top)}px`;
    elements.shell.style.paddingRight = `${Math.max(14, right)}px`;
    elements.shell.style.paddingBottom = `${Math.max(14, bottom)}px`;
    elements.shell.style.paddingLeft = `${Math.max(14, left)}px`;
  }
}

function extractSnapshot(result: ToolResult): ManagementSnapshot | null {
  const value = result.structuredContent;
  if (!value || typeof value !== "object") return null;
  const candidate = value as Partial<ManagementSnapshot>;
  if (!Array.isArray(candidate.retries) || typeof candidate.retry_prompt !== "string") return null;
  return candidate as ManagementSnapshot;
}

function render(next: ManagementSnapshot): void {
  const keepSettingsDraft = snapshot !== null && currentSettings() !== savedSettings;
  snapshot = next;
  savedPrompt = next.retry_prompt;
  elements.version.textContent = next.version ? `v${next.version}` : "";
  elements.retryPrompt.disabled = false;
  elements.pauseToggle.disabled = false;
  elements.sharedAppServerToggle.disabled = false;
  if (!keepSettingsDraft) {
    elements.retryPrompt.value = next.retry_prompt;
    elements.maxRecoveryAttempts.value = String(next.max_recovery_attempts);
    elements.maxConsecutiveRetries.value = String(next.max_consecutive_retries);
    elements.initialDelay.value = String(next.initial_delay_seconds);
    elements.maxDelay.value = String(next.max_delay_seconds);
    elements.delayIncrement.value = String(next.delay_increment_seconds);
    for (const option of elements.delayStrategies) option.checked = option.value === next.delay_strategy;
    elements.notificationsToggle.checked = next.show_notifications;
  }
  savedSettings = serializedSettings(next);
  elements.pauseToggle.checked = !next.paused;
  elements.sharedAppServerToggle.checked = next.shared_app_server_enabled;
  elements.sharedAppServerDescription.textContent = next.shared_app_server_enabled
    ? "正在使用插件拥有且已通过健康检查的后台"
    : "默认关闭，不影响 Codex 官方后台";
  updatePromptState();
  renderService(next);
  renderMetrics(next);
  renderQueue(next);
  renderScanTime(next);
  if (next.notice) showNotice(next.notice, false);
  refreshIcons();
}

function renderService(next: ManagementSnapshot): void {
  const dot = document.createElement("span");
  dot.className = "status-dot";
  let label = "未运行";
  let detail = "未检测到有效心跳";
  if (next.running && next.controller_state === "codex_restart_required") {
    label = "等待重启";
    detail = "请重启一次 Codex，使后台恢复通道生效";
    dot.classList.add("status-dot-warning");
  } else if (next.running && next.controller_state === "codex_not_running") {
    label = "Codex 已退出";
    detail = "相关任务已停止自动重试；启动 Codex 后可手动重新开始";
    dot.classList.add("status-dot-danger");
  } else if (next.running && next.controller_state === "shared_app_server_disabled") {
    label = "共享后台已关闭";
    detail = "Codex 继续使用官方后台；打开共享后台后才会执行静默恢复";
    dot.classList.add("status-dot-warning");
  } else if (next.running && next.controller_state && !["ready", "starting"].includes(next.controller_state)) {
    label = "恢复通道异常";
    detail = `自动重试已停止继续空转：${controllerStateLabel(next.controller_state)}`;
    dot.classList.add("status-dot-danger");
  } else if (next.running && next.paused) {
    label = "已暂停";
    detail = "监控保持运行，新重试暂不执行";
    dot.classList.add("status-dot-warning");
  } else if (next.running) {
    label = "运行中";
    detail = `正在监控 ${next.watched_roots} 个会话位置`;
    dot.classList.add("status-dot-positive");
  } else {
    dot.classList.add("status-dot-danger");
  }
  elements.serviceStatus.replaceChildren(dot, document.createTextNode(label));
  elements.serviceLine.textContent = detail;
  elements.pauseDescription.textContent = next.paused ? "已暂停新重试" : "运行中";
}

function renderMetrics(next: ManagementSnapshot): void {
  const total = next.pending_retries + next.active_retries + next.stopped_retries;
  elements.queueCount.textContent = String(total);
  const pending = next.retries
    .filter((retry) => retry.state === "pending" && retry.due_at)
    .sort((a, b) => Date.parse(a.due_at ?? "") - Date.parse(b.due_at ?? ""));
  if (next.paused && pending.length > 0) {
    elements.nextRetry.textContent = "等待恢复";
  } else if (pending.length > 0) {
    elements.nextRetry.dataset.dueAt = pending[0].due_at ?? "";
    updateCountdownElement(elements.nextRetry);
  } else if (next.active_retries > 0) {
    elements.nextRetry.textContent = "正在重试";
    delete elements.nextRetry.dataset.dueAt;
  } else {
    elements.nextRetry.textContent = "--";
    delete elements.nextRetry.dataset.dueAt;
  }
  if (total === 0) {
    elements.queueSummary.textContent = "当前没有等待中的任务";
  } else {
    elements.queueSummary.textContent = `${next.pending_retries} 个等待中，${next.active_retries} 个执行中，${next.stopped_retries} 个已停止`;
  }
}

function renderQueue(next: ManagementSnapshot): void {
  elements.queueList.replaceChildren();
  if (next.retries.length === 0) {
    const empty = document.createElement("div");
    empty.className = "empty-state";
    empty.append(icon("activity"), document.createTextNode("队列为空"));
    elements.queueList.append(empty);
    return;
  }
  for (const retry of next.retries) {
    elements.queueList.append(createQueueItem(retry, next.paused));
  }
}

function createQueueItem(retry: ManagedRetry, paused: boolean): HTMLElement {
  const row = document.createElement("article");
  row.className = "queue-item";
  row.dataset.threadId = retry.thread_id;

  const main = document.createElement("div");
  main.className = "queue-main";
  const queueIcon = document.createElement("span");
  queueIcon.className = `queue-icon${retry.state === "pending" ? "" : retry.state === "stopped" ? " stopped" : " active"}`;
  queueIcon.append(icon(retry.state === "pending" ? "clock" : retry.state === "stopped" ? "x" : "refresh-cw"));
  const copy = document.createElement("div");
  copy.className = "queue-copy";
  const title = document.createElement("div");
  title.className = "queue-title";
  title.textContent = retry.label;
  title.title = retry.thread_id;
  const meta = document.createElement("div");
  meta.className = "queue-meta";
  const recovery = retry.max_recovery_attempts
    ? `本次故障恢复 ${retry.recovery_attempt}/${retry.max_recovery_attempts}`
    : `本次故障恢复 ${retry.recovery_attempt}`;
  const consecutive = retry.max_consecutive_retries
    ? `连续无进展 ${retry.consecutive_retry}/${retry.max_consecutive_retries}`
    : `连续无进展 ${retry.consecutive_retry}`;
  const stateLabel = retry.state === "pending"
    ? "等待中"
    : retry.state === "stopped"
      ? stoppedStateLabel(retry)
      : actionLabel(retry.action);
  meta.append(
    textSpan(classLabel(retry.class)),
    textSpan(recovery),
    textSpan(consecutive),
    textSpan(stateLabel),
  );
  copy.append(title, meta);
  main.append(queueIcon, copy);

  const state = document.createElement("div");
  state.className = "queue-state";
  const primary = document.createElement("strong");
  const secondary = document.createElement("span");
  if (retry.state === "pending" && retry.due_at) {
    primary.dataset.dueAt = retry.due_at;
    updateCountdownElement(primary);
    secondary.textContent = paused ? "恢复后执行" : "后重试";
  } else if (retry.state === "stopped") {
    primary.textContent = "已停止";
    secondary.textContent = stopReasonLabel(retry);
  } else {
    primary.textContent = retry.state === "running" ? "执行中" : "启动中";
    secondary.textContent = actionLabel(retry.action);
  }
  state.append(primary, secondary);

  const actions = document.createElement("div");
  actions.className = "queue-actions";
  if (retry.can_retry_now) {
    actions.append(actionButton("play", "立即重试", "retry-action", () => runThreadAction("retry_now", retry.thread_id)));
  }
  if (retry.can_cancel) {
    actions.append(actionButton("x", "取消这次重试", "cancel-action", () => runThreadAction("cancel_retry", retry.thread_id)));
  }
  if (retry.can_restart) {
    actions.append(actionButton("rotate-ccw", "重新开始计数并重试", "retry-action", () => runThreadAction("restart_retry", retry.thread_id)));
  }
  row.append(main, state, actions);
  return row;
}

function actionButton(iconName: string, label: string, className: string, action: () => void): HTMLButtonElement {
  const button = document.createElement("button");
  button.type = "button";
  button.className = `queue-action ${className}`;
  button.title = label;
  button.setAttribute("aria-label", label);
  button.append(icon(iconName));
  button.addEventListener("click", action);
  return button;
}

function icon(name: string): HTMLElement {
  const element = document.createElement("i");
  element.dataset.lucide = name;
  element.setAttribute("aria-hidden", "true");
  return element;
}

function textSpan(value: string): HTMLElement {
  const span = document.createElement("span");
  span.textContent = value;
  return span;
}

function renderScanTime(next: ManagementSnapshot): void {
  if (!next.last_scan_at) {
    elements.scanTime.textContent = "";
    return;
  }
  const date = new Date(next.last_scan_at);
  elements.scanTime.textContent = `扫描于 ${date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" })}`;
}

function updateCountdowns(): void {
  document.querySelectorAll<HTMLElement>("[data-due-at]").forEach(updateCountdownElement);
}

function updateCountdownElement(element: HTMLElement): void {
  const dueAt = Date.parse(element.dataset.dueAt ?? "");
  if (!Number.isFinite(dueAt)) {
    element.textContent = "--";
    return;
  }
  element.textContent = formatDuration(Math.max(0, Math.ceil((dueAt - Date.now()) / 1000)));
}

function formatDuration(totalSeconds: number): string {
  if (totalSeconds < 60) return `${totalSeconds} 秒`;
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  if (minutes < 60) return `${minutes} 分 ${String(seconds).padStart(2, "0")} 秒`;
  const hours = Math.floor(minutes / 60);
  return `${hours} 时 ${String(minutes % 60).padStart(2, "0")} 分`;
}

function classLabel(value: FailureClass): string {
  const labels: Record<FailureClass, string> = {
    transient: "连接中断",
    rate_limit: "请求限流",
    server: "供应商故障",
    auth_transient: "登录服务暂不可用",
    auth_limited: "登录异常",
    empty_response: "模型空回复",
    unknown: "未知故障",
    none: "未分类",
  };
  return labels[value] ?? "未知故障";
}

function actionLabel(value?: string): string {
  const labels: Record<string, string> = {
    dispatching: "准备恢复",
    goal_resume: "目标恢复",
    goal_active: "目标运行",
    conversation_continue: "对话继续",
    subagent_continue: "子 Agent 恢复",
    goal_block: "目标停止",
  };
  return value ? (labels[value] ?? "正在处理") : "正在处理";
}

function stopReasonLabel(retry: ManagedRetry): string {
  if (retry.stop_reason === "codex_not_running") {
    return "Codex 已退出，自动重试已停止";
  }
  if (retry.stop_reason === "shared_app_server_disabled") {
    return "共享后台模式已关闭，Codex 仍使用官方后台";
  }
  if (retry.stop_reason === "codex_restart_required") {
    return "重启一次 Codex 后会自动恢复";
  }
  if (retry.stop_reason === "codex_home_not_shared") {
    return "此任务不在当前 Codex 的共享会话目录中";
  }
  if (retry.stop_reason === "shared_app_server_port_conflict") {
    return "后台恢复端口被其他程序占用";
  }
  if (retry.stop_reason?.startsWith("controller_") || retry.stop_reason?.startsWith("codex_background_") || retry.stop_reason === "app_server_request_failed") {
    return "后台恢复通道连续失败，已停止空转";
  }
  if (retry.stop_reason === "goal_empty_response_limit_block_failed") {
    return `目标连续空回复达到上限，恢复已停止，但自动设为受阻失败`;
  }
  if (retry.stop_reason === "goal_empty_response_limit") {
    return `目标连续空回复达到上限，目标恢复已停止`;
  }
  if (retry.stop_reason === "consecutive_retry_limit") {
    return `无进展 ${retry.consecutive_retry}/${retry.max_consecutive_retries ?? retry.consecutive_retry} 达上限`;
  }
  return `本次恢复 ${retry.recovery_attempt}/${retry.max_recovery_attempts ?? retry.recovery_attempt} 达上限`;
}

function stoppedStateLabel(retry: ManagedRetry): string {
  switch (retry.stop_reason) {
    case "shared_app_server_disabled":
      return "共享后台已关闭";
    case "codex_not_running":
      return "Codex 已退出";
    case "codex_restart_required":
      return "等待重启 Codex";
    case "codex_home_not_shared":
      return "任务目录未接入";
    case "shared_app_server_port_conflict":
      return "恢复端口冲突";
    default:
      return "达到上限";
  }
}

function controllerStateLabel(value: string): string {
  const labels: Record<string, string> = {
    codex_restart_required: "需要重启 Codex",
    codex_not_running: "Codex 已退出，自动重试已停止",
    shared_app_server_disabled: "共享后台模式已关闭，Codex 使用官方后台",
    codex_home_not_shared: "任务目录未接入共享通道",
    shared_app_server_port_conflict: "共享端口被占用",
    codex_background_channel_unavailable: "共享通道不可用",
    codex_background_dispatch_failed: "恢复请求失败",
    controller_timeout: "恢复请求超时",
    controller_unavailable: "控制器不可用",
  };
  return labels[value] ?? value;
}

function updatePromptState(): void {
  const value = elements.retryPrompt.value;
  const count = Array.from(value).length;
  elements.promptCount.textContent = String(count);
  let error = "";
  if (!value.trim()) error = "重试文字不能为空";
  else if (count > 500) error = "最多 500 个字符";
  elements.promptError.textContent = error;
  elements.savePrompt.disabled = Boolean(error) || value === savedPrompt || busyCount > 0;
  const strategy = selectedDelayStrategy();
  const initialDelay = Number(elements.initialDelay.value);
  const maxDelay = Number(elements.maxDelay.value);
  const delayIncrement = Number(elements.delayIncrement.value);
  const recoveryAttempts = Number(elements.maxRecoveryAttempts.value);
  const consecutiveRetries = Number(elements.maxConsecutiveRetries.value);
  let settingsError = "";
  if (!Number.isInteger(recoveryAttempts) || recoveryAttempts < 1 || recoveryAttempts > 1000) {
    settingsError = "本次故障恢复上限应为 1 到 1000";
  } else if (!Number.isInteger(consecutiveRetries) || consecutiveRetries < 1 || consecutiveRetries > 100) {
    settingsError = "连续无进展重试上限应为 1 到 100";
  } else if ((strategy !== "fixed" && strategy !== "linear" && strategy !== "exponential")
    || !Number.isInteger(initialDelay) || initialDelay < 1 || initialDelay > 3600
    || !Number.isInteger(maxDelay) || maxDelay < 1 || maxDelay > 86400
    || !Number.isInteger(delayIncrement) || delayIncrement < 1 || delayIncrement > 3600) {
    settingsError = "等待时间设置超出范围";
  } else if (strategy !== "fixed" && maxDelay < initialDelay) {
    settingsError = "递增等待时，最大等待不能小于首次等待";
  }
  elements.maxDelay.disabled = strategy === "fixed";
  elements.delayIncrement.disabled = strategy !== "linear";
  elements.initialDelayLabel.textContent = strategy === "fixed" ? "固定间隔（秒）" : "首次等待（秒）";
  elements.settingsError.textContent = settingsError;
  updateDelayPreview(strategy, initialDelay, maxDelay, delayIncrement, consecutiveRetries);
  elements.saveSettings.disabled = Boolean(error) || Boolean(settingsError)
    || currentSettings() === savedSettings || busyCount > 0;
}

function currentSettings(): string {
  return JSON.stringify({
    retry_prompt: elements.retryPrompt.value,
    max_recovery_attempts: Number(elements.maxRecoveryAttempts.value),
    max_consecutive_retries: Number(elements.maxConsecutiveRetries.value),
    initial_delay_seconds: Number(elements.initialDelay.value),
    max_delay_seconds: Number(elements.maxDelay.value),
    delay_increment_seconds: Number(elements.delayIncrement.value),
    delay_strategy: selectedDelayStrategy(),
    show_notifications: elements.notificationsToggle.checked,
  });
}

function serializedSettings(value: ManagementSnapshot): string {
  return JSON.stringify({
    retry_prompt: value.retry_prompt,
    max_recovery_attempts: value.max_recovery_attempts,
    max_consecutive_retries: value.max_consecutive_retries,
    initial_delay_seconds: value.initial_delay_seconds,
    max_delay_seconds: value.max_delay_seconds,
    delay_increment_seconds: value.delay_increment_seconds,
    delay_strategy: value.delay_strategy,
    show_notifications: value.show_notifications,
  });
}

function selectedDelayStrategy(): "fixed" | "linear" | "exponential" {
  const selected = elements.delayStrategies.find((option) => option.checked)?.value;
  if (selected === "fixed" || selected === "linear") return selected;
  return "exponential";
}

function updateDelayPreview(
  strategy: "fixed" | "linear" | "exponential",
  initialDelay: number,
  maxDelay: number,
  delayIncrement: number,
  consecutiveRetries: number,
): void {
  if (!Number.isInteger(initialDelay) || initialDelay < 1 || !Number.isInteger(maxDelay) || maxDelay < 1
    || !Number.isInteger(delayIncrement) || delayIncrement < 1
    || !Number.isInteger(consecutiveRetries) || consecutiveRetries < 1) {
    elements.delayPreview.textContent = "";
    return;
  }
  const visibleCount = Math.min(consecutiveRetries, 8);
  const delays: number[] = [];
  let delay = initialDelay;
  for (let index = 0; index < visibleCount; index += 1) {
    delays.push(strategy === "fixed" ? initialDelay : Math.min(delay, maxDelay));
    if (strategy === "exponential") delay = Math.min(delay * 2, maxDelay);
    if (strategy === "linear") delay = Math.min(delay + delayIncrement, maxDelay);
  }
  const suffix = consecutiveRetries > visibleCount ? "，…" : "";
  elements.delayPreview.textContent = `等待序列：${delays.map(formatPreviewDelay).join("，")}${suffix}`;
}

function formatPreviewDelay(seconds: number): string {
  if (seconds < 60) return `${seconds} 秒`;
  if (seconds % 3600 === 0) return `${seconds / 3600} 小时`;
  if (seconds % 60 === 0) return `${seconds / 60} 分钟`;
  return `${seconds} 秒`;
}

function setBusy(active: boolean): void {
  busyCount = Math.max(0, busyCount + (active ? 1 : -1));
  const busy = busyCount > 0;
  elements.refreshButton.disabled = busy;
  elements.refreshButton.classList.toggle("is-spinning", busy);
  elements.pauseToggle.disabled = busy || !snapshot;
  elements.sharedAppServerToggle.disabled = busy || !snapshot;
  document.querySelectorAll<HTMLButtonElement>(".queue-action").forEach((button) => {
    button.disabled = busy;
  });
  updatePromptState();
}

async function callTool(name: string, args: Record<string, unknown> = {}, quiet = false): Promise<void> {
  if (!app) {
    showNotice("管理面板尚未连接", true);
    return;
  }
  if (!quiet) setBusy(true);
  try {
    const result = (await app.callServerTool({ name, arguments: args })) as ToolResult;
    const next = extractSnapshot(result);
    if (next) render(next);
    else if (result.isError) throw new Error(result.content?.find((item) => item.text)?.text ?? "操作失败");
  } catch (error) {
    if (name === "set_shared_app_server_enabled" && snapshot) {
      elements.sharedAppServerToggle.checked = snapshot.shared_app_server_enabled;
    }
    if (!quiet) showNotice(error instanceof Error ? error.message : "操作失败", true);
  } finally {
    if (!quiet) setBusy(false);
  }
}

async function runThreadAction(name: "retry_now" | "cancel_retry" | "restart_retry", threadId: string): Promise<void> {
  await callTool(name, { thread_id: threadId });
  window.setTimeout(() => void callTool("get_auto_retry_status"), 1200);
}

function showNotice(message: string, isError: boolean): void {
  window.clearTimeout(noticeTimer);
  elements.notice.textContent = message;
  elements.notice.classList.toggle("is-error", isError);
  elements.notice.hidden = false;
  noticeTimer = window.setTimeout(() => {
    elements.notice.hidden = true;
  }, isError ? 7000 : 4200);
}

elements.refreshButton.addEventListener("click", () => void callTool("get_auto_retry_status"));
elements.pauseToggle.addEventListener("change", () => void callTool("set_auto_retry_paused", { paused: !elements.pauseToggle.checked }));
elements.sharedAppServerToggle.addEventListener("change", () => {
  const enabled = elements.sharedAppServerToggle.checked;
  elements.sharedAppServerDescription.textContent = enabled
    ? "正在使用插件拥有且已通过健康检查的后台"
    : "默认关闭，不影响 Codex 官方后台";
  void callTool("set_shared_app_server_enabled", { enabled });
});
elements.retryPrompt.addEventListener("input", updatePromptState);
elements.maxRecoveryAttempts.addEventListener("input", updatePromptState);
elements.maxConsecutiveRetries.addEventListener("input", updatePromptState);
for (const option of elements.delayStrategies) option.addEventListener("change", updatePromptState);
elements.initialDelay.addEventListener("input", updatePromptState);
elements.maxDelay.addEventListener("input", updatePromptState);
elements.delayIncrement.addEventListener("input", updatePromptState);
elements.notificationsToggle.addEventListener("change", updatePromptState);
elements.savePrompt.addEventListener("click", () => void callTool("set_retry_prompt", { prompt: elements.retryPrompt.value }));
elements.saveSettings.addEventListener("click", () => void callTool("set_retry_settings", JSON.parse(currentSettings()) as Record<string, unknown>));

refreshIcons();
window.setInterval(updateCountdowns, 1000);
window.setInterval(() => {
  if (app && busyCount === 0) void callTool("get_auto_retry_status", {}, true);
}, 5000);

if (new URLSearchParams(window.location.search).has("preview")) {
  render(previewSnapshot());
} else {
  app = new App({ name: "Codex Auto Retry", version: "0.7.3" });
  app.onerror = (error) => showNotice(error instanceof Error ? error.message : "连接失败", true);
  app.onhostcontextchanged = handleHostContext;
  app.ontoolresult = (result) => {
    const next = extractSnapshot(result as ToolResult);
    if (next) render(next);
  };
  app.connect()
    .then(() => {
      const context = app?.getHostContext();
      if (context) handleHostContext(context);
      return callTool("get_auto_retry_status");
    })
    .catch((error) => showNotice(error instanceof Error ? error.message : "连接失败", true));
}

function previewSnapshot(): ManagementSnapshot {
  const now = Date.now();
  return {
    version: "0.7.3",
    running: true,
    heartbeat_stale: false,
    paused: false,
    shared_app_server_enabled: false,
    retry_prompt: "继续",
    max_recovery_attempts: 15,
    max_consecutive_retries: 5,
    initial_delay_seconds: 5,
    max_delay_seconds: 300,
    delay_increment_seconds: 2,
    delay_strategy: "exponential",
    controller_state: "ready",
    show_notifications: true,
    now: new Date(now).toISOString(),
    last_scan_at: new Date(now - 1300).toISOString(),
    pending_retries: 2,
    active_retries: 1,
    stopped_retries: 1,
    watched_roots: 3,
    retries: [
      {
        thread_id: "019f9d5d-9c82-75b1-b7c0-20a658af0423",
        label: "任务 019f9d5d",
        state: "running",
        class: "server",
        seconds_remaining: 0,
        recovery_attempt: 1,
        max_recovery_attempts: 15,
        consecutive_retry: 1,
        max_consecutive_retries: 5,
        action: "goal_resume",
        can_retry_now: false,
        can_cancel: false,
        can_restart: false,
      },
      {
        thread_id: "019f9d5d-9c82-75b1-b7c0-20a658af0424",
        label: "任务 019f9d5e",
        state: "pending",
        class: "rate_limit",
        due_at: new Date(now + 42_000).toISOString(),
        seconds_remaining: 42,
        recovery_attempt: 4,
        max_recovery_attempts: 15,
        consecutive_retry: 1,
        max_consecutive_retries: 5,
        can_retry_now: true,
        can_cancel: true,
        can_restart: false,
      },
      {
        thread_id: "019f9d5d-9c82-75b1-b7c0-20a658af0425",
        label: "任务 019f9d5f",
        state: "pending",
        class: "transient",
        due_at: new Date(now + 126_000).toISOString(),
        seconds_remaining: 126,
        recovery_attempt: 3,
        max_recovery_attempts: 15,
        consecutive_retry: 2,
        max_consecutive_retries: 5,
        can_retry_now: true,
        can_cancel: true,
        can_restart: false,
      },
      {
        thread_id: "019f9d5d-9c82-75b1-b7c0-20a658af0426",
        label: "任务 019f9d60",
        state: "stopped",
        class: "server",
        seconds_remaining: 0,
        recovery_attempt: 15,
        max_recovery_attempts: 15,
        consecutive_retry: 5,
        max_consecutive_retries: 5,
        can_retry_now: false,
        can_cancel: false,
        can_restart: true,
      },
    ],
  };
}
