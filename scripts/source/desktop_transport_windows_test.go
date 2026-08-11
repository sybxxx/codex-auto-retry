//go:build windows

package main

import (
	"strings"
	"testing"
)

func TestDesktopTransportScriptDistinguishesListenServerFromLegacyStdio(t *testing.T) {
	if !strings.Contains(desktopTransportScript, "$legacy = @($owned | Where-Object") {
		t.Fatal("desktop transport probe does not distinguish legacy app-server processes")
	}
	if !strings.Contains(desktopTransportScript, "--listen(?:=|\\s)") {
		t.Fatal("desktop transport probe does not recognize the shared app-server listen flag")
	}
}
