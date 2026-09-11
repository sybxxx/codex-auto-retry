//go:build windows

package main

import "testing"

func TestStartupEntryMatchesExecutable(t *testing.T) {
	executable := `C:\Users\TQY\AppData\Local\CodexAutoRetry\codex-auto-retry.exe`
	for _, test := range []struct {
		name  string
		value string
		match bool
	}{
		{name: "quoted supervised", value: `"C:\Users\TQY\AppData\Local\CodexAutoRetry\codex-auto-retry.exe" supervise`, match: true},
		{name: "unquoted supervised", value: `C:\Users\TQY\AppData\Local\CodexAutoRetry\codex-auto-retry.exe supervise`, match: true},
		{name: "foreign path", value: `"C:\Temp\other.exe" supervise`, match: false},
		{name: "empty", value: "", match: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := startupEntryMatchesExecutable(test.value, executable); got != test.match {
				t.Fatalf("startupEntryMatchesExecutable(%q) = %v, want %v", test.value, got, test.match)
			}
		})
	}
}
