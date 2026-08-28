# Codex Auto Retry

Codex Auto Retry is a personal Codex plugin with a local Windows watchdog. Once
installed, the watchdog runs globally at Windows sign-in; the plugin does not
need to be mentioned in each Codex task. The plugin also exposes an embedded
Codex management panel and a Windows notification-area controller. Retry
behavior remains independent of whether either settings surface is open.

## Behavior

- Watches the default Codex session store and optional Cockpit-managed Codex
  instances for recoverable provider failures.
- Rejoins the exact failed task through the Codex App process that is already
  running. Codex Desktop and the watchdog are two clients of one local shared
  app-server, so recovery does not open a task link, focus Codex, change the
  task currently on screen, or create a hidden `codex exec resume` task.
- Restores the failed task with its latest working directory, workspace roots,
  model and provider, service tier, reasoning settings, personality, approval
  routing, and effective permission profile instead of applying the App's
  defaults.
- In goal mode, uses Codex's native goal state and activates only a blocked
  goal that can be attributed to the same provider failure. Codex then creates
  the continuation turn itself. If an active goal creates another turn
  immediately after an empty reply, that turn is adopted into the same bounded
  recovery chain instead of being mistaken for new manual work.
- Treats a user or AI pause, including a goal waiting for review, as
  authoritative for goal recovery. A pause during the failed turn, countdown,
  or controller startup cancels recovery; only an explicit later `active` goal
  update clears the goal hold. If the pause predates a later user-started
  conversation turn, a provider failure in that later turn may be silently
  continued while the goal remains paused and unchanged.
- Never converts completed, usage-limited, budget-limited, or unknown goal
  states into a normal-conversation `continue` turn.
- In a normal conversation, starts an empty-input continuation in that same
  task. The original request and completed tool results stay in context, while
  no new user-message bubble is added and the composer draft is untouched.
- Uses the configured retry text only as a narrow compatibility fallback when
  Codex explicitly rejects empty-input turns. It never rolls back and resends
  the failed turn, which avoids intentionally replaying completed side effects.
- If the failed task is already running, keeps its retry queued and tries again
  later instead of canceling it.
- Keeps separate retry state for every task and can dispatch up to four due
  tasks independently by default.
- For an empty reply from an internal subagent, appends one deterministic
  recovery event to its parent and silently continues the exact existing child
  thread. An unloaded parent is first restored with its own persisted task
  settings, rather than current App defaults. The parent receives the recovery
  event, while the watchdog remains the sole wake-up owner for that event. The
  event explicitly forbids a replacement child; live child state, persisted
  notification acknowledgement, and turn correlation prevent a duplicate
  continuation or duplicate Agent creation. Other child failures remain owned
  by the parent workflow.
- Tracks two independent safety limits. `本次故障恢复` counts every automatic
  recovery in one outage (15 by default, configurable from 1 to 1000).
  `连续无进展` counts retries that produce neither a visible assistant reply
  nor a completed tool result (5 by default, configurable from 1 to 100). A
  successful completion or a new user turn clears both; visible progress clears
  only the consecutive no-progress count.
- When an active goal reaches either limit through repeated empty replies, the
  watchdog retains the exhausted entry, changes that goal to `blocked`, and
  shows `目标连续空回复达到上限，目标恢复已停止`. Controller failures retry
  separately and do not consume another provider attempt; repeated local
  control failure eventually stops with its own explicit reason.
- Supports fixed, linear, or doubling delays capped at a configurable maximum.
  Linear waits add a configurable number of seconds each time. Increasing
  waits follow the consecutive no-progress count, so visible progress starts
  the delay sequence over. It correlates the new
  `task_started` turn ID with its matching `task_complete`. An unrelated
  successful turn cannot falsely mark a retry as recovered.

The watchdog retries network failures, timeouts, rate limits, HTTP 5xx
responses, the structured CC Switch `cc_switch_upstream_error` wrapper when
its `upstream_status` is 400 and the cause is `Upstream request failed`,
interrupted streams, successful completions with no final model reply, and
temporarily unavailable authentication services within the
configured dual limits. Ambiguous or
persistent authentication failures may have a lower safety limit. Unknown
provider failures keep their separate recovery safety budget but still use the
configured consecutive no-progress limit. If Codex App exits, the watchdog
stops the affected retry immediately without consuming another provider
attempt, so it does not keep a countdown running against a closed application.
The stopped task is shown with its reason and can be restarted manually after
Codex is open again. Other local controller failures stop after three consecutive
failures by default instead of refreshing a countdown forever. A task that
still uses Codex's old per-process transport stops with
`codex_restart_required` and asks for one Codex restart.
User cancellation, invalid requests, ordinary HTTP 400/404 errors, missing
models, context length errors, policy failures, permission failures, and
approval failures are not retried.

