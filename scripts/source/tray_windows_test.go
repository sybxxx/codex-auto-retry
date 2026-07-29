//go:build windows

package main

import (
	"bytes"
	"testing"
)

func TestEnsureUTF8BOMAddsMissingMarker(t *testing.T) {
	bom := []byte{0xef, 0xbb, 0xbf}
	got := ensureUTF8BOM([]byte("param()"))
	if !bytes.HasPrefix(got, bom) {
		t.Fatal("UTF-8 BOM was not added")
	}
}

func TestEmbeddedSettingsScriptHasOneUTF8BOM(t *testing.T) {
	bom := []byte{0xef, 0xbb, 0xbf}
	got := ensureUTF8BOM([]byte(settingsPowerShell))
	if !bytes.HasPrefix(got, bom) {
		t.Fatal("embedded settings script is missing its UTF-8 BOM")
	}
	if bytes.HasPrefix(got[len(bom):], bom) {
		t.Fatal("embedded settings script contains a duplicate UTF-8 BOM")
	}
}

func TestGoalEmptyResponseStoppedCount(t *testing.T) {
	retries := []ManagedRetry{
		{State: "stopped", StopReason: goalEmptyResponseStopReason},
		{State: "stopped", StopReason: "recovery_attempt_limit"},
		{State: "pending", StopReason: goalEmptyResponseStopReason},
	}
	if got := goalEmptyResponseStoppedCount(retries); got != 1 {
		t.Fatalf("goal-specific tray notification count = %d, want 1", got)
	}
}

func TestGoalEmptyResponseBlockFailedCount(t *testing.T) {
	retries := []ManagedRetry{
		{State: "stopped", StopReason: goalEmptyResponseBlockFailReason},
		{State: "stopped", StopReason: goalEmptyResponseStopReason},
		{State: "pending", StopReason: goalEmptyResponseBlockFailReason},
	}
	if got := goalEmptyResponseBlockFailedCount(retries); got != 1 {
		t.Fatalf("goal-block failure tray notification count = %d, want 1", got)
	}
}
