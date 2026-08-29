# Project Map

## User-Facing Components

| Path | Responsibility |
| --- | --- |
| `.codex-plugin/plugin.json` | Codex plugin identity, catalog metadata, skill discovery, and MCP server declaration. |
| `.gitignore`, `.gitattributes` | Keep local runtime state out of public source and make cross-platform line endings deterministic. |
| `.mcp.json` | Portable hidden fallback for the on-demand stdio MCP server; release deployment replaces it with the direct installed executable path. |
| `skills/codex-auto-retry/SKILL.md` | Status, repair, installation, removal, privacy, compatibility, and retry-policy workflow. |
| `scripts/status.ps1` | Reads the installed heartbeat, verifies PID/path and age, and reports stale or app-sandbox-redirected services, startup mode, endpoint presence, and shared-server state without inspecting conversation content. |
| `scripts/startup-manager.ps1` | Provides a standalone status/start/stop/enable/disable/safe-disable/uninstall manager with ownership checks and a graphical Windows Forms view. |
| `scripts/install.ps1` | Rejects app-sandbox path redirection, then transactionally stages and verifies binaries, defaults to fail-open, optionally enables the shared app-server after health checks, migrates the per-user startup entry to supervised mode, and rolls back on failure. |
| `scripts/path-safety.ps1` | Detects Windows package redirection or directory links before runtime installation can be mistaken for a host installation. |
| `scripts/path-safety-smoke-test.ps1` | Verifies ordinary paths, missing-path probe cleanup, redirected-path rejection, and non-destructive failure. |
| `scripts/uninstall.ps1` | Stops watchdog/MCP/settings processes, restores the prior shared-server environment, removes startup registration, and optionally preserves runtime data. |
| `scripts/safe-disable.ps1` | Independent break-glass cleanup for plugin-owned processes, startup, and endpoint; persists shared mode disabled and never removes chat data or user-owned `CODEX_API_KEY`. |
| `docs/shared-backend-safety.md` | Operational contract for fail-open startup, supervised migration, opt-in shared mode, transactional rollback, emergency disable, and stale-backend diagnostics. |
| `scripts/environment.ps1` | Shared current-user environment ownership, explicit registry-value removal, backup/restore, Windows change broadcast, and safe unused-server cleanup. |
| `scripts/build.ps1` | Type-checks and bundles the embedded panel, formats and tests Go, and builds both Windows executables with the GUI subsystem. |
| `scripts/build-release.ps1` | Builds a self-contained Windows x64 ZIP with one-click install/uninstall launchers and SHA-256 manifests. |
| `scripts/release-test.ps1` | Extracts a release, verifies every checksum and required file, parses installer scripts, and runs path-safety plus mutation-free installer/uninstaller checks. |
| `scripts/mcp-smoke-test.ps1` | Verifies MCP discovery, app resource metadata, isolated settings updates, and queued controls. |
| `scripts/tray-smoke-test.ps1` | Starts an isolated watchdog, verifies its native tray window, visible non-overlapping settings form, concurrent refresh stability, settings-process shutdown, heartbeat, and clean status shutdown. |
| `scripts/smoke-test.ps1` | Runs the isolated shared-server and environment-ownership smoke tests. |
| `scripts/supervisor-smoke-test.ps1` | Starts the sign-in supervisor, kills only the worker to prove bounded restart, then verifies an intentional stop is honored. |
| `scripts/status-smoke-test.ps1` | Verifies the installed watchdog process and UTC heartbeat are reported as fresh, including on non-UTC Windows time zones. |
| `scripts/shared-app-server-smoke-test.ps1` | Uses a real isolated Codex WebSocket app-server, two clients, and a local fake provider to prove Desktop-visible same-task recovery without a visible user message. |
| `scripts/environment-smoke-test.ps1` | Proves safe environment ownership, idempotent endpoint updates, restoration, and conflict refusal through a random test-only user variable. |
| `scripts/app-server-protocol-smoke-test.ps1` | Proves native goal recovery, silent normal-turn continuation, continuation beside an unchanged paused goal, settings-preserving resume of an unloaded parent before fixed event injection, and active-to-blocked goal closure against an isolated app-server and temporary `CODEX_HOME`. |
| `scripts/empty-response-protocol-smoke-test.ps1` | Reproduces an HTTP 200 response with no model output through a local fake provider, then proves silent same-task recovery without adding a user message or replaying the original turn. |
| `release/windows/deploy.ps1` | One-click deployment engine: validates the package, writes the direct background MCP launcher, safely updates the personal marketplace, registers the plugin, installs the runtime, and verifies the result. |
| `release/windows/uninstall-release.ps1` | Removes Codex registration, startup, and installed source while preserving runtime data unless full removal is explicitly requested. |
| `release/windows/common.ps1` | Shared path-safety, JSON, executable validation, and Codex CLI discovery helpers for release deployment. |
| `release/windows/安装.cmd`, `release/windows/卸载.cmd`, `release/windows/启动管理器.cmd`, `release/windows/启动管理器.vbs`, `release/windows/安全停用.cmd` | Double-click entry points for installation, clean removal, startup management, and break-glass shared-backend disable; the VBS helper starts the graphical manager without a console. |