Runtime state is written atomically. A temporary Windows sharing violation from
an indexer, security scanner, or settings reader is retried for several seconds;
if it still cannot be replaced, the watchdog keeps its in-memory state, reports
`state_write_deferred`, and retries persistence on the next scan instead of
exiting and removing the tray icon.

## Windows Tray Controller

The watchdog owns one notification-area icon; it does not install or start a
second background application. Hovering the icon shows whether retry is
running, paused, waiting, active, or stopped, including the nearest live
countdown. If Windows Explorer restarts, the watchdog automatically registers
the icon again and restores its current state. Double-clicking opens the
graphical settings window. The right-click menu opens settings, pauses or
resumes dispatch, and exits the watchdog.

The graphical window shows current waiting and active tasks, plus a short-lived
exhausted record immediately after a limit is reached, using only privacy-safe
task IDs. Older stopped records remain durable but are not counted as current
retry work. It edits the fallback retry text, both retry limits,
fixed, linear, or doubling waits, first/fixed delay, linear increment, maximum delay, and the watchdog's
retry-limit notification.
An exhausted task can be restarted with a fresh attempt budget. These settings
are shared with the embedded Codex panel and take effect without restarting the
watchdog.

## Management Panel

Ask Codex to `打开 Codex Auto Retry 管理面板` or select the matching plugin
starter prompt. The panel opens inside the current Codex task and shows:

- the watchdog state, watched locations, and last scan;
- every pending or active retry, including a live countdown per task;
- controls to retry a pending task now or cancel it before it starts;
- exhausted tasks and a control to restart their attempt budget;
- a persistent pause switch for new retry dispatches; and
- the editable fallback retry text, limited to 500 characters;
- the per-fault recovery limit, consecutive no-progress limit, and wait strategy; and
- the watchdog retry-limit notification preference.

Normal conversations use silent continuation first. The default fallback text
is `继续` and is used only if the installed Codex version explicitly rejects
empty-input turns. Goal mode never uses this text: it still activates Codex's
native interrupted goal. Saving the text takes effect without restarting the
watchdog.

The panel refreshes approximately every five seconds and computes countdowns
locally between refreshes. It never opens itself, focuses Codex, or navigates to
another task. Closing the panel has no effect on the global watchdog.

## Safety And Privacy

The event scanner accepts lifecycle records plus privacy-bounded progress and
correlation markers. For a completion it retains only
whether `last_agent_message` was present and non-empty, never its contents. A
completion with no explicit error and no final reply is treated as a temporary
empty-response failure. An abort remains authoritative even if a delayed
completion for that same turn is written afterward. For goal updates it retains only the
target task ID, status, and lifecycle timestamps; the event can be routed correctly even
when Codex persists it in another task's rollout. It never reads the goal
objective or searches conversation text for words such as "review". An
assistant message or completed tool-result item contributes only a boolean
"this retry made visible progress" marker; its content is never decoded or
stored. A dedicated `user_message` lifecycle record contributes only a
content-free "explicit user input" marker associated with the currently
started turn, so real user work supersedes an adopted native goal turn. User
role context items written by an automatic goal turn are ignored. The only
message text parsed is the watchdog's own fixed, schema-checked subagent
recovery marker; it retains only parent, child, and deterministic event IDs.
Immediately
before recovery, a separate settings reader decodes only an allowlisted subset
of the latest `turn_context` and
`thread_settings_applied` records: working directory, workspace roots, model
and provider, service tier, reasoning effort and summary, personality, approval
policy and reviewer, and effective permission mode. It discards every other
field and never forwards or logs developer instructions, conversation
messages, assistant output text, tool input, tool output, credentials, provider
URLs, or response bodies.

