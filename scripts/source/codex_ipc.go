package main

import (
	"context"
	"errors"
)

var errCodexIPCOwnerMissing = errors.New("Codex thread owner is unavailable")

// officialDesktopIPC is the narrow bridge exposed by current Codex Desktop
// builds. It is deliberately separate from the app-server WebSocket: the
// plugin must never assume that a stdio child is externally controllable.
type officialDesktopIPC interface {
	Available(context.Context) (bool, error)
	StartTurn(context.Context, string, ResumeSettings) error
	NotifySubagentRecovery(context.Context, string, string, string, ResumeSettings) error
}

func newOfficialDesktopIPC() officialDesktopIPC {
	return newPlatformOfficialDesktopIPC()
}
