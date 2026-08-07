# Architecture

## Purpose And Boundary

Codex plugins do not receive a provider-failure callback after a model turn has
already stopped. This plugin therefore packages a local watchdog. The watchdog
observes persisted lifecycle events and asks the installed Codex App to recover
the original task through one shared local app-server.

The watchdog is provider-independent. It does not proxy model traffic, change
provider settings, refresh credentials, launch an external Codex CLI, start a
second per-task app-server, or create a second task. It starts one detached
loopback app-server that Codex Desktop and the watchdog both use. This keeps one
in-memory owner for task, workspace, model, permissions, login, conversation,
and goal state.

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
2. An independent app-server used only by the watchdog can issue structured
   requests without a window, but it would own a second in-memory copy of a task
   already loaded by Codex Desktop. That risks stale UI state and concurrent
   rollout ownership.
3. The selected design can launch one loopback WebSocket app-server and set
   `CODEX_APP_SERVER_WS_URL` only when the user explicitly enables shared mode.
   The watchdog becomes another client of the same server after Codex restarts.
   With shared mode disabled, Codex remains on its official backend and the
   watchdog fails closed without writing the global endpoint.

The third option preserves native behavior while removing the shared visible
UI surface that caused both reported failures.

## Data Flow

1. Codex writes a JSONL lifecycle event to a session rollout.
2. The scanner reads only newly appended bytes using a persisted file cursor.
3. The event parser accepts `event_msg` records with payload type
   `task_started`, `task_complete`, `turn_aborted`, `user_message`, or
   `thread_goal_updated`. For a completion it derives only whether
   `last_agent_message` is known and non-empty, then discards the value. A goal
   event is routed by its payload task ID because Codex may persist it in
   another task's rollout; only its status and lifecycle timestamps are retained.
   A `user_message` becomes a content-free explicit-input marker for the current
   started turn. `response_item` records become either a boolean visible-progress
   marker or, for the watchdog's fixed developer event only, a validated
   subagent-recovery marker containing parent, child, and event IDs.
4. A non-active goal state creates a durable hold and cancels goal recovery.
   Only an explicit later `active` goal update clears that hold. A `blocked`
   state is exempt when its update time is from two seconds before through five
   seconds after the matching provider failure. A second narrow exception
   records the start time of a later externally started conversation turn: its
   failure may be retried without changing a paused or pre-existing blocked
   goal, provided the held goal predates that turn and remains unchanged.
5. A `task_complete` with an explicit provider error or a known empty final
   reply is classified as retryable, limited-retry, or non-retryable. A
   `turn_aborted` clears recovery and records the aborted turn ID, so a delayed
   completion for the same turn cannot override user cancellation.
6. A retryable failure is deduplicated and scheduled with a fixed, linear, or
   doubling wait capped at the configured maximum, in state owned by that task ID.
7. When an active goal creates a new `task_started` within five seconds of its
   empty completion, that native continuation adopts the pending retry instead
   of resetting its two counters. User-role context items in that native turn
   are ignored; only the separate `user_message` lifecycle event proves that a
   person superseded the automatic chain.
8. The controller verifies that Codex Desktop is using the configured shared
   transport. An old Desktop-owned stdio app-server produces the visible,
   terminal `codex_restart_required` state instead of another countdown.
9. A reverse reader finds the latest `turn_context` and
   `thread_settings_applied` records in that task's rollout and decodes only the
   allowlisted execution settings needed by `thread/resume`.
10. The watchdog opens its own JSON-RPC WebSocket client, resumes an unloaded
    target before reading it, rechecks live task state, and calls
    `thread/resume` with the persisted settings when needed.
11. For a subagent empty reply, the controller derives the parent from the child
    thread. If the parent is unloaded, it locates that parent's rollout and
    restores its own allowlisted execution settings before injecting one
    deterministic recovery event and persisting the acknowledgement. It then
    re-reads the exact child and starts an empty-input continuation only if the
    child is still inactive. The fixed path has no spawn, replacement-thread,
    failed-turn replay, or parent-turn start; other subagent failure classes
    remain with the parent workflow.
12. The controller reads goal state before and after hydration/resume. A
    provider-attributed blocked goal is changed to `active`. Completed,
    usage-limited, budget-limited, changed, and unknown states fail closed. A
    paused or pre-existing blocked goal normally stops recovery. It permits only
    a later externally started conversation turn whose recorded start time is
    after the held goal update; both reads must return the same held revision.
