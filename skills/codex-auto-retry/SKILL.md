---
name: codex-auto-retry
description: Inspect, configure, install, repair, or remove Codex Auto Retry. Use when the user asks about automatic retries, tray controls, countdowns, queue state, retry limits, retry text, pause controls, goal recovery, watchdog status, retry logs, supported failure types, startup behavior, installation, repair, or removal.
---

# Codex Auto Retry

This plugin includes a local Windows watchdog and a small sign-in supervisor.
Once installed, the supervisor starts the watchdog globally at Windows sign-in
and restarts it after unexpected exits with bounded backoff. The user does not need to invoke this
plugin in each Codex task. Its MCP management panel is optional and does not
need to remain open for retries to work.

The watchdog worker owns a Windows notification-area icon. Double-click opens
the graphical settings window; right-click shows status, pause/resume, settings,
and exit. This is not a second watchdog or a separate retry engine.

## Recovery Behavior

- Goal mode: rejoin the exact failed task through Codex App's existing
  background connection and activate its native goal only when the blocked
  state is attributable to the same provider failure. Codex creates the goal
  continuation turn itself. An immediate native turn after an empty reply is
  adopted into the same recovery chain, so its counters are not reset as
  manual work. At either limit, the goal is changed to `blocked` and the panel
  reports that repeated empty replies stopped it.
- A user or AI pause, including a goal waiting for review, overrides goal
  recovery. Pauses during the failed turn, countdown, or dispatch cancel that
  retry. Only a later explicit `active` goal update clears the goal hold.
- If a held goal predates a later externally started conversation turn, retry
  that turn as a silent normal continuation while leaving the goal unchanged.
  Recheck the same held goal revision immediately before `turn/start`; a pause
  or goal change at or after the turn start must fail closed.
- Completed, usage-limited, budget-limited, missing, and unknown goal states
  must fail closed; never turn them into a normal-conversation continuation.
- Normal conversation: start a silent empty-input continuation in the exact
  same task without touching the composer, adding a visible user message, or
  replaying the failed turn. Use the configured fallback text only when Codex
  explicitly rejects empty-input turns.
- Preserve each task's latest model, workspace, reasoning, personality,
  approval, and effective permission settings during background resume.
- Never open a task link, focus Codex, switch the task currently displayed,
  launch `codex exec resume`, or create a hidden external Codex task.
- If one target task is already active, delay only that task. Other failed
  tasks retain their own queue entries and can retry independently.
- For an internal subagent empty reply, inject one deterministic recovery event
  into its parent and continue the exact existing child thread. If the parent is
  unloaded, restore it with its own persisted task settings before injection.
  The watchdog is the sole wake-up owner for that event: never spawn a
  replacement, replay the failed turn, or start a second child turn while the
  original child is active. Other subagent failure categories remain owned by
  the parent workflow.
- Count recovery only when the App-created `task_started` ID has a matching
  successful `task_complete`.
- Stop at either independent safety limit: `max_recovery_attempts` bounds all
  automatic attempts in one fault (15 by default, 1-1000), while
  `max_consecutive_retries` bounds retries without a visible assistant reply or
  completed tool result (5 by default, 1-100). Visible progress resets only the
  second count; success or a new user turn resets both. Controller failures
  consume neither budget. A closed Codex App immediately stops the affected
  recovery with `codex_not_running`; restart it manually from the panel after
  Codex is open again. Other controller failures stop after three consecutive
  failures by default instead of refreshing the countdown forever.
  `codex_restart_required` means the user must restart Codex once after the
  optional shared mode is enabled so Desktop inherits the shared endpoint.

## Embedded Management

When the MCP tools are available, use `get_auto_retry_status` to display the
embedded panel and return current state. The panel shows all pending and active
tasks, live countdowns, pause state, shared-backend opt-in state, and the
compatibility fallback text.

- Use `set_shared_app_server_enabled` only when the user explicitly requests
  silent shared-backend recovery. Enabling runs ownership and health checks;
  failure leaves Codex's environment unchanged. Disabling restores only the
  plugin-owned endpoint and stops only the plugin-owned server.

- Use `set_retry_prompt` to change only the fallback text. The default is
  `继续`, the maximum is 500 characters, and changes apply without restarting
  the watchdog. Normal retries do not send this text when silent continuation
  is supported.
- Use `set_retry_settings` to change the fallback text, both retry limits,
  fixed, linear, or doubling waits, first/fixed delay, linear increment,
  maximum delay, and the watchdog's retry-limit notification.
