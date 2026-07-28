# Project Map

## User-Facing Components

| Path | Responsibility |
| --- | --- |
| `.codex-plugin/plugin.json` | Codex plugin identity, catalog metadata, skill discovery, and MCP server declaration. |
| `.gitignore`, `.gitattributes` | Keep local runtime state out of public source and make cross-platform line endings deterministic. |
| `.mcp.json` | Launches the installed local MCP management server on demand through stdio. |
| `skills/codex-auto-retry/SKILL.md` | Status, repair, installation, removal, privacy, compatibility, and retry-policy workflow. |
| `scripts/status.ps1` | Reads the installed heartbeat without inspecting conversation content. |
| `scripts/install.ps1` | Preserves configuration, replaces the binary, registers per-user startup, starts the watchdog, and verifies its heartbeat. |
| `scripts/uninstall.ps1` | Stops the watchdog, removes startup registration, and optionally preserves runtime data. |
| `scripts/build.ps1` | Type-checks and bundles the embedded panel, formats and tests Go, and builds the watchdog and MCP executables. |
| `scripts/build-release.ps1` | Builds a self-contained Windows x64 ZIP with one-click install/uninstall launchers and SHA-256 manifests. |
| `scripts/release-test.ps1` | Extracts a release, verifies every checksum and required file, parses installer scripts, and runs mutation-free installer/uninstaller checks. |
| `scripts/mcp-smoke-test.ps1` | Verifies MCP discovery, app resource metadata, isolated settings updates, and queued controls. |
| `scripts/smoke-test.ps1` | Runs an isolated process-level two-task retry and strict-correlation test through a mock background endpoint. |
| `scripts/renderer-control-smoke-test.ps1` | Probes the installed Codex App's background bridge through production discovery and transport code without changing UI or tasks. |
| `scripts/app-server-protocol-smoke-test.ps1` | Proves native goal and normal-turn continuation against an isolated app-server and temporary `CODEX_HOME`. |
| `release/windows/deploy.ps1` | One-click deployment engine: validates the package, safely updates the personal marketplace, registers the plugin, installs the runtime, and verifies the result. |
| `release/windows/uninstall-release.ps1` | Removes Codex registration, startup, and installed source while preserving runtime data unless full removal is explicitly requested. |
| `release/windows/common.ps1` | Shared path-safety, JSON, executable validation, and Codex CLI discovery helpers for release deployment. |
| `release/windows/安装.cmd`, `release/windows/卸载.cmd` | Double-click entry points for nontechnical Windows users. |

## Watchdog Source

Source code lives under `scripts/source`.

| Module | Ownership |
| --- | --- |
| `main.go` | Process startup, shutdown signaling, singleton acquisition, and top-level wiring. |
| `daemon.go` | Scan loop, strict retry state machine, intentional-goal hold protection, provider-failure attribution, bounded parallel scheduling, acknowledgement, and status publication. |
| `control.go` | Persistent pause state and atomic retry-now/cancel command files shared with the management process. |
| `management.go` | Privacy-bounded queue snapshots, heartbeat freshness, prompt updates, and management command submission. |
| `mcp_server.go` | Official Go MCP SDK wiring, management tools, and the embedded MCP App resource. |
| `scanner.go` | Incremental JSONL reads, file cursors, payload-based goal-task routing, rollout paths, and mirrored-session detection. |
| `events.go` | Privacy-bounded parsing of task start/completion and goal status/time lifecycle events. |
| `classifier.go` | Provider-independent retry decisions and limited authentication budgets. |
| `runner.go` | Controller result validation, privacy-safe failure codes, PowerShell discovery support, and retry backoff. |
| `resume_settings.go` | Reverse lookup and allowlisted validation of the latest per-task context and applied thread settings used during resume. |
| `renderer_control.go` | Loopback Codex target discovery, WebSocket transport, fixed background recovery program, live goal-hold checks, native goal resume, and same-task normal turn start. |
| `roots.go` | Default Codex, optional Cockpit, and explicitly configured session-root discovery. |
| `state.go` | Persistent cursors, pending and awaiting retries, turn correlation, migration, deduplication, and pruning. |
| `config.go` | Versioned defaults, validation, legacy visible-UI migration, and user overrides. |
| `jsonio.go` | Atomic JSON persistence. |
| `logger.go` | Size-limited, privacy-safe operational logging. |
| `lock_windows.go` | Per-user single-instance file lock. |
| `cmd/mock-cdp/main.go` | Test-only loopback DevTools endpoint used by the compiled process smoke test. |
| `*_test.go` | Classification, parsing, privacy, migration, restart, mirroring, correlation, concurrency, and background-controller regression tests. |

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
| `%LOCALAPPDATA%\CodexAutoRetry` | Watchdog and MCP executables, configuration, controls, commands, state, heartbeat, lock, stop signal, and logs. |
| `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` | Current-user startup entry named `CodexAutoRetry`. |

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
If the installed source is a Git checkout, its `.git` directory or worktree
pointer is restored after the payload replacement, so an upgrade does not erase
local history or remote configuration.

## Configuration

`%LOCALAPPDATA%\CodexAutoRetry\config.json` owns poll and backoff timing,
provider retry limits, task-start acknowledgement timeout, optional session
roots, maximum parallel retries, and the normal-conversation continuation
prompt. The prompt defaults to `继续`, is limited to 500 characters, and is
reloaded immediately before each normal-conversation dispatch.

Configuration version 2 migrates the old forced single UI action to four
independent background dispatch slots. A version 2 user override from one to
sixteen slots is preserved. `include_default_home` and
`include_cockpit_homes` control automatic root discovery.
`powershell_executable` optionally overrides Windows PowerShell discovery.
`renderer_debug_port` is a diagnostic/test-only fixed-port override; normal
installations discover Codex App automatically.

`control.json` stores the persistent pause switch separately from
`config.json`. One-use files under `commands` request `retry_now` or
`cancel_retry`; the watchdog consumes them while it owns the retry-state lock.
This keeps the MCP process from editing `state.json` concurrently with the
scanner.

## Extension Points

- Add provider wording in `classifier.go`; preserve permanent-error checks
  before broad transient matches.
- Add session locations through `config.json` `session_roots`; do not hard-code
  provider credentials.
- Adapt renderer bridge discovery in `renderer_control.go` when a verified
  Codex App update changes its bundled export shape; keep recovery restricted
  to fixed structured requests and loopback targets.
- Extend persisted resume settings in `resume_settings.go` only when the App
  protocol requires another task setting; never forward an entire
  `turn_context` or `thread_settings_applied` payload.
- Add event formats in `events.go` only when lifecycle correlation requires
  them; keep the parser restricted to lifecycle event records.
- Extend controller outcomes in `model.go`, `runner.go`, and `daemon.go`
  together so unrecognized results continue to fail closed.
