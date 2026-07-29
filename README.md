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
  running. It does not open a task link, focus Codex, change the task currently
  on screen, or create a hidden `codex exec resume` task.
- Restores the failed task with its latest working directory, workspace roots,
  model and provider, service tier, reasoning settings, personality, approval
  routing, and effective permission profile instead of applying the App's
  defaults.
- In goal mode, uses Codex's native goal state and activates only a blocked
  goal that can be attributed to the same provider failure. Codex then creates
  the continuation turn itself.
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
- Internal subagent rollouts are owned by their parent task. They are not
  opened or resumed as independent user tasks, so one parent workflow cannot
  create duplicate retry entries for its internal workers.
- Tracks two independent safety limits. `本次故障恢复` counts every automatic
  recovery in one outage (15 by default, configurable from 1 to 100).
  `连续无进展` counts retries that produce neither a visible assistant reply
  nor a completed tool result (5 by default, configurable from 1 to 20). A
  successful completion or a new user turn clears both; visible progress clears
  only the consecutive no-progress count.
- Supports a fixed interval or doubling delays capped at a configurable maximum.
  Doubling follows the consecutive no-progress count, so visible progress starts
  the delay sequence over. It correlates the new
  `task_started` turn ID with its matching `task_complete`. An unrelated
  successful turn cannot falsely mark a retry as recovered.

The watchdog retries network failures, timeouts, rate limits, HTTP 5xx
responses, interrupted streams, successful completions with no final model
reply, and temporarily unavailable authentication services within the
configured dual limits. Ambiguous or
persistent authentication failures may have a lower safety limit. A temporary
inability to reach Codex App delays recovery without consuming an attempt.
User cancellation, invalid requests, missing models, context length errors,
policy failures, permission failures, and approval failures are not retried.

## Windows Tray Controller

The watchdog owns one notification-area icon; it does not install or start a
second background application. Hovering the icon shows whether retry is
running, paused, waiting, active, or stopped, including the nearest live
countdown. Double-clicking opens the graphical settings window. The right-click
menu opens settings, pauses or resumes dispatch, and exits the watchdog.

The graphical window shows every waiting, active, and exhausted task using only
privacy-safe task IDs. It edits the fallback retry text, both retry limits,
fixed or doubling waits, first/fixed delay, maximum delay, and notifications.
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
- the Windows notification preference.

Normal conversations use silent continuation first. The default fallback text
is `继续` and is used only if the installed Codex version explicitly rejects
empty-input turns. Goal mode never uses this text: it still activates Codex's
native interrupted goal. Saving the text takes effect without restarting the
watchdog.

The panel refreshes approximately every five seconds and computes countdowns
locally between refreshes. It never opens itself, focuses Codex, or navigates to
another task. Closing the panel has no effect on the global watchdog.

## Safety And Privacy

The event scanner accepts lifecycle records plus privacy-bounded progress
markers. For a completion it retains only
whether `last_agent_message` was present and non-empty, never its contents. A
completion with no explicit error and no final reply is treated as a temporary
empty-response failure. An abort remains authoritative even if a delayed
completion for that same turn is written afterward. For goal updates it retains only the
target task ID, status, and lifecycle timestamps; the event can be routed correctly even
when Codex persists it in another task's rollout. It never reads the goal
objective or searches conversation text for words such as "review". An
assistant message or completed tool-result item contributes only a boolean
"this retry made visible progress" marker; its content is never decoded or
stored. Immediately
before recovery, a separate settings reader decodes only an allowlisted subset
of the latest `turn_context` and
`thread_settings_applied` records: working directory, workspace roots, model
and provider, service tier, reasoning effort and summary, personality, approval
policy and reviewer, and effective permission mode. It discards every other
field and never forwards or logs developer instructions, conversation
messages, assistant output text, tool input, tool output, credentials, provider
URLs, or response bodies.

Recovery uses Codex App's own already-running local app-server connection. The
controller connects only to a loopback debugging endpoint whose page is the
Codex App, evaluates a fixed recovery program, and calls the same structured
`thread/resume`, `thread/goal/set`, and `turn/start` methods used by Codex. It
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

The watchdog and MCP management server are installed under
`%LOCALAPPDATA%\CodexAutoRetry`. Only the watchdog and its tray icon start at
the current user's Windows sign-in. The MCP server starts on demand through Codex and exits with
its Codex connection. Runtime state, heartbeat, configuration, controls, and
privacy-safe logs remain in the same local directory. Plugin management
commands live in `skills/codex-auto-retry/SKILL.md`.

After installing or updating the plugin, open a new Codex task so the updated
MCP tools and embedded panel are discovered. The background watchdog itself is
restarted and verified by the installer immediately.

Maintainers build a release with `scripts/build-release.ps1` and verify the
resulting archive with `scripts/release-test.ps1`. Release output must be kept
outside the plugin source tree; the builder accepts `-OutputDirectory` for that
purpose.

See `docs/project-map.md` for module ownership and `docs/architecture.md` for
the recovery state machine, safety boundaries, and verification model.

## Limitations

A permanently expired or revoked login still requires authentication. Codex
App must be running for a retry to start. If an App update removes or changes
the local background bridge, recovery fails closed and remains queued with
backoff; it never falls back to opening or focusing a task.

The tray controller requires Windows 10 or 11. Closing it through the tray menu
also stops automatic retry until the next Windows sign-in or reinstall/start.

If the failed rollout does not contain valid settings records, recovery also
remains queued instead of resuming the task with replacement defaults.

Third-party license notices for the WebSocket transport, MCP SDKs, and embedded
panel libraries are in
`THIRD_PARTY_NOTICES.md`.