Optional shared recovery uses one local app-server bound only to `127.0.0.1`.
It is disabled by default: an ordinary install leaves
`CODEX_APP_SERVER_WS_URL` untouched, so Codex keeps its official backend. When
the user explicitly enables shared mode, the plugin first validates the
plugin-owned server and only then sets the current-user endpoint. At worker
startup it temporarily detaches a prior owned endpoint until the replacement
server passes its health check. When a worker or supervisor exits, it restores
the prior endpoint and removes stale owned state before Codex can inherit a
dead port. The watchdog uses only the structured `thread/read`, `thread/resume`,
`thread/inject_items`, `thread/goal/get`, `thread/goal/set`, and `turn/start`
methods used by Codex. It validates ownership of the loopback server before
using it and never routes local recovery traffic through an HTTP proxy. It
does not automate the mouse, keyboard, clipboard, composer, window focus, or
task navigation.

## Installation And Maintenance

End users can use the self-contained Windows x64 release ZIP. After extracting
it, double-click `安装.cmd`; the installer verifies every packaged file, locates
the Codex App-bundled CLI, installs the personal plugin, registers current-user
startup, starts the watchdog, and verifies both Codex registration and the
runtime heartbeat. It requires neither administrator rights nor Go or Node.js.
`卸载.cmd` removes the active integration while preserving retry configuration
and state by default.

`启动管理器.cmd` opens a standalone startup manager through a detached
Windows Script Host launcher, so double-clicking it does not leave a console
window in front of the manager. It displays the exact
startup command, watchdog process and heartbeat, shared-backend state, and
endpoint status. It can enable/disable startup, start/stop the service, safely
disable the shared backend, or uninstall the integration. `安全停用.cmd` is a
one-click break-glass action that disables shared mode and restores the official
Codex backend. These tools do not require the Codex management panel to be open.

The supervisor, watchdog worker, and MCP management server are installed under
`%LOCALAPPDATA%\CodexAutoRetry`. The supervisor starts the worker and keeps it
available after an unexpected worker exit. The MCP server starts on demand through Codex and exits with
its Codex connection. The release installer gives Codex the executable's direct
absolute path, and both plugin binaries use the Windows GUI subsystem. The
watchdog also starts the shared Codex app-server inside one hidden inherited
console, so Playwright, Node REPL, code-mode, and shell subprocesses reuse that
hidden console instead of opening separate Windows Terminal windows. An upgrade
from the older detached launch waits until Codex is fully closed, terminates only
that owned app-server process tree, and starts the corrected server before the
next launch. The Windows sign-in entry always starts the lightweight
`supervise` command rather than the worker's old direct `run` command, so a
worker exit cannot leave a published endpoint behind. Runtime state, heartbeat,
configuration, controls, and
privacy-safe logs remain in the same local directory. Plugin management
commands live in `skills/codex-auto-retry/SKILL.md`.

The installer refuses to overwrite a different existing
`CODEX_APP_SERVER_WS_URL`, records the environment value it owns, and restores
the prior user value on uninstall. If Codex was open during the first install
or the hidden-console launch upgrade, fully exit and restart Codex once. The
panel and tray show that requirement instead of
repeatedly pretending to retry.

When shared mode is already enabled, each controller readiness check also
reconciles the endpoint before inspecting the Desktop transport. If the owned
endpoint is missing, the watchdog restores it only after re-validating the
plugin-owned server and broadcasts the Windows environment change. A different
user value is never overwritten; shared mode fails open with an explicit
environment-conflict status instead of repeatedly asking for a restart.

After installing or updating the plugin, open a new Codex task so the updated
MCP tools and embedded panel are discovered. The background watchdog itself is
restarted and verified by the installer immediately.

Do not run the final runtime installation from a Codex tool shell when Windows
redirects `%LOCALAPPDATA%` into the Codex package `LocalCache`. That redirected
copy is visible to the tool process but not to Explorer or Windows sign-in, so
it is not a valid global watchdog installation. Both the release deployer and
runtime installer detect this condition before changing plugin, startup, or
environment state and tell the user to run the installer from Explorer or a
normal desktop PowerShell. `scripts/status.ps1` reports
`RuntimePathRedirected=true` and `runtime_path_redirected` instead of claiming
that the redirected copy is installed.

Maintainers build a release with `scripts/build-release.ps1` and verify the
resulting archive with `scripts/release-test.ps1`. Release output must be kept
outside the plugin source tree; the builder accepts `-OutputDirectory` for that
purpose.

