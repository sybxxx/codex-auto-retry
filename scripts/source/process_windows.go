//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func processOwnsRuntime(pid int, dataDir string) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return false
	}
	if exitCode != 259 {
		return false
	}
	expected := filepath.Join(dataDir, "codex-auto-retry.exe")
	if _, err := os.Stat(expected); err != nil {
		return true
	}
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err != nil || size == 0 {
		return false
	}
	actual := windows.UTF16ToString(buffer[:size])
	return strings.EqualFold(filepath.Clean(actual), filepath.Clean(expected))
}
