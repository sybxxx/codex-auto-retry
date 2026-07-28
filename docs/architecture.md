# Architecture

## Purpose And Boundary

Codex plugins do not receive a provider-failure callback after a model turn has
already stopped. This plugin therefore packages a local watchdog. The watchdog
observes persisted lifecycle events and asks the installed Codex App to recover
the original task through the App's already-running app-server connection.

The watchdog is provider-independent. It does not proxy model traffic, change
provider settings, refresh credentials, launch an external Codex CLI, start a
second app-server, or create a second task. The existing Codex process remains
the single owner of the task, so its workspace, model, permissions, login,
conversation, and goal state remain intact.

The user-facing management panel and the watchdog are deliberately separate
processes. Codex starts the MCP server only when it needs plugin tools or the
embedded panel; Windows starts the watchdog at sign-in. Closing Codex, closing
the panel, or ending the MCP stdio connection does not disable the watchdog.
The Windows tray icon is owned by the watchdog itself, so it adds no second
resident service and cannot disagree about process lifetime.

## Recovery Channel Decision

Three recovery channels were evaluated:

1. A `codex://threads/{id}` link can reach an exact task, but Codex always
   makes its primary window visible and navigates that window. This caused the
   foreground stealing and cross-task interruption reported by the user.
2. A separate `codex app-server` process can issue structured requests without
   a window, but it would independently own an in-memory copy of a task already
   loaded by Codex App. That risks stale UI state and concurrent rollout
   ownership.
3. The selected design reaches the app-server connection already owned by the
   running Codex renderer. It issues structured background requests through
   that connection and never invokes navigation or window APIs.

The third option preserves native behavior while removing the shared visible
UI surface that caused both reported failures.

## Data Flow

1. Codex writes a JSONL lifecycle event to a session rollout.
2. The scanner reads only newly appended bytes using a persisted file cursor.
3. The event parser accepts only `event_msg` records with payload type
   `task_started`, `task_complete`, or `thread_goal_updated`. A goal event is
   routed by its payload task ID because Codex may persist it in another
   task's rollout; only its status and lifecycle timestamps are retained.
4. A non-active goal state creates a durable hold and cancels pending,
   awaiting, or starting recovery. Only an explicit later `active` goal update
   clears that hold. A `blocked` state is exempt only when its update time is
   from two seconds before through five seconds after the matching provider
   failure, which allows for timestamp precision and event ordering.
5. A failed `task_complete` is classified as retryable, limited-retry, or
   non-retryable.
6. A retryable failure is deduplicated and scheduled with exponential backoff
   in state owned by that task ID.
7. The controller discovers Codex App's loopback debugging port, verifies an
   exact `app://-/index.html` Codex page, and connects to its renderer.
8. A reverse reader finds the latest `turn_context` and
   `thread_settings_applied` records in that task's rollout and decodes only the
   allowlisted execution settings needed by `thread/resume`.
9. The renderer program locates Codex's internal structured request bridge,
   reads the target task state, hydrates it in the background, and calls
   `thread/resume` with those settings on the existing app-server connection.
10. Parent-owned subagent rollouts are removed from the independent queue; their
   lifecycle remains part of the parent task that created them.
11. The renderer reads goal state before and after hydration/resume. A paused
   goal or a blocked goal that predates the provider failure stops recovery. A
   provider-attributed blocked goal is changed to `active`; completed,
   usage-limited, budget-limited, changed, and unknown states fail closed. Only
   a task with no goal may fall through to the configured normal continuation.
12. The first lifecycle `task_started` in the dispatch window becomes the retry
   turn ID. Only a `task_complete` with that same ID can recover the chain or
   schedule its next provider retry.

## Management Data Flow

1. Codex launches `codex-auto-retry-mcp.exe mcp` through the plugin's stdio MCP
   declaration. The process never starts at Windows sign-in.
2. `get_auto_retry_status` reads atomic snapshots of `status.json`,
   `state.json`, `control.json`, and `config.json`. It exposes task IDs,
   privacy-safe short labels, retry categories, attempts, actions, and due
   times, but no conversation content.
3. The MCP App receives structured tool output, refreshes approximately every
   five seconds, and updates each countdown locally once per second.
4. `set_retry_prompt` atomically updates only `config.json`. The runner reloads
   that field immediately before dispatching a normal-conversation retry.
5. `set_auto_retry_paused` atomically updates `control.json`. The watchdog keeps
   scanning and tracking active turns while paused, but starts no new retry.
6. `retry_now` and `cancel_retry` create unique atomic command files. During its
   next scan tick, the watchdog consumes those commands while holding the same
   lock that owns `state.json`.
7. `set_retry_settings` atomically updates the prompt, global consecutive
   attempt limit, first delay, maximum delay, and notification preference.
   `restart_retry` converts only an exhausted entry into an immediate first
   attempt with a fresh budget.

The MCP process never edits retry state directly. A retry-now command applies
only while the task remains pending. Cancellation also applies only before
dispatch; an already-started Codex turn is not terminated or falsely reported
as cancelled. These rules make management commands race-safe even when several
Codex tasks have panels open.