13. Normal continuation, including the held-goal exception, first calls
    `turn/start` with an empty input array. It
   starts another model inference in the same task without a user-message item,
   while preserving the original request and completed tool results. The
   configured text is used only when the App returns an explicit empty-input
   validation error. The held-goal path never calls `thread/goal/set`, and the
   controller never rolls back or replays a completed turn.
14. If repeated empty replies from an active goal exceed either retry limit, a
    durable stopped entry is retained and a separate control job calls only
    `thread/goal/get` and `thread/goal/set status=blocked`. This safety closure
    runs even while ordinary retry dispatch is paused; controller failures back
    off without consuming provider-attempt counters. It stops at the configured
    controller-failure limit (three by default) and exposes a distinct terminal reason instead of looping
    indefinitely or claiming the goal was blocked.
15. The first lifecycle `task_started` in the dispatch window becomes the retry
    turn ID. Only a `task_complete` with that same ID can recover the chain or
    schedule its next provider retry.

## Management Data Flow

1. Codex launches `codex-auto-retry-mcp.exe mcp` directly through the plugin's
   stdio declaration. Release deployment rewrites the portable declaration to
   the installed absolute path before plugin registration. The GUI-subsystem
   process never opens a console and never starts at Windows sign-in.
2. `get_auto_retry_status` reads atomic snapshots of `status.json`,
   `state.json`, `control.json`, and `config.json`. It exposes task IDs,
   privacy-safe short labels, retry categories, attempts, actions, and due
   times, but no conversation content.
3. The MCP App receives structured tool output, refreshes approximately every
   five seconds, and updates each countdown locally once per second.
4. `set_retry_prompt` atomically updates only `config.json`. The runner reloads
   that fallback field immediately before dispatching a normal-conversation retry.
5. `set_auto_retry_paused` atomically updates `control.json`. The watchdog keeps
   scanning and tracking active turns while paused, but starts no new retry.
6. `retry_now` and `cancel_retry` create unique atomic command files. During its
   next scan tick, the watchdog consumes those commands while holding the same
   lock that owns `state.json`.
7. `set_retry_settings` atomically updates the prompt, both retry limits, wait
   strategy, first/fixed delay, maximum delay, and notification preference.
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
The icon does not inspect Codex windows, activate Codex, or navigate tasks. It
also registers the system `TaskbarCreated` message; when Explorer recreates the
notification area, the watchdog removes and re-adds its icon, clears its local
tooltip cache, and refreshes the current visual state.

Double-click launches the embedded `ui/settings.ps1` as one visible Windows
Forms settings process. The form reads the same privacy-bounded status,
configuration, control, and retry-state files. It sends validated settings and
one-use retry commands back through the watchdog executable; it never writes
`state.json`. The script is embedded in the build input and materialized with
an explicit UTF-8 BOM so Windows PowerShell renders Chinese labels correctly.
Only one settings process is launched from a watchdog instance at a time.
Local commands from the form are polled in short bounded slices while the
WinForms message loop continues to run. A command that exceeds the 35-second
UI deadline is terminated as a process tree and reported as a failed save;
shared-backend enablement therefore cannot leave the window waiting forever.

## Background Controller Safety

`shared_server_windows.go`, `desktop_transport_windows.go`,
`app_server_rpc.go`, and `shared_controller.go` jointly own the recovery
transport.

The shared app-server is a console-subsystem Codex process, but it is launched
with one new console whose window is hidden. Console-subsystem descendants such
as Playwright MCP, Node REPL, the code-mode host, and PowerShell then inherit
that same hidden console. Launching the server detached would leave those
children without a console and Windows would create a separate visible Windows
Terminal window for each one. State records the launch mode; an older detached
server remains available while Codex is open, then the watchdog waits for the
Desktop process to exit, terminates only that owned process tree, and replaces
it with the hidden-console server.

- The server listens only on `127.0.0.1`; `wss`, hostnames, credentials, paths,
  and a different port are rejected.
- The default port is `49621`. A TCP4 bind preflight runs before the Codex
  process starts, classifying Windows-excluded ports separately from occupied
  ports so management surfaces can show an actionable reason.
