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

Installation is transactional. Candidate binaries are staged and hashed before
the installed files are replaced. Configuration, startup registration,
environment ownership, and the previous binaries are captured; a failed
heartbeat or shared-mode health check restores them. Runtime state and chat
data are not part of the rollback.

The tray settings form keeps the health check bounded and responsive. A failed
or timed-out start removes stale plugin-owned server state when its process has
already exited, and leaves Codex on its previous backend.

`scripts/safe-disable.ps1` is the break-glass path. It does not use the
watchdog or Codex, and it only stops processes whose absolute executable path,
owner marker, endpoint, and command line match the plugin's state. It removes
the plugin's startup entry, restores the endpoint recorded in
`environment-backup.json`, broadcasts `Environment`, and verifies that the
stopped endpoint was not left in place. It never deletes `CODEX_API_KEY`, chat
data, state, or logs.

All status consumers verify both PID/path and heartbeat age. A stale status file
is therefore shown as `backend service not running`, even when it still
contains an old `running=true` value.
