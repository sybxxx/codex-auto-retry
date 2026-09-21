# Shared Backend Safety

The plugin has two deliberately separate modes:

- Fail-open (default): `shared_app_server_enabled=false`. The installer leaves
  `CODEX_APP_SERVER_WS_URL` alone, Codex uses its bundled official backend, and
  the watchdog stops retries with `shared_app_server_disabled` instead of
  spinning. Such a stop records zero provider attempts; it is not an attempt
  limit and is shown as "shared backend disabled" in the tray and panel.
- Shared mode (explicit): the management panel or installer switch starts the
  plugin-owned loopback server, verifies the endpoint, WebSocket handshake,
  executable/version marker, PID, command line, and Codex home, then publishes
  the endpoint to the explicit safe launcher's child environment only. A failed
  check never publishes a persistent environment variable.

## Process-Scoped Desktop Launch

The safe launcher is used only when an explicit shared WebSocket route is
needed. Current Desktop builds can keep their official stdio backend and still
accept recovery through the verified `codex-ipc` owner route described below.

## Codex Capability Profiles

The watchdog reports the capability it has actually proved in its status
snapshot; a saved preference is not treated as a connection. The current
Windows Codex App profile is:

| Observed transport | Automatic recovery | Recovery path | Meaning |
| --- | --- | --- | --- |
| Official `stdio` with verified `codex-ipc` owner route | Yes | Official IPC owner-routed `thread-follower-start-turn` | Current Desktop exposes a loopback named-pipe router. The watchdog discovers the exact thread owner and submits an empty-input recovery request without changing global routing. |
| Official `stdio` without verified IPC | No | Safe process-scoped launcher | Codex is healthy, but an external watchdog cannot inject into its private stdio pipe. |
| Verified plugin-owned WebSocket | Yes | Shared WebSocket RPC | The endpoint, process identity, command line, version marker, and handshake all passed. |
| Stopped or unknown | No | None | The watchdog remains fail-closed until Codex or a verifiable connection is available. |

The `desktop_transport`, `recovery_mode`, `automatic_recovery_supported`, and
`recovery_capability_reason` fields are diagnostic only and are intentionally
derived from live evidence. A Codex update that changes its process or
transport shape therefore falls back to observation instead of being routed
through an unproven compatibility path.

Windows also stores startup approval separately from the `Run` command. The
settings window, management panel, and status script show `enabled`, `disabled`,
or `unknown` independently; the supervisor repairs only a disabled marker that
matches the plugin-owned executable.

Configuration version 11 separates the saved user preference
`shared_app_server_requested` from the effective `shared_app_server_enabled`
gate. Temporary failure disables only the latter. Explicit disable, uninstall
or fail-open installation disables both. Migration cannot infer a previously
lost preference: a version-10 configuration already set to false stays false
until the user explicitly enables it.

When an already-running compatible worker is fresh, the safe launcher may
request preparation twice, waiting up to 10 seconds per request. The worker
owns preparation on its serialized tick and consumes expiring requests; it
does not activate goals or retry conversations as part of preparation. User
opt-out, an existing Desktop, an expired request or a memory guard refuses
automatic preparation. If the worker is stopped, the launcher does not start
it against the user's choice. A failed attempt still uses the official backend.
`shared-availability.json` records the last failure without resetting consent.

Build provenance is checked independently of the version label:
`scripts/build.ps1` records source and EXE hashes in `scripts/build-info.json`
and embeds the source hash into the worker's `build_source_hash` heartbeat.
Packaging, including `-SkipBuild`, refuses missing or stale build provenance.
Installation verifies the actual running worker against that record. A plugin
version label or a ZIP checksum alone does not prove the EXE contains the
current source. Never distribute a rebuilt script bundle with previous EXEs.

Persistent user routing has been retired. `scripts/launch-codex.ps1` (also
available as `安全启动Codex.vbs` and the startup manager's `Launch Codex safely`
button) inspects the installed worker's fresh `desktop_launch_mode=process_scoped`
status, worker identity, shared-server ownership and WebSocket health. It gives
only the new Desktop process the verified endpoint. Missing, old, stopped or
unhealthy services select the official backend instead. `-Official` explicitly
selects that backend; `-CheckOnly` performs read-only preflight without opening
Desktop or writing a result record.
The tray settings window offers the same action when the worker reports
`codex_restart_required`. That action waits up to 120 seconds for Desktop to
close and then invokes this launcher; it never terminates an existing Codex
process or writes a persistent endpoint.

The launcher removes `CODEX_APP_SERVER_WS_URL` from the child environment before
choosing a route, including stale inherited values. It does not change the
parent environment, registry, account, provider configuration, official
shortcuts, protocol handlers or update entry points. An existing Desktop or
uncertain process identity blocks launch; no process is killed or focused.
A bounded launch mutex prevents concurrent safe-launch clicks. The current-user
OpenAI.Codex package supplies the executable path, not a saved versioned path.