- The watchdog records the server PID, executable, endpoint, Codex home, and
  start time. A responsive port is rejected unless its live process still
  matches that owned record.
- Loopback WebSocket traffic bypasses environment HTTP proxies.
- JSON-RPC calls are fixed structured methods; task IDs, fallback prompts, and
  allowlisted settings are JSON values, not executable strings.
- The controller uses only state read, loaded-list, resume, parent
  recovery-event injection, goal status, and turn-start methods.
- Goal state is checked before hydration and again immediately before the
  mutating goal/turn action. A goal appearing, disappearing, pausing, or
  changing to a terminal or limited state during dispatch fails closed.
- It contains no renderer evaluation, DevTools port, task deeplink, routing
  call, window activation, UI Automation,
  mouse, keyboard, clipboard, or composer access.
- A closed App waits without consuming either provider or controller counters.
  A restart requirement and permanent local configuration conflict stop
  immediately; other controller failures stop at a configurable small limit.
- Missing or invalid persisted task settings fail closed and reschedule only
  that task; App defaults are never substituted during recovery.
- Subagent recovery validates a deterministic event ID, loads an unloaded
  parent only with that parent's persisted settings, notifies it once, rechecks
  the original child's live status, and calls `turn/start` only on that child.
  It contains no `spawn_agent` or `thread/start` request and cannot create a
  replacement thread.
- Goal-limit closure uses a separate fixed program that cannot hydrate, resume,
  inject items, or start a turn.
- There is deliberately no visible-UI or navigation fallback. The compatibility
  prompt fallback is still a structured background request and runs only after
  an explicit empty-input validation rejection.

Windows PowerShell is used only for read-only process ownership and Desktop
transport checks. The embedded stdin stream ends with a blank submission line
because Windows PowerShell otherwise may exit before executing its final
compound statement.

## Concurrency And Retry Policy

Transient and empty-response failures receive bounded retries. Every automatic
recovery increments the per-fault counter (15 by default, configurable from 1
to 1000). A second counter tracks consecutive retries without a visible assistant
reply or completed tool result (5 by default, configurable from 1 to 100). These failures include network and
stream interruptions, timeouts, HTTP 408/425/429, HTTP 5xx, provider overload,
cooldown, successful completions without a final reply, and temporarily
unavailable authentication services. An empty automatic retry remains a failure
until the matching completion contains a real final reply. Visible progress resets
only the no-progress counter; it never erases the per-fault safety budget. Fixed
waiting always uses the configured interval. Linear waiting adds the configured
increment, while doubling multiplies by two; both are capped by the maximum and
follow the no-progress counter, so useful progress restarts the wait sequence.

Generic 401/403 authentication errors have a conservative budget that can lower
both configured limits. Unknown provider errors keep a separate recovery safety
budget, but continue to use the configured consecutive no-progress limit so an
internal classifier ceiling cannot be displayed as the user's setting. Invalid payloads, context limits,
missing models, policy errors, approval failures, permissions, and user
cancellation are never retried. Controller transport failures are tracked
separately and consume neither provider retry counter. They are bounded by
`controller_failure_limit`, including read failures while checking an
acknowledged retry turn. `codex_not_running` is terminal for the affected
chain at both the pending-dispatch and acknowledged-turn stages: the watchdog
clears the retry state, records the explicit stop reason, and waits for a manual
restart command after Codex is open again. This prevents a closed desktop from
causing an unbounded background polling loop.

Every task has separate pending, awaiting, recovery-attempt, consecutive
no-progress, and dispatch-failure state.
Due tasks are dispatched up to `max_parallel_retries` instead of competing for
one navigation surface. Activity in one task delays only that task. It cannot
cancel or block another task's queue entry.

Stopped records remain durable for diagnosis, but the management queue exposes
only a short recent-stop window. Older stopped records are history, not tasks
waiting to retry, so they do not inflate the current queue counts.

An internal subagent is not treated as a replacement user task. For an empty
reply, its persisted `parentThreadId` or `subAgent` source selects the exact
parent and child pair. A deterministic event ID lets the parent observe one
recovery event and lets scanner state confirm that notification after a restart.
The watchdog is the sole wake-up owner for that event and rechecks child
activity before continuing it. Non-empty subagent failures remain owned by the
parent workflow.