## Tray Data Flow

`tray_windows.go` owns the notification-area icon and hidden message window on
the watchdog's UI thread. It reads the same management snapshot once per
second, derives the nearest countdown, and updates only its tooltip and menu.
The icon does not inspect Codex windows, activate Codex, or navigate tasks.

Double-click launches the embedded `ui/settings.ps1` as one visible Windows
Forms settings process. The form reads the same privacy-bounded status,
configuration, control, and retry-state files. It sends validated settings and
one-use retry commands back through the watchdog executable; it never writes
`state.json`. The script is embedded in the build input and materialized with
an explicit UTF-8 BOM so Windows PowerShell renders Chinese labels correctly.
Only one settings process is launched from a watchdog instance at a time.

## Background Controller Safety

`renderer_control.go` owns port discovery, target validation, the DevTools
transport, and the fixed renderer program.

- Port discovery reads only process command lines containing a numeric
  `--remote-debugging-port` value.
- HTTP and WebSocket connections are restricted to loopback addresses and the
  same discovered port.
- The selected page must be a Codex `app://-/index.html` page.
- Task IDs, continuation prompts, and allowlisted resume settings are inserted
  by JSON encoding, not executable string concatenation.
- The renderer program calls only the fixed background methods needed for
  state read, hydration, resume, goal activation, and turn start.
- Goal state is checked before hydration and again immediately before the
  mutating goal/turn action. A goal appearing, disappearing, pausing, or
  changing to a terminal or limited state during dispatch fails closed.
- It contains no task deeplink, routing call, window activation, UI Automation,
  mouse, keyboard, clipboard, or composer access.
- A missing App, incompatible renderer bridge, active target task, or missing
  lifecycle acknowledgement is rescheduled with backoff.
- Missing or invalid persisted task settings fail closed and reschedule only
  that task; App defaults are never substituted during recovery.
- There is deliberately no visible-UI fallback.

Windows PowerShell is used only to discover the numeric debugging port. The
embedded stdin stream ends with a blank submission line because Windows
PowerShell otherwise may exit before executing its final compound statement.

## Concurrency And Retry Policy

Transient failures receive bounded retries with delays from five seconds up to
five minutes. The default is five automatic attempts per consecutive failure
chain and the user may select 1 through 20. These failures include network and
stream interruptions, timeouts, HTTP 408/425/429, HTTP 5xx, provider overload,
cooldown, and temporarily unavailable authentication services.

Generic 401/403 authentication errors and unknown provider errors have limited
budgets that can only lower the global limit. Invalid payloads, context limits,
missing models, policy errors, approval failures, permissions, and user
cancellation are never retried. Controller transport failures are tracked
separately and never consume the provider attempt budget.

Every task has separate pending, awaiting, attempt, and dispatch-failure state.
Due tasks are dispatched up to `max_parallel_retries` instead of competing for
one navigation surface. Activity in one task delays only that task. It cannot
cancel or block another task's queue entry.

An internal subagent is not a second user task. Its persisted `parentThreadId`
or `subAgent` source is detected before dispatch and the independent retry entry
is cleared, leaving recovery ownership with the parent workflow.

Provider retry attempts and controller dispatch failures are tracked
separately. A temporary inability to reach Codex App does not consume a
provider retry from a limited authentication budget.

## State And Correlation

`state.json` stores file offsets, processed event keys, task retry counters,
pending deadlines, failed turn IDs and times, goal status/timestamps/hold,
background actions, retry turn IDs, dispatch deadlines, exhausted retry
records, and rollout paths.
Atomic replacement prevents partial state files.

A pending retry moves to `awaiting` before background dispatch begins, so a
fast `task_started` cannot be lost. A matching start attaches its turn ID. A
different task started after acknowledgement cancels automation, and an
unrelated completion is ignored. On restart, an unacknowledged dispatch is
rescheduled after its deadline while an acknowledged running turn remains
tracked.

An intentional goal hold is not inferred from assistant prose. It is driven
only by Codex goal lifecycle status and update time. A normal task start cannot
clear it, so a user who briefly resumes and then pauses a goal remains
protected across later failures and watchdog restarts. Pausing during a queued
or starting retry clears its state and cancels the controller context; any late
controller result is treated as stale.

On startup, an interrupted controller dispatch that never received a
`task_started` acknowledgement is moved back to pending immediately. An
acknowledged retry turn remains correlated so a completion written while the
watchdog was stopped can still resolve it. A clean shutdown publishes zero
active and pending counts, and every management reader verifies the recorded
PID before accepting a `running` heartbeat.

The first installation baselines existing rollout files at their current end.
Later restarts continue from saved offsets and process failures written while
the watchdog was stopped. Mirrored Cockpit session files are recognized by
task ID and cannot replay old events.

`control.json` owns the durable pause switch. One-use `retry_now`,
`cancel_retry`, and `restart_retry` command files are kept in `commands` only
until a watchdog tick consumes them. The management MCP server and tray
settings process can therefore run concurrently with the watchdog without
becoming additional writers for `state.json`.

## Privacy And Security