Ordinary Codex launches no longer depend on the watchdog after old plugin-owned
persistent routing has been retired. They use the official backend; current
Desktop builds may still support silent recovery through their verified
`codex-ipc` owner route, while older builds require the safe launcher or remain
monitor-only. A missing/disabled Windows startup approval can therefore disable
retries without making Codex unbootable.
Foreign persistent user routes are preserved, not claimed to be safe or owned.
After migration, existing shells may still carry their old environment until
they restart or the user signs out. The safe launcher sanitizes its child copy.

This prevents a plugin-owned route surviving a reboot. It does not promise that
a shared server cannot fail between preflight and connection, or that a Codex
update's self-relaunch discards its inherited route. The last bounded
`desktop-launch.json` record says `launch_requested`, not "connected". Actual
recovery additionally requires an established Desktop TCP connection to the
expected shared port. Real packaged-Desktop login/update behavior and a full
reboot remain controlled manual acceptance gates.

The default loopback port is `49621`. The watchdog binds it before launching
Codex so Windows-excluded ranges and occupied ports can be reported separately
and the shared mode can fail open without a generic health-check message.
If that preferred port is occupied by an unknown or stale listener, the health
check selects the first available loopback port in a bounded range, persists
that port in `config.json`, and publishes only the new owned endpoint. It never
terminates the process that occupied the preferred port.

Upgrades are fail-open unless `-EnableSharedAppServer` is explicitly supplied:
the installer disables the stored shared-mode flag, restores the owned endpoint,
and removes a legacy endpoint only when an old plugin state or startup entry
proves ownership. A different user endpoint is left untouched.

If the optional mode is already enabled but cannot prepare its backend at
startup, the watchdog performs the same transition automatically and records the
failure reason while Codex continues with its official backend.

The shared server also mirrors the bundled Desktop `codex_app` definition when
it starts. The JSON definition is converted to the app-server's TOML override,
the plugin-only `type` field is removed, relative paths are made absolute, and
the normalized definition is hashed in `shared-server.json`. A Codex update
that changes this definition schedules an owned-server migration after the
Desktop process closes. If the app-server reports an invalid `codex_app`
transport, the watchdog disables shared mode, restores the official endpoint,
and stops the affected retry instead of repeatedly sending requests to a bad
backend.

Readiness retires only legacy plugin-owned persistent routing; it never restores
a missing shared endpoint into the user environment. A different current user
value is treated as an ownership conflict and is never overwritten; the
watchdog fails open and reports that conflict explicitly.

An upgrade may find a still-running app-server recorded by an older plugin
release. If its owner marker, executable path and hash, loopback endpoint, Codex
home, and live command line all still match, the new watchdog adopts that state
and updates only its plugin version marker. It does not treat its own server as
an external port conflict.

While shared mode is enabled and official IPC is not selected, shared readiness
is checked periodically even when no retry is queued. A plugin-owned server
that exits is restarted after the same
ownership and WebSocket health checks; an unowned listener is never terminated.

Verified official IPC is independent of shared mode. When Desktop uses its
official stdio backend, IPC preparation and dispatch do not start or repair a
shared server or publish environment routing. Readiness probes continue with
shared mode off. Only eligible, recent pre-dispatch stops can automatically
rejoin their existing retry chain after transport recovery; budgets, deadlines,
pause/cancellation and duplicate protection remain in force. Turning shared mode
off alone does not pause IPC retries. The global retry pause and service stop
controls still stop new dispatches; safe-disable still stops the owned worker.

The same readiness boundary samples the owned server's private memory. The
default monitor limit is 4096 MB. An over-limit sample records
`shared_app_server_memory_limit_exceeded`, disables shared mode, and defers
cleanup while Desktop is live; it never force-kills Codex. The watchdog has a
separate private-memory guard and alert for its own process.

The sign-in entry launches a small supervisor. It starts the actual watchdog
worker, restarts it after an unexpected exit with a one-second-to-one-minute
backoff, and records only lifecycle categories. A clean tray exit, uninstall,
or upgrade writes a one-shot stop marker so an intentional shutdown is not
resurrected. The worker remains the sole owner of the tray, retry state, and
shared app-server; the supervisor never creates a second backend. Installation
always migrates the current-user `Run` entry to `"...\\codex-auto-retry.exe"
supervise` and verifies that migration. This replaces the older direct `run`
entry that could exit without a stable cleanup owner.

Windows stores startup approval separately from the `Run` command. The
installer and startup manager update the owned `StartupApproved\\Run` marker
at the same boundary and report `enabled`, `disabled`, or `unknown`; disable,
safe-disable, uninstall, and rollback remove or restore only that matching
marker. A durable `shared-fail-open.json` marker covers an interrupted startup
transition so a later worker cannot recreate a shared endpoint from an old
enabled configuration before cleanup is complete.

The worker and supervisor share the same endpoint ownership record across
restarts. A new worker adopts a healthy owned server instead of creating a
disconnect window. A runtime shared-backend failure persists
`shared_app_server_enabled=false`; if Codex is still using the live server,
cleanup is deferred and the worker retries it after Desktop closes. Dead owned
state is removed immediately. This prevents a process boundary from killing
Codex's active route while still ensuring that a disabled backend is eventually
removed instead of remaining a permanent stale endpoint.

