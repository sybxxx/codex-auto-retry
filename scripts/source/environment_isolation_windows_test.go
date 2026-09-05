//go:build windows

package main

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// Even an interrupted test suite must never change the live Desktop route.
func TestMain(m *testing.M) {
	sharedAppServerEnvironmentName = fmt.Sprintf("CODEX_AUTO_RETRY_TEST_%d_%d", os.Getpid(), time.Now().UnixNano())
	code := m.Run()
	if err := restoreUserEnvironment(sharedAppServerEnvironmentName, "", false); err != nil {
		fmt.Fprintln(os.Stderr, "test environment cleanup failed:", err)
		code = 1
	}
	os.Exit(code)
}

func TestPersistentProductionRouteCannotBePublished(t *testing.T) {
	for _, name := range []string{"CODEX_APP_SERVER_WS_URL", "codex_app_server_ws_url"} {
		if _, err := setOwnedSharedEnvironmentNamed(t.TempDir(), name, "ws://127.0.0.1:49622"); err == nil {
			t.Fatal("production routing publication was not rejected")
		}
	}
}
