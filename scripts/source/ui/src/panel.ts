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
  | "unknown"
  | "none";

type ManagedRetry = {
  thread_id: string;
  label: string;
  state: "pending" | "starting" | "running" | "stopped";
  class: FailureClass;
  due_at?: string;
  seconds_remaining: number;
  attempt: number;
  max_attempts?: number;
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
  retry_prompt: string;
  max_retry_attempts: number;
  initial_delay_seconds: number;
  max_delay_seconds: number;
  show_notifications: boolean;
  now: string;
  last_scan_at?: string;
  pending_retries: number;
  active_retries: number;
  stopped_retries: number;
  watched_roots: number;
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
  pauseDescription: required<HTMLElement>("pause-description"),
  retryPrompt: required<HTMLTextAreaElement>("retry-prompt"),
  promptCount: required<HTMLElement>("prompt-count"),
  promptError: required<HTMLElement>("prompt-error"),
  savePrompt: required<HTMLButtonElement>("save-prompt"),
  maxAttempts: required<HTMLInputElement>("max-attempts"),
  initialDelay: required<HTMLInputElement>("initial-delay"),
  maxDelay: required<HTMLInputElement>("max-delay"),
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
  if (!keepSettingsDraft) {
    elements.retryPrompt.value = next.retry_prompt;
    elements.maxAttempts.value = String(next.max_retry_attempts);
    elements.initialDelay.value = String(next.initial_delay_seconds);
    elements.maxDelay.value = String(next.max_delay_seconds);
    elements.notificationsToggle.checked = next.show_notifications;
  }
  savedSettings = serializedSettings(next);
  elements.pauseToggle.checked = !next.paused;
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
  if (next.running && next.paused) {
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
  const attempt = retry.max_attempts
    ? `第 ${retry.attempt}/${retry.max_attempts} 次`
    : `第 ${retry.attempt} 次`;
  meta.append(
    textSpan(classLabel(retry.class)),
    textSpan(attempt),
    textSpan(retry.state === "pending" ? "等待中" : retry.state === "stopped" ? "达到上限" : actionLabel(retry.action)),
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
    secondary.textContent = `${retry.attempt}/${retry.max_attempts ?? retry.attempt} 次失败`;
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
  };
  return value ? (labels[value] ?? "正在处理") : "正在处理";
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
  const initialDelay = Number(elements.initialDelay.value);
  const maxDelay = Number(elements.maxDelay.value);
  const attempts = Number(elements.maxAttempts.value);
  const settingsError = Boolean(error) || !Number.isInteger(attempts) || attempts < 1 || attempts > 20
    || !Number.isInteger(initialDelay) || initialDelay < 1 || initialDelay > 3600
    || !Number.isInteger(maxDelay) || maxDelay < initialDelay || maxDelay > 86400;
  elements.saveSettings.disabled = settingsError || currentSettings() === savedSettings || busyCount > 0;
}

function currentSettings(): string {
  return JSON.stringify({
    retry_prompt: elements.retryPrompt.value,
    max_retry_attempts: Number(elements.maxAttempts.value),
    initial_delay_seconds: Number(elements.initialDelay.value),
    max_delay_seconds: Number(elements.maxDelay.value),
    show_notifications: elements.notificationsToggle.checked,
  });
}

function serializedSettings(value: ManagementSnapshot): string {
  return JSON.stringify({
    retry_prompt: value.retry_prompt,
    max_retry_attempts: value.max_retry_attempts,
    initial_delay_seconds: value.initial_delay_seconds,
    max_delay_seconds: value.max_delay_seconds,
    show_notifications: value.show_notifications,
  });
}

function setBusy(active: boolean): void {
  busyCount = Math.max(0, busyCount + (active ? 1 : -1));
  const busy = busyCount > 0;
  elements.refreshButton.disabled = busy;
  elements.refreshButton.classList.toggle("is-spinning", busy);
  elements.pauseToggle.disabled = busy || !snapshot;
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
elements.retryPrompt.addEventListener("input", updatePromptState);
elements.maxAttempts.addEventListener("input", updatePromptState);
elements.initialDelay.addEventListener("input", updatePromptState);
elements.maxDelay.addEventListener("input", updatePromptState);
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
  app = new App({ name: "Codex Auto Retry", version: "0.4.3" });
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
    version: "0.4.3",
    running: true,
    heartbeat_stale: false,
    paused: false,
    retry_prompt: "继续",
    max_retry_attempts: 5,
    initial_delay_seconds: 5,
    max_delay_seconds: 300,
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
        attempt: 1,
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
        attempt: 2,
        max_attempts: 5,
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
        attempt: 3,
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
        attempt: 5,
        max_attempts: 5,
        can_retry_now: false,
        can_cancel: false,
        can_restart: true,
      },
    ],
  };
}