An active goal's native post-failure turns remain in the same bounded chain.
Reaching either limit leaves a durable stopped record and schedules goal
closure independently of the ordinary retry queue and pause switch. This keeps
the user-visible reason available after the native goal becomes `blocked`. The
local closure path is separately bounded; exhaustion preserves the provider
counters and reports that automatic blocking itself failed.

Provider retry attempts and controller dispatch failures are tracked
separately. A temporary inability to reach Codex App does not consume a
provider retry from a limited authentication budget.

## State And Correlation

`state.json` stores file offsets, processed event keys, task retry counters,
pending deadlines, failed turn IDs and times, goal status/timestamps/hold,
last-started and last-aborted turn IDs, background actions, retry turn IDs,
dispatch deadlines, parent-notification acknowledgement, durable goal-stop
requests, exhausted retry records, and rollout paths.
Atomic replacement prevents partial state files.
Windows sharing violations during replacement are retried with a multi-second
bounded backoff. If the target remains temporarily locked, the daemon retains
the authoritative in-memory state, publishes `state_write_deferred`, and tries
again on the next scan. A persistence hiccup is therefore visible but does not
terminate the watchdog or its tray controller.

A pending retry moves to `awaiting` before background dispatch begins, so a
fast `task_started` cannot be lost. A matching start attaches its turn ID. A
different task started after acknowledgement cancels automation, and an
unrelated completion is ignored. On restart, an unacknowledged dispatch is
rescheduled after its deadline while an acknowledged running turn remains
tracked.

An immediate native goal turn can move a pending empty-response retry directly
to `awaiting` before the controller fires. The counters and original fault stay
attached to it. A later explicit `user_message` lifecycle marker cancels that
adoption, while automatic user-role context items do not. Once the goal limit
is exhausted, later native starts, completions, or stale active-goal records
cannot clear the stopped reason; only a later observed explicit activation or a
management restart begins a fresh budget.

An intentional goal hold is not inferred from assistant prose. It is driven
only by Codex goal lifecycle status and update time. A normal task start cannot
clear it. Instead, the watchdog separately records that external turn's start
time so only its conversation continuation can run while the goal remains
held. A later pause or goal revision cancels that exception. This protects a
user who briefly resumes and then pauses a goal across failures and watchdog
restarts. Pausing during a queued or starting retry clears its state and
cancels the controller context; any late controller result is treated as stale.

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

- Lifecycle scanning parses only start, completion, abort, explicit user-input,
  and goal status/time events, plus assistant/tool metadata needed to derive a progress boolean. For
  a completion it retains only known/non-empty booleans for the
  final message. For a goal it retains the payload task ID, status, and update time,
  but not the objective. The explicit-input event retains no message fields.
  User-role `response_item` context is ignored. The separate
  resume-settings reader inspects only the latest `turn_context` and decodes a
  fixed subset. A second allowlisted record supplies model-provider and service
  tier provenance without forwarding collaboration instructions or messages.
- Conversation messages, prompts, developer instructions, assistant output text,
  tool arguments, and tool results are never retained or forwarded by either
  parser, except that the watchdog parses its own fixed subagent recovery event
  and retains only its validated parent, child, and deterministic event IDs.
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
  routing, explicit-input versus native-goal correlation, deterministic
  subagent event routing, intentional pause before/during retry, later external
  conversation turns beside a held goal, blocked-failure attribution, dual retry limits,
  visible-progress resets, hold persistence across restart, latest-context reverse lookup, allowlisted
  settings, configuration migration, baselining, mirroring, strict turn
  correlation, per-task activity delays, bounded parallel dispatch, shared
  server ownership validation, fixed-method safety, unloaded-task resume, and
  two WebSocket clients observing one retry.
- `go test -race` verifies concurrent scanning, dispatch, and controller
  completion. `go vet` checks static correctness.
- `shared-app-server-smoke-test.ps1` launches an isolated real Codex app-server
  over WebSocket plus a local fake Responses provider. A simulated Desktop
  client creates the task and observes the retry started by a separate watchdog
  client. It proves one additional provider request, same-task context,
  no visible user item, and no real provider use.
