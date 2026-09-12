## Summary

<!-- What user-visible behavior or maintenance change does this PR make? -->

## Verification

- [ ] `go test ./... -count=1`
- [ ] `go test -race ./... -count=1` when concurrency or lifecycle code changed
- [ ] `go vet ./...`
- [ ] Relevant Windows or protocol smoke tests

## Safety Checklist

- [ ] No credentials, session files, logs, chat content, or local runtime state are included.
- [ ] No persistent `CODEX_APP_SERVER_WS_URL` route is introduced.
- [ ] Process ownership and cancellation remain bounded and fail closed.
- [ ] A focused regression test covers the changed failure or lifecycle path.