- Lifecycle scanning parses only start, completion, and goal status/time
  events. For a goal it retains the payload task ID, status, and update time,
  but not the objective. The separate
  resume-settings reader inspects only the latest `turn_context` and decodes a
  fixed subset. A second allowlisted record supplies model-provider and service
  tier provenance without forwarding collaboration instructions or messages.
- Conversation messages, prompts, developer instructions, assistant output,
  tool arguments, and tool results are never retained or forwarded by either
  parser.
- Logs contain short task identifiers, retry categories, attempt numbers,
  delays, background action categories, and privacy-safe reason codes only.
- API keys, bearer tokens, provider URLs, request bodies, error bodies, drafts,
  and continuation text are not logged.
- The controller never reads composer or draft content.
- Recovery explicitly reapplies the target task's model, provider, service
  tier, workspace, reasoning, personality, permission, and approval settings.
  Authentication ownership remains attached to the same native task.

## Verification

- Go unit tests cover classification, privacy filtering, payload-based goal
  routing, intentional pause before/during retry, blocked-failure attribution,
  hold persistence across restart, latest-context reverse lookup, allowlisted
  settings, configuration migration, baselining, mirroring, strict turn
  correlation, per-task activity delays, bounded parallel dispatch, renderer
  target validation, fixed-method safety, and two simultaneous tasks with
  distinct settings.
- `go test -race` verifies concurrent scanning, dispatch, and controller
  completion. `go vet` checks static correctness.
- `smoke-test.ps1` runs the compiled GUI-subsystem watchdog with isolated
  sessions and a mock background endpoint. It proves HTTP 400 suppression, two
  independent HTTP 503 dispatches with distinct task settings, absence of
  navigation and external CLI mechanisms, private-context exclusion,
  unrelated-success rejection, matching-turn recovery, and graceful stop.
- `renderer-control-smoke-test.ps1` uses the production discovery and transport
  code to perform a read-only state snapshot and loaded-task-list request
  against the installed Codex App. It does not resume, navigate, or modify a
  task.
- `app-server-protocol-smoke-test.ps1` uses a temporary `CODEX_HOME` to prove
  that model, provider, service tier, workspace, approvals, permissions, and
  reasoning effort survive resume; goal activation starts native continuation;
  and `turn/start` creates a normal continuation in the same task without using
  Codex App UI.
- `mcp-smoke-test.ps1` launches the console-subsystem MCP binary with isolated
  data. It verifies all seven tools, nested and compatibility UI metadata, the
  `text/html;profile=mcp-app` resource, structured status, prompt and pause
  updates, and atomic retry-now/cancel command submission.
- `tray-smoke-test.ps1` starts an isolated GUI-subsystem watchdog and opens the
  real Windows Forms settings process through the native tray command. It
  requires the settings top-level window to be visible, verifies that numeric
  labels do not overlap their input fields, closes it through a test-only
  signal, and verifies that no stale running status remains.
- The panel is type-checked, bundled into a single offline HTML resource, and
  inspected with Playwright at desktop and narrow widths. The MCP Apps client
  handles host theme variables, safe-area insets, and automatic iframe sizing.
- `release-test.ps1` extracts the distributable ZIP, verifies every listed
  SHA-256 hash, checks its one-folder layout and required payload, parses all
  deployment scripts with the PowerShell AST parser, and executes mutation-free
  install and uninstall dry runs.

## Release And Upgrade Boundary

The release package contains application binaries, plugin metadata, source,
tests, documentation, and deployment scripts, but never copies a user's
`%LOCALAPPDATA%\CodexAutoRetry` directory. Configuration, pause state, queue
state, cursors, and logs therefore remain local across upgrades and cannot leak
into a published archive. The release builder also refuses to package anything
under `scripts/bin` except the two expected executables, and the archive test
rejects runtime JSON, logs, and `node_modules` wherever they appear.

The installer verifies `SHA256SUMS.txt` and x64 PE headers before changing
state. It stages a rollback copy of any existing owned plugin, edits the
personal marketplace through a JSON parser while retaining unrelated entries,
and invokes Codex's supported `plugin add` command instead of editing Codex
configuration directly. Runtime installation keeps user data in place and
must publish a version-matching heartbeat before the release is accepted. When
the installed plugin source is also a Git checkout, the installer restores its
`.git` metadata from the rollback copy before registration, preserving local
history and remotes without adding that metadata to the release archive.

The uninstaller uses Codex's supported `plugin remove` command, stops the
watchdog, removes current-user startup, and deletes only a plugin directory
whose manifest identifies it as `codex-auto-retry`. Runtime data is retained by
default and is deleted only with the explicit `-RemoveData` option. Both paths
reject directory links before recursive removal.

## Known Limitations

Retrying cannot repair a permanently expired or revoked login. Codex App must
be running. An App update that removes or changes its local structured bridge
temporarily prevents dispatch; this fails closed and backs off instead of
falling back to visible navigation.

Missing, oversized, or invalid latest settings records also prevent dispatch
and keep that task queued. This protects task settings instead of retrying with
the wrong defaults.
