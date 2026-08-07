//go:build windows

package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRegisterTaskbarCreatedMessage(t *testing.T) {
	first, err := registerTaskbarCreatedMessage()
	if err != nil || first == 0 {
		t.Fatalf("TaskbarCreated message registration failed: id=%d err=%v", first, err)
	}
	second, err := registerTaskbarCreatedMessage()
	if err != nil || second != first {
		t.Fatalf("TaskbarCreated message registration was not stable: first=%d second=%d err=%v", first, second, err)
	}
}

func TestTrayWndProcHandlesTaskbarCreated(t *testing.T) {
	const hwnd = uintptr(0x7a11)
	restored := false
	app := &trayApp{
		hwnd:           hwnd,
		taskbarCreated: 0xc011,
		restoreIconFunc: func() {
			restored = true
		},
	}
	trayApps.Store(hwnd, app)
	defer trayApps.Delete(hwnd)
	if got := trayWndProc(hwnd, app.taskbarCreated, 0, 0); got != 0 {
		t.Fatalf("TaskbarCreated handler returned %d, want 0", got)
	}
	if !restored {
		t.Fatal("TaskbarCreated message did not trigger icon restoration")
	}
}

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

func TestRestoreTrayIconReaddsBeforeRefreshing(t *testing.T) {
	var events []string
	if !restoreTrayIcon(
		func() bool {
			events = append(events, "add")
			return true
		},
		func() { events = append(events, "remove") },
		func() { events = append(events, "refresh") },
	) {
		t.Fatal("successful tray icon restore returned false")
	}
	if got := strings.Join(events, ","); got != "remove,add,refresh" {
		t.Fatalf("tray icon restore order = %q, want remove,add,refresh", got)
	}
}

func TestRestoreTrayIconDoesNotRefreshAfterAddFailure(t *testing.T) {
	refreshed := false
	if restoreTrayIcon(
		func() bool { return false },
		func() {},
		func() { refreshed = true },
	) {
		t.Fatal("failed tray icon restore returned true")
	}
	if refreshed {
		t.Fatal("failed tray icon restore refreshed a missing icon")
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

func TestRetryLimitStoppedCountExcludesControllerStops(t *testing.T) {
	retries := []ManagedRetry{
		{State: "stopped", StopReason: "recovery_attempt_limit"},
		{State: "stopped", StopReason: "shared_app_server_disabled"},
		{State: "stopped", StopReason: "codex_not_running"},
		{State: "pending", StopReason: "consecutive_retry_limit"},
	}
	if got := retryLimitStoppedCount(retries); got != 1 {
		t.Fatalf("retry-limit notification count = %d, want 1", got)
	}
}
