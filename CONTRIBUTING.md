# Contributing

Thank you for helping improve Codex Auto Retry. Contributions should preserve
the project's central boundary: recover the existing Codex task safely without
replaying completed work, changing global Codex routing, or creating a second
task or backend accidentally.

## Development Setup

The supported target is Windows 10 or 11 x64. Install Go and Node.js only for
development; end users install the self-contained release ZIP.

From the repository root:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\build.ps1
```

The build script installs the locked frontend dependencies, type-checks and
builds the management panel, formats and tests the Go sources, runs `go vet`,
and records build provenance for the two Windows binaries.

Useful focused checks are:

```powershell
Push-Location .\scripts\source
try {
  go test ./... -count=1
  go test -race ./... -count=1
  go vet ./...
} finally {
  Pop-Location
}
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\mcp-smoke-test.ps1
```

The MCP smoke test uses an isolated local fixture. The optional
`empty-response-protocol-smoke-test.ps1` and
`app-server-protocol-smoke-test.ps1` also require a standalone Codex CLI and
are intended for a machine where that CLI is already installed. Do not run
tests with a production account, and do not add credentials, session rollouts,
logs, generated runtime state, or local installation metadata to a pull
request.

## Pull Requests

- Explain the user-visible behavior and the failure or lifecycle state being
  changed.
- Add a focused regression test for bug fixes, including failure, retry,
  cancellation, repetition, and restart behavior when relevant.
- Keep provider traffic, Codex credentials, conversation content, and API keys
  outside logs, fixtures, and test output.
- Preserve process ownership checks and bounded timeouts. Never introduce
  force-kill behavior for an unverified process or a persistent
  `CODEX_APP_SERVER_WS_URL` write.
- Update the relevant README, architecture, installation, or security note
  when behavior, configuration, or operational boundaries change.
- Report the commands that passed and any checks that could not run.

Small documentation and test improvements are welcome. For larger behavior
changes, open an issue first so the recovery and fail-open boundaries can be
reviewed before implementation.