- `environment-smoke-test.ps1` uses a random temporary user environment name to
  prove endpoint ownership, idempotent updates, prior-value restoration, and
  refusal to overwrite a conflicting value. `smoke-test.ps1` runs both checks.
- `app-server-protocol-smoke-test.ps1` uses a temporary `CODEX_HOME` to prove
  that model, provider, service tier, workspace, approvals, permissions, and
  reasoning effort survive resume; goal activation starts native continuation;
  empty-input `turn/start` creates a normal continuation in the same task; and
  the same continuation can start beside an unchanged paused goal; it also
  proves `thread/inject_items` persists the fixed parent recovery event and an
  active goal can be changed to `blocked`, all without using Codex App UI.
- `empty-response-protocol-smoke-test.ps1` uses a local fake Responses API and
  temporary `CODEX_HOME` to reproduce an HTTP 200 completion with zero model
  output. It proves that silent continuation sends exactly one additional
  provider request, retains the original prompt in context, creates no visible
  user item, and stores the recovered assistant reply in the same task.
- `mcp-smoke-test.ps1` launches the GUI-subsystem MCP binary with isolated
  data. It verifies all seven tools, nested and compatibility UI metadata, the
  `text/html;profile=mcp-app` resource, structured status, prompt and pause
  updates, and atomic retry-now/cancel command submission.
- `tray-smoke-test.ps1` starts an isolated GUI-subsystem watchdog and opens the
  real Windows Forms settings process through the native tray command. It
  requires the settings top-level window to be visible, verifies that numeric
  labels do not overlap their input fields, exercises concurrent status refresh
  across several watchdog scans, closes it through a test-only signal, and
  verifies that no stale running status remains.
- The panel is type-checked, bundled into a single offline HTML resource, and
  inspected with Playwright at desktop and narrow widths. The MCP Apps client
  handles host theme variables, safe-area insets, and automatic iframe sizing.
- `release-test.ps1` extracts the distributable ZIP, verifies every listed
  SHA-256 hash, checks its one-folder layout and required payload, checks the
  GUI PE subsystem, parses all deployment scripts with the PowerShell AST
  parser, executes dry runs, and performs an isolated plugin-only install to
  prove that the PowerShell fallback is replaced by the direct MCP launcher.

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
Runtime installation is fail-open by default: it does not write
`CODEX_APP_SERVER_WS_URL`, and a new configuration has
`shared_app_server_enabled=false`. The explicit shared-mode path first starts
and validates a versioned, plugin-owned loopback server and completes a
WebSocket health handshake; only then does it atomically publish the endpoint
and startup entry. A failed candidate restores the previous binaries,
configuration, environment value, and startup entry. Uninstall and the
independent `scripts/safe-disable.ps1` restore only the endpoint recorded in
`environment-backup.json`; neither path removes `CODEX_API_KEY` or chat/state
data by default.

The shared-server ownership record includes the plugin owner, version, PID,
absolute executable, endpoint, and Codex home. Cleanup checks all of those
fields plus the live process command line before stopping a process. Status
readers also check the PID and heartbeat age, so a stale `running=true` JSON
file is presented as “后台服务未运行”.

The uninstaller uses Codex's supported `plugin remove` command, stops the
watchdog, removes current-user startup, and deletes only a plugin directory
whose manifest identifies it as `codex-auto-retry`. Runtime data is retained by
default and is deleted only with the explicit `-RemoveData` option. Both paths
reject directory links before recursive removal.

## Known Limitations

Retrying cannot repair a permanently expired or revoked login. Codex App must
be running. An App update that removes or changes its local structured protocol
prevents dispatch; this fails closed at the controller limit instead of falling
back to visible navigation. When shared mode is enabled, Codex requires one
restart to inherit `CODEX_APP_SERVER_WS_URL`; the default fail-open mode does
not require that restart.

Missing, oversized, or invalid latest settings records also prevent dispatch
and keep that task queued. This protects task settings instead of retrying with
the wrong defaults.

Codex App owns turn-completion notifications. The App emits a completion toast
before the watchdog's rollout scan can identify that a nominally successful
completion had no final model reply, so the watchdog cannot selectively prevent
or retract only that false toast. Codex's native `notifications-turn-mode =
"off"` setting disables all turn-completion notifications. The watchdog's
`show_notifications` setting is intentionally narrower and controls only its
own retry-limit notification.