## Watchdog Source

Source code lives under `scripts/source`.

| Module | Ownership |
| --- | --- |
| `main.go` | Process startup, supervised worker shutdown signaling, singleton acquisition, local settings/control commands, and top-level wiring. |
| `supervisor.go` | Stable sign-in supervisor, bounded worker restart backoff, intentional-stop marker handling, shared-backend lifecycle ownership across worker restarts, and privacy-safe lifecycle logging. |
| `daemon.go` | Scan loop, bounded parallel dispatch, generic controller lifecycle, acknowledgement timeouts, and status publication. |
| `retry_state.go` | Generic retry transitions, dual attempt limits, visible-progress resets, startup reconciliation, later external-turn attribution, and management command application. |
| `goal_recovery.go` | Goal lifecycle holds, native-turn adoption, stale-update protection, bounded post-limit goal blocking, and goal-specific controller reconciliation. |
| `subagent_recovery.go` | Durable acknowledgement of deterministic parent recovery events for the exact existing child. |
| `control.go` | Persistent pause state and atomic retry-now/cancel/restart command files shared with management surfaces. |
| `management.go` | Privacy-bounded queue snapshots, process-backed heartbeat freshness, settings updates, and management command submission. |
| `mcp_server.go` | Official Go MCP SDK wiring, management tools, and the embedded MCP App resource. |
| `tray_windows.go` | Native notification-area icon, live tooltip/countdown, menu controls, and graphical settings-process lifecycle. |
| `process_windows.go` | Windows process-liveness verification, Codex Desktop detection, hidden inherited-console attributes, and owned process-tree cleanup. |
| `scanner.go` | Incremental JSONL reads, file cursors, payload-based goal-task routing, parent-to-child recovery-event routing, rollout paths, and mirrored-session detection. |
| `events.go` | Privacy-bounded parsing of task start, completion, abort, explicit user input, goal lifecycle, visible-progress markers, and the plugin's fixed subagent recovery event. |
| `classifier.go` | Provider-independent retry decisions, empty-response classification, and limited authentication budgets. |
| `runner.go` | Controller result validation, privacy-safe failure codes, runtime shared-backend fail-open handling, PowerShell discovery support, and retry backoff. |
| `resume_settings.go` | Reverse lookup, exact-thread rollout discovery, and allowlisted validation of the latest per-task context and applied thread settings used during resume. |
| `app_server_rpc.go` | Loopback JSON-RPC WebSocket transport, initialization, request correlation, and fail-closed handling of interactive server requests. |
| `shared_server_windows.go` | Starts and records the opt-in shared app-server in one hidden inherited console, applies the normalized bundled `codex_app` MCP override, validates loopback health and versioned process ownership, migrates stale launch/config state after Codex closes, and discovers the Codex CLI. |
| `codex_app_mcp_windows.go` | Reads the bundled Desktop `codex_app` definition without modifying it, normalizes it into a TOML app-server override, and computes the migration hash. |
| `shared_mode_windows.go` | Owns the transactional opt-in endpoint backup/restore, deferred cleanup while Desktop is live, registry broadcast, health gate, and plugin-owned server shutdown without touching API keys. |
| `desktop_transport_windows.go` | Read-only detection of stopped, old Desktop-owned stdio, or shared-server Codex transport. |
| `shared_controller.go` | Settings-preserving unloaded task and parent resume, live task/goal rechecks, deterministic parent notification, exact-child continuation, goal recovery/blocking, and silent normal continuation. |
| `roots.go` | Default Codex, optional Cockpit, and explicitly configured session-root discovery. |
| `state.go` | Persistent cursors, pending and awaiting retries, turn correlation, migration, deduplication, and pruning. |
| `config.go` | Versioned defaults, validation, legacy visible-UI migration, and user overrides. |
| `jsonio.go` | Atomic JSON persistence. |
| `logger.go` | Size-limited, privacy-safe operational logging. |
| `lock_windows.go` | Per-user single-instance file lock. |
| `ui/settings.ps1` | Embedded Windows Forms status and settings window launched from the tray icon. |
| `*_test.go` | Classification, parsing, privacy, migration, restart, mirroring, correlation, concurrency, controller bounds, shared-server ownership, and two-client recovery regression tests. |

## Embedded Panel Source

The panel source lives under `scripts/source/ui`. It uses the official MCP Apps
client, a small vanilla TypeScript view, and Lucide icons. Vite produces one
self-contained `dist/panel.html`; `mcp_server.go` embeds that file into the MCP
executable, so the installed panel performs no network requests and needs no
Node.js runtime.

## Runtime Locations

