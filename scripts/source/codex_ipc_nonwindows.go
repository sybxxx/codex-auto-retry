//go:build !windows

package main

import "context"

type unsupportedOfficialDesktopIPC struct{}

func newPlatformOfficialDesktopIPC() officialDesktopIPC { return unsupportedOfficialDesktopIPC{} }

func (unsupportedOfficialDesktopIPC) Available(context.Context) (bool, error) { return false, nil }

func (unsupportedOfficialDesktopIPC) StartTurn(context.Context, string, ResumeSettings) error {
	return errSharedServerUnavailable
}

func (unsupportedOfficialDesktopIPC) NotifySubagentRecovery(context.Context, string, string, string, ResumeSettings) error {
	return errSharedServerUnavailable
}