- `show_notifications` controls only the watchdog alert shown when a retry limit
  is reached. Codex App's `ChatGPT finished a turn` popup is emitted before an
  empty response can be classified, so it cannot be selectively withdrawn. Use
  Codex **Settings > General > Notifications > Turn completion notifications >
  Never** to suppress all completion popups while leaving Codex permission and
  question notifications independently configurable.
- Goal recovery never sends the fallback text; it continues to activate the
  native Codex goal.
- Use `set_auto_retry_paused` to pause or resume new dispatches. Do not claim
  that pausing terminates a retry that already started.
- Use `retry_now` or `cancel_retry` only with a task ID returned in the pending
  queue. Cancel cannot undo a retry that already started.
- Use `restart_retry` only for a task reported as stopped at its retry limit.
  It starts a fresh attempt budget and requests an immediate retry.
- Never open the panel automatically, navigate to another task, or focus Codex.

## Status

Run:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$env:USERPROFILE\plugins\codex-auto-retry\scripts\status.ps1"
```

Report whether the process is running, its version and PID, pause state, MCP
server installation, startup mode, endpoint presence, shared-server state, the
last scan time, pending and active retry counts, and the privacy-safe log path.
Do not read Codex conversation content while checking status. Treat
`StartupMode=run` as an old entry that should be repaired by the installer.
Treat `RuntimePathRedirected=true` or
`runtime_path_redirected` as not installed: a package `LocalCache` copy is not
visible to Explorer or Windows sign-in even when the Codex tool process can
read it.

## Install Or Repair

Run the build, shared-server/environment smoke tests, isolated protocol tests,
and installer:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$env:USERPROFILE\plugins\codex-auto-retry\scripts\build.ps1"
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$env:USERPROFILE\plugins\codex-auto-retry\scripts\mcp-smoke-test.ps1"
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$env:USERPROFILE\plugins\codex-auto-retry\scripts\tray-smoke-test.ps1"
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$env:USERPROFILE\plugins\codex-auto-retry\scripts\path-safety-smoke-test.ps1"
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$env:USERPROFILE\plugins\codex-auto-retry\scripts\supervisor-smoke-test.ps1"
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$env:USERPROFILE\plugins\codex-auto-retry\scripts\status-smoke-test.ps1"
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$env:USERPROFILE\plugins\codex-auto-retry\scripts\startup-manager-smoke-test.ps1"
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$env:USERPROFILE\plugins\codex-auto-retry\scripts\smoke-test.ps1"
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$env:USERPROFILE\plugins\codex-auto-retry\scripts\app-server-protocol-smoke-test.ps1"
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$env:USERPROFILE\plugins\codex-auto-retry\scripts\empty-response-protocol-smoke-test.ps1"
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$env:USERPROFILE\plugins\codex-auto-retry\scripts\install.ps1"
```

Builds and isolated tests may run from a Codex task. The final `install.ps1`
must run from Windows Explorer or a normal desktop PowerShell when the status
reports runtime path redirection. The installer deliberately stops before
changing the host runtime, startup entry, or environment in that situation.
Never report the redirected `OpenAI.Codex_*\LocalCache` copy as installed and
never compensate by publishing a shared endpoint that points at it.

The MCP smoke test uses isolated local data and verifies all management tools,
the embedded HTML resource, prompt changes, pause state, and atomic control
commands. The tray smoke test verifies the native notification-area window,
visible graphical settings window, heartbeat, and clean final status without
touching Codex. The shared-server smoke test uses two WebSocket clients and a
local fake provider to prove that a watchdog-started retry is visible to the
simulated Desktop client. The environment test uses a random test-only user
variable and restores it. The isolated app-server tests use temporary
`CODEX_HOME` directories; the empty-response test also uses a local fake
provider and no real account.
Neither test uses Codex App UI. The installer preserves and migrates `config.json`, replaces both
executables, leaves `CODEX_APP_SERVER_WS_URL` untouched by default (or sets it
only after the explicit shared-mode health gate), registers per-user Windows
startup in `supervise` mode rather than the legacy direct `run` mode, points the plugin at the direct installed MCP
executable, starts both GUI-subsystem processes without a visible console, and
launches the shared Codex app-server with one hidden inherited console so its
Playwright, Node REPL, code-mode, and shell descendants do not create separate
Windows Terminal windows. An older detached server is replaced automatically
after Codex fully exits, and the installer verifies the watchdog heartbeat.
Restart Codex once only after enabling the optional shared mode or changing its
launch mode; then open a new task so Codex discovers the updated MCP tools and
panel.

## Startup Manager And Remove