| Location | Contents |
| --- | --- |
| `%USERPROFILE%\plugins\codex-auto-retry` | Personal plugin source. |
| `%USERPROFILE%\.codex\plugins\cache\personal\codex-auto-retry` | Codex's installed plugin cache. |
| `%LOCALAPPDATA%\CodexAutoRetry` | Supervisor/worker and MCP executables, configuration, controls, commands, state, heartbeat, shared-server ownership, environment backup, locks, stop signals, and logs. |
| `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` | Current-user startup entry named `CodexAutoRetry`. |
| `HKCU\Environment\CODEX_APP_SERVER_WS_URL` | Optional loopback WebSocket endpoint; written only after explicit shared-mode health checks, with ownership backup and safe restoration. |

## Release Layout

The distributable ZIP contains a single versioned folder. Its top level holds
the double-click launchers, PowerShell deployment engine, Chinese installation
guide, `release-manifest.json`, and `SHA256SUMS.txt`. The complete plugin lives
under `payload/codex-auto-retry`; personal task state, configuration, and logs
are never copied into a release.

Installation replaces only the owned plugin source after taking a temporary
rollback copy. It updates `~/.agents/plugins/marketplace.json` through JSON
parsing while retaining unrelated marketplace entries, uses Codex's own
`plugin add` command, and delegates runtime replacement to `scripts/install.ps1`.
Before changing the plugin or runtime, release deployment rejects a
`%LOCALAPPDATA%` target redirected into the Codex package `LocalCache`; that
copy is not a host installation and cannot satisfy startup verification.
If the installed source is a Git checkout, its `.git` directory or worktree
pointer is restored after the payload replacement, so an upgrade does not erase
local history or remote configuration.

## Configuration

`%LOCALAPPDATA%\CodexAutoRetry\config.json` owns poll and wait strategy/timing,
the recovery and consecutive no-progress limits, provider-specific lower limits, task-start acknowledgement timeout, optional session
roots, maximum parallel retries, the normal-conversation fallback prompt, and
the watchdog retry-limit notification preference. Normal recovery first starts a silent
empty-input continuation. The fallback prompt defaults to `继续`, is limited to
500 characters, and is used only when Codex explicitly rejects an empty-input
turn. It is reloaded immediately before each normal-conversation dispatch. The
per-fault recovery limit defaults to 15 and accepts values from 1 through 1000.
The consecutive no-progress limit defaults to five and accepts values from 1
through 100. Waiting can stay fixed, add `delay_increment_seconds` linearly, or
double from the initial delay up to the maximum; visible progress restarts an
increasing sequence.

Configuration version 2 migrated the old forced single UI action to four
independent background dispatch slots. A version 2 user override from one to
sixteen slots is preserved. Configuration version 3 adds the bounded retry and
notification defaults without replacing existing timing, prompt, roots, or
parallelism settings. Configuration version 4 separates the old ambiguous
retry limit into the per-fault recovery budget and the consecutive no-progress
guard, and stores the wait strategy explicitly. Existing
`max_retry_attempts` values become the recovery budget; the new no-progress
guard starts at five. `include_default_home` and
`include_cockpit_homes` control automatic root discovery.
`powershell_executable` optionally overrides Windows PowerShell discovery.
Configuration version 5 adds the linear increment. Version 6 replaces the
removed renderer debugging channel with `shared_app_server_port` and a bounded
`controller_failure_limit` (three by default). Version 7 adds
`shared_app_server_enabled`, which defaults to false so Codex remains fail-open
on its official backend. Version 8 moves the shared-server default port out of
the Windows-excluded range and adds a bind preflight with distinct reserved and
occupied-port diagnostics. Version 9 adds a bounded private-memory guard for
the watchdog.

`control.json` stores the persistent pause switch separately from
`config.json`. One-use files under `commands` request `retry_now`,
`cancel_retry`, or `restart_retry`; the watchdog consumes them while it owns
the retry-state lock. This keeps both graphical management surfaces from
editing `state.json` concurrently with the scanner.

State format version 5 also persists parent-notification acknowledgement and
post-limit goal-stop requests. These fields make subagent recovery idempotent
across restarts and prevent an exhausted native goal chain from being cleared
by a later automatic turn.

## Extension Points

- Add provider wording or a narrowly validated structured wrapper in
  `classifier.go`; preserve permanent-error checks before broad transient
  matches. The CC Switch 400 exception must keep its exact code, upstream
  status, and cause/message checks.
- Add session locations through `config.json` `session_roots`; do not hard-code
  provider credentials.
- Adapt protocol calls in `shared_controller.go` and transport validation in
  `app_server_rpc.go` when a verified Codex App update changes the app-server
  schema; keep recovery restricted to fixed structured requests and the owned
  loopback target.
- Extend persisted resume settings in `resume_settings.go` only when the App
  protocol requires another task setting; never forward an entire
  `turn_context` or `thread_settings_applied` payload.
- Add event formats in `events.go` only when lifecycle correlation or the
  content-free progress marker requires them; never retain record content.
- Extend controller outcomes in `model.go`, `runner.go`, and `daemon.go`
  together so unrecognized results continue to fail closed.