If `config.json` is unreadable during one of these boundaries, cleanup does not
rewrite or replace it. It derives the actual loopback port and Codex home from
the ownership-checked `shared-server.json`, restores the recorded environment
backup, and stops only the matching plugin-owned process. The watchdog then
stops so the damaged configuration can be repaired explicitly; Codex is not
left pointed at a dead plugin endpoint.

Installation requires Desktop to be fully closed, even in official mode.
Release CLI calls use `native-command.ps1`: .NET drains stdout and stderr
separately, preserving the real exit code on Windows PowerShell 5.1. Captures
are capped at 4,194,304/32,768 characters. CLI discovery selects a native EXE
and has a ten-second deadline, plugin-list
verification thirty seconds, registration sixty seconds, and runtime installation
three minutes. Only read-only list failures categorized as connection errors or
timeouts are retried once. Warning-only output does not invalidate JSON. Printed
diagnostics are limited to exit codes and predefined categories; raw output is
not logged. The target marketplace is checked before mutation; first installs
without a marketplace use the unfiltered read-only list instead.

The outer release transaction captures runtime EXEs/settings and the named Run/
StartupApproved values in `runtime-backup` before replacement. Final plugin
verification requires the exact cachebuster version, not merely an enabled
plugin with the same name. Rollback stops the new worker, retires owned routing,
verifies backup hashes, restores both source and runtime, and re-registers and
verifies the old plugin. It preserves concurrent foreign startup changes, retry
data and configuration; it never starts the old worker or republishes routing.
Failure to complete rollback keeps the journal and backups. A reopened Desktop
defers rollback. Interrupted-journal recovery uses the same restoration routine.
`installer-cli-smoke-test.ps1` and `upgrade-rollback-smoke-test.ps1` cover these
paths with child command fixtures and isolated files/mocked registry/processes.

It is transactional. Candidate binaries are staged and hashed before
the installed files are replaced. Configuration, startup registration,
environment ownership, and the previous binaries are captured; a failed
heartbeat or shared-mode health check restores the binaries and startup state,
but leaves the previous worker stopped with shared mode disabled. Rollback and
interrupted-journal recovery never republish a recorded plugin endpoint, even
when an older backup incorrectly calls that same endpoint its previous value.
Foreign current user values and unrelated retry settings are preserved. Runtime state and chat
data are not part of the rollback. The Windows environment-change broadcast is
advisory and runs through a minimal system process, so an oversized parent
environment cannot make the transaction fail after the durable registry write.

The tray settings form keeps the health check bounded and responsive. A failed
or timed-out start removes stale plugin-owned server state when its process has
already exited, and leaves Codex on its previous backend.

When shared mode is turned off or a fail-open transition is recorded while
Codex is still open, the service reports
`shared_app_server_migration_deferred` and keeps the ownership record. The
worker retries cleanup on later ticks; after Codex closes it restores the prior
endpoint and removes the owned server. Killing a live process immediately can
strand Codex in a broken session, while deleting the record early can make a
stale port look user-owned on the next startup.

`scripts/safe-disable.ps1` is the break-glass path. It does not use the
watchdog or Codex, and it only stops processes whose absolute executable path,
owner marker, endpoint, and command line match the plugin's state. It removes
the plugin's startup entry, persists shared mode disabled, restores the endpoint
recorded in `environment-backup.json`, broadcasts `Environment`, and verifies that the
stopped endpoint was not left in place. It never deletes `CODEX_API_KEY`, chat
data, state, or logs.

The release also includes `启动管理器.cmd`, `startup-manager.vbs`, and
`startup-manager.ps1`. The command file hands off to a detached Windows Script
Host launcher so Explorer double-clicks do not keep a console window in front
of the graphical manager. It shows
the exact current-user startup command, whether it is the supervised entry and
whether its matching Windows `StartupApproved\Run` marker is enabled, disabled, or unknown, the
verified watchdog PID/heartbeat, shared mode, endpoint presence, and shared
server state. It can enable or disable only the plugin-owned startup value,
start or stop only the plugin executable, invoke safe-disable, or perform the
complete release uninstallation. The default uninstall keeps retry data;
deleting runtime data requires a separate confirmation in the graphical
manager or `-RemoveData -NoPrompt` on an explicitly invoked command.

The release uninstaller removes and verifies both the `Run\CodexAutoRetry`
value and its matching `StartupApproved\Run` marker through the Windows
Registry API. This remains effective for an older or partially extracted
payload even when the installed helper script is unavailable.

When the endpoint is still present but neither the shared-server state nor its
ownership backup can be verified, startup fails closed with an explicit
ownership-unknown status. The worker preserves the endpoint rather than risk
deleting a value owned by another tool, and it does not report a successful
return to the official Codex backend. A user must remove or restore that value
deliberately before enabling shared mode again.

All status consumers verify both PID/path and heartbeat age. A stale status file
is therefore shown as `backend service not running`, even when it still
contains an old `running=true` value.

The status script and startup manager additionally verify the shared state
record's executable hash, exact command line, process creation time, loopback
endpoint, and live TCP listener. A matching PID alone is never displayed as a
healthy shared backend.