See `docs/project-map.md` for module ownership and `docs/architecture.md` for
the recovery state machine, safety boundaries, and verification model.

## Limitations

A permanently expired or revoked login still requires authentication. Codex
App must be running for a retry to start. If an App update removes or changes
the local app-server protocol, recovery fails closed at the bounded controller
limit and remains visibly restartable; it never falls back to opening or
focusing a task.

The tray controller requires Windows 10 or 11. Closing it through the tray menu
stops automatic retry intentionally until the next Windows sign-in or
reinstall/start; the supervisor honors that one-shot stop marker.

If the failed rollout does not contain valid settings records, recovery also
remains queued instead of resuming the task with replacement defaults.

The `ChatGPT finished a turn` popup is emitted by Codex App before this watchdog
can classify a completion as an empty-response failure. It therefore cannot be
selectively withdrawn only for false completions. Codex App's own **Settings >
General > Notifications > Turn completion notifications > Never** option is the
reliable way to suppress it, but that option also suppresses legitimate turn
completion notifications. Permission and question notifications remain
separate Codex settings. The notification checkbox in this plugin controls only
the watchdog alert shown when a retry limit is reached.

Third-party license notices for the WebSocket transport, MCP SDKs, and embedded
panel libraries are in
`THIRD_PARTY_NOTICES.md`.

## Fail-Open Shared Backend Safety

The shared Codex app-server is opt-in. A fresh install defaults
`shared_app_server_enabled` to `false` and does not write the global
`CODEX_APP_SERVER_WS_URL`, so a broken plugin cannot redirect Codex away from
its official backend. The watchdog reports that recovery is disabled and
stops queued retries with a visible reason instead of spinning forever.

Enable the shared mode only after the health check passes. The embedded
management panel and the tray settings window can enable it explicitly; the
management tool is `set_shared_app_server_enabled`, and the Windows installer
accepts `-EnableSharedAppServer`. All paths require a loopback endpoint, a successful
WebSocket handshake, a versioned executable, and a process whose path and
command line match the plugin-owned state. `CODEX_API_KEY` is never read for
mutation and is never removed.

The default loopback port is `49621`. Before Codex is started, the watchdog
actually binds the port once to detect Windows-excluded ranges and occupied
ports. The settings window reports those two cases separately and keeps the
shared mode disabled when the check fails.

An install or upgrade without `-EnableSharedAppServer` explicitly puts the
runtime back into fail-open mode. It restores the recorded endpoint, removes
dead plugin-owned shared-server state, and also
clears a legacy endpoint only when the old plugin state proves ownership;
unrelated user values are preserved.

If an explicitly enabled shared backend later fails during watchdog startup,
the watchdog performs the same fail-open transition: it clears its owned
endpoint, disables shared mode, and leaves Codex on its official backend.

The shared launch mirrors the bundled Desktop `codex_app` MCP definition so the
WebSocket app-server sees the same server shape as the official Desktop launch.
The plugin reads that JSON definition without editing it, removes only the
plugin-marketplace `type` field that older app-server versions do not accept,
normalizes relative paths, and records a hash so a Codex update can trigger an
owned-server migration. If the server still reports an invalid `codex_app`
transport, the watchdog disables shared mode, restores the official endpoint,
and stops the affected retry rather than leaving Codex connected to a broken
local backend.

The tray form stays responsive while this check runs and stops waiting after
35 seconds. A failed or timed-out check leaves Codex on its previous backend
and does not save the shared-mode switch.

If startup or an upgrade is broken, run `scripts/safe-disable.ps1`. This
break-glass script is independent of the watchdog: it removes only the plugin's
startup entry, persists shared mode as disabled, stops only plugin-owned processes, restores only the endpoint
recorded in `environment-backup.json`, broadcasts the environment change, and
leaves chats, state, logs, and user-owned credentials intact.

The watchdog also fails open when `config.json` is unreadable at startup or
shutdown. It does not overwrite that file; it uses the ownership-verified
`shared-server.json` record to recover the actual loopback endpoint, restores
the environment backup, stops the matching plugin-owned server, and exits for
explicit repair. This prevents a damaged configuration from leaving Codex
connected to a dead plugin port.
