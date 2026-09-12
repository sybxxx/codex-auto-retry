# Security Policy

Codex Auto Retry runs locally and observes Codex lifecycle metadata. It can
start a plugin-owned loopback recovery backend when the user explicitly enables
that mode, so process ownership, endpoint routing, credential handling, and
rollback behavior are security-sensitive.

## Reporting A Vulnerability

Please use a [private GitHub Security Advisory](https://github.com/sybxxx/codex-auto-retry/security/advisories/new)
when available. Do not open a public issue for an unpatched vulnerability.

Include the affected release, Windows version, reproduction steps, and the
smallest relevant log excerpt. Remove API keys, authentication material,
session files, conversation text, tool arguments, response bodies, and personal
paths before submitting a report.

Security issues include, but are not limited to:

- credential or conversation-data exposure;
- arbitrary command execution or unsafe path handling;
- endpoint hijacking or persistent Codex routing changes;
- unverified process termination or cross-process control; and
- data loss, duplicate task creation, or an unbounded retry loop.

The current release is the recommended version for security fixes. Reports are
triaged privately and may result in a patch release, documentation update, or a
fail-open change that disables an unsafe recovery path.

## Security Boundaries

The default mode leaves Codex on its official backend. Shared recovery is
opt-in, loopback-only, ownership-checked, and process-scoped. The watchdog does
not need or store API keys, and it does not retain conversation or tool content
to make retry decisions.
