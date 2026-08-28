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
  the endpoint. A failed check does not change the environment.

The default loopback port is `49621`. The watchdog binds it before launching
Codex so Windows-excluded ranges and occupied ports can be reported separately
and the shared mode can fail open without a generic health-check message.

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

If the stored shared-mode flag is still enabled but `CODEX_APP_SERVER_WS_URL`
has disappeared, readiness first verifies the plugin-owned server state and
restores the recorded endpoint, then broadcasts the environment change. A
different current user value is treated as an ownership conflict and is never
overwritten; the watchdog fails open and reports that conflict explicitly.

An upgrade may find a still-running app-server recorded by an older plugin
release. If its owner marker, executable path and hash, loopback endpoint, Codex
home, and live command line all still match, the new watchdog adopts that state
and updates only its plugin version marker. It does not treat its own server as
an external port conflict.

While shared mode is enabled, readiness is also checked periodically when no
retry is queued. A plugin-owned server that exits is restarted after the same
ownership and WebSocket health checks; an unowned listener is never terminated.

The sign-in entry launches a small supervisor. It starts the actual watchdog
worker, restarts it after an unexpected exit with a one-second-to-one-minute
backoff, and records only lifecycle categories. A clean tray exit, uninstall,
or upgrade writes a one-shot stop marker so an intentional shutdown is not
resurrected. The worker remains the sole owner of the tray, retry state, and
shared app-server; the supervisor never creates a second backend. Installation
always migrates the current-user `Run` entry to `"...\\codex-auto-retry.exe"
supervise` and verifies that migration. This replaces the older direct `run`
entry that could exit without a stable cleanup owner.

The worker and supervisor both clean up the plugin-owned route at process
boundaries. Before a new worker prepares shared mode, it removes an endpoint
left by an earlier worker. After a worker exits or cannot start, the supervisor
restores the prior endpoint and removes a dead owned state record. A runtime
shared-backend failure also persists `shared_app_server_enabled=false` before
cleanup. Therefore an interrupted plugin backend fails open to Codex's normal
backend rather than leaving `CODEX_APP_SERVER_WS_URL` pointing at an unbound
loopback port.

If `config.json` is unreadable during one of these boundaries, cleanup does not
rewrite or replace it. It derives the actual loopback port and Codex home from
the ownership-checked `shared-server.json`, restores the recorded environment
backup, and stops only the matching plugin-owned process. The watchdog then
stops so the damaged configuration can be repaired explicitly; Codex is not
left pointed at a dead plugin endpoint.

Installation is transactional. Candidate binaries are staged and hashed before
the installed files are replaced. Configuration, startup registration,
environment ownership, and the previous binaries are captured; a failed
heartbeat or shared-mode health check restores them. Runtime state and chat
data are not part of the rollback. The Windows environment-change broadcast is
advisory and runs through a minimal system process, so an oversized parent
environment cannot make the transaction fail after the durable registry write.

The tray settings form keeps the health check bounded and responsive. A failed
or timed-out start removes stale plugin-owned server state when its process has
already exited, and leaves Codex on its previous backend.

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
the exact current-user startup command, whether it is the supervised entry, the
verified watchdog PID/heartbeat, shared mode, endpoint presence, and shared
server state. It can enable or disable only the plugin-owned startup value,
start or stop only the plugin executable, invoke safe-disable, or perform the
complete release uninstallation. The default uninstall keeps retry data;
deleting runtime data requires a separate confirmation in the graphical
manager or `-RemoveData -NoPrompt` on an explicitly invoked command.

All status consumers verify both PID/path and heartbeat age. A stale status file
is therefore shown as `backend service not running`, even when it still
contains an old `running=true` value.