The release includes `启动管理器.cmd`. Use it when Windows Explorer does not
show the full `HKCU\...\Run` command: it reports the exact startup entry,
supervisor/worker process chain, heartbeat, shared mode, endpoint, and stale
state, and provides start, stop, enable, disable, safe-disable, and uninstall
actions. `安全停用.cmd` performs only the break-glass shared-backend cleanup.
Both tools operate only on paths and state records owned by this plugin.

Complete removal still uses the release's `卸载.cmd` or
`uninstall-release.ps1`; the graphical manager's default uninstall preserves
runtime settings/state/logs, while its destructive option asks for confirmation.
The command-line destructive path requires `-RemoveData -NoPrompt` explicitly.

## Remove

Only remove the watchdog when the user explicitly asks:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$env:USERPROFILE\plugins\codex-auto-retry\scripts\uninstall.ps1"
```

## Privacy Boundary

The event scanner parses lifecycle records plus privacy-bounded `response_item`
metadata. For
completions it retains only whether `last_agent_message` was known and non-empty,
never the message text. For aborts it retains only the turn ID, reason, and
timestamp. Goal parsing retains only the target task ID, status, and lifecycle
timestamps, and routes by the payload task ID even when Codex stores the event
in another rollout. It does not retain the goal objective or inspect
conversation text for intent. Progress records retain only a boolean. A
dedicated user-input lifecycle record contributes only a content-free marker
for the currently started turn; user-role context items from automatic goal
turns are ignored. The one text exception is the watchdog's own fixed,
schema-validated subagent recovery event, from which only parent, child, and
deterministic event IDs are retained; all other message/tool-result content
remains undecoded and unsaved. Before dispatch, a
separate reader extracts only the required execution settings from the latest `turn_context` and
`thread_settings_applied` records. It does not retain, forward, or log prompts,
developer instructions, assistant messages, tool arguments, tool results, API
keys, or response bodies.

The controller connects only to Codex App on a loopback endpoint and sends a
fixed structured recovery program. It does not read drafts or automate the
window, mouse, keyboard, clipboard, composer, or task navigation. It never logs
the fallback prompt or app-server error bodies.

## Retry Policy

- Bounded by both the per-fault recovery budget and the consecutive no-progress
  guard: network
  failures, timeouts, HTTP 408/425/429 and 5xx responses, the structured CC
  Switch `cc_switch_upstream_error` wrapper when `upstream_status=400` and the
  cause is `Upstream request failed`, interrupted streams, HTTP 200 completions
  with no final model reply, temporary provider authentication outages,
  cooldown, and provider overload.
- Repeated empty replies from an active goal share one chain. Exhaustion leaves
  a visible stopped entry and changes the native goal from `active` to
  `blocked`; failure of that local control action uses the configured controller
  limit (three by default) without changing provider counters, then remains
  stopped with an explicit goal-block failure reason.
- Lower limited budgets may apply to generic 401/403 authentication failures
  and unknown errors.
- No retry: user cancellation, invalid request or payload, ordinary HTTP 400/404
  errors, missing model,
  context limit, policy, approval, or permission failures.

If a permanent login failure remains after the limited retry budget, explain
that re-authentication is required; do not weaken authentication or security.

## Shared Backend Safety

Shared app-server recovery is opt-in. Treat `shared_app_server_enabled=false`
as the safe default and never add `CODEX_APP_SERVER_WS_URL` to the user's
global environment during an ordinary install. The optional enable operation
must verify loopback ownership, a WebSocket health handshake, executable and
plugin version, and the recorded live PID before publishing the endpoint.
Installation and update are transactional; a failed health check restores the
previous binary, config, endpoint, and startup entry.

For a startup emergency, run `scripts/safe-disable.ps1` directly. It is
watchdog-independent, persists shared mode disabled, stops only processes
proven to belong to this plugin, restores only the endpoint in
`environment-backup.json`, removes only the plugin's startup entry, broadcasts
the environment change, and never removes chat data or a user-owned
`CODEX_API_KEY`. A worker restart adopts a healthy owned endpoint. If a live
Codex Desktop is still using the shared server, cleanup is deferred and the
worker retries it after Desktop closes; dead owned state is removed immediately.
Stale PID or heartbeat data must be shown as `后台服务未运行`, never as healthy
`running`.

If `config.json` is damaged, process-boundary cleanup does not replace it. The
watchdog uses the ownership-verified `shared-server.json` record to recover the
actual endpoint and Codex home, restores the environment backup, stops only the
matching owned server, and exits so the configuration can be repaired
explicitly.
