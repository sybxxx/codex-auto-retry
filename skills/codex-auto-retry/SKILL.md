---
name: codex-auto-retry
description: Inspect, configure, install, repair, or remove Codex Auto Retry. Use when the user asks about automatic retries, countdowns, queue state, retry text, pause controls, goal recovery, watchdog status, retry logs, supported failure types, startup behavior, installation, repair, or removal.
---

# Codex Auto Retry

This plugin includes a local Windows watchdog. Once installed, the watchdog is
global and starts at Windows sign-in. The user does not need to invoke this
plugin in each Codex task. Its MCP management panel is optional and does not
need to remain open for retries to work.

## Recovery Behavior

- Goal mode: rejoin the exact failed task through Codex App's existing
  background connection and activate its native goal. Codex creates the goal
  continuation turn itself.
- Normal conversation: create the configured continuation turn in the exact
  same task without touching the composer or a user draft.
- Preserve each task's latest model, workspace, reasoning, personality,
  approval, and effective permission settings during background resume.
- Never open a task link, focus Codex, switch the task currently displayed,
  launch `codex exec resume`, or create a hidden external Codex task.
- If one target task is already active, delay only that task. Other failed
  tasks retain their own queue entries and can retry independently.
- Treat an internal subagent rollout as part of its parent task, not as another
  independent user task to resume.
- Count recovery only when the App-created `task_started` ID has a matching
  successful `task_complete`.

## Embedded Management

When the MCP tools are available, use `get_auto_retry_status` to display the
embedded panel and return current state. The panel shows all pending and active
tasks, live countdowns, pause state, and the normal-conversation retry text.

- Use `set_retry_prompt` to change only the normal-conversation text. The
  default is `继续`, the maximum is 500 characters, and changes apply without
  restarting the watchdog.
- Goal recovery never sends the configured text; it continues to activate the
  native Codex goal.
- Use `set_auto_retry_paused` to pause or resume new dispatches. Do not claim
  that pausing terminates a retry that already started.
- Use `retry_now` or `cancel_retry` only with a task ID returned in the pending
  queue. Cancel cannot undo a retry that already started.
- Never open the panel automatically, navigate to another task, or focus Codex.

## Status

Run:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$env:USERPROFILE\plugins\codex-auto-retry\scripts\status.ps1"
```

Report whether the process is running, its version and PID, pause state, MCP
server installation, the last scan time, pending and active retry counts, and
the privacy-safe log path. Do not read Codex conversation content while
checking status.

## Install Or Repair

Run the build, compiled process smoke test, read-only installed-App probe,
isolated native-protocol test, and installer:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$env:USERPROFILE\plugins\codex-auto-retry\scripts\build.ps1"
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$env:USERPROFILE\plugins\codex-auto-retry\scripts\mcp-smoke-test.ps1"
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$env:USERPROFILE\plugins\codex-auto-retry\scripts\smoke-test.ps1"
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$env:USERPROFILE\plugins\codex-auto-retry\scripts\renderer-control-smoke-test.ps1"
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$env:USERPROFILE\plugins\codex-auto-retry\scripts\app-server-protocol-smoke-test.ps1"
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$env:USERPROFILE\plugins\codex-auto-retry\scripts\install.ps1"
```

The MCP smoke test uses isolated local data and verifies all management tools,
the embedded HTML resource, prompt changes, pause state, and atomic control
commands. The installed-App probe reads only a bounded App state summary through the
production background transport. It must not resume, navigate, or modify a
task. The isolated protocol test uses a temporary `CODEX_HOME` and does not use
Codex App UI. The installer preserves and migrates `config.json`, replaces both
executables, registers per-user Windows startup for the watchdog only, starts
the watchdog without a visible window, and verifies its heartbeat. A new Codex
task is required after plugin reinstall so Codex discovers the updated MCP
tools and panel.

## Remove

Only remove the watchdog when the user explicitly asks:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$env:USERPROFILE\plugins\codex-auto-retry\scripts\uninstall.ps1"
```

## Privacy Boundary

The event scanner parses only `event_msg` records whose payload type is
`task_started` or `task_complete`. Before dispatch, a separate reader extracts
only the required execution settings from the latest `turn_context` and
`thread_settings_applied` records. It does not retain, forward, or log prompts,
developer instructions, assistant messages, tool arguments, tool results, API
keys, or response bodies.

The controller connects only to Codex App on a loopback endpoint and sends a
fixed structured recovery program. It does not read drafts or automate the
window, mouse, keyboard, clipboard, composer, or task navigation. It never logs
the continuation prompt or app-server error bodies.

## Retry Policy

- Unlimited backoff retries: network failures, timeouts, HTTP 408/425/429 and
  5xx responses, interrupted streams, temporary provider authentication
  outages, cooldown, and provider overload.
- Limited retries: generic 401/403 authentication failures and unknown errors.
- No retry: user cancellation, invalid request or payload, missing model,
  context limit, policy, approval, or permission failures.

If a permanent login failure remains after the limited retry budget, explain
that re-authentication is required; do not weaken authentication or security.
