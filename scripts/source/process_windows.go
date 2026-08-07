//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	createNewConsole      = 0x00000010
	createNewProcessGroup = 0x00000200
	createNoWindow        = 0x08000000
	stillActive           = 259
)

func hiddenInheritedConsoleAttributes() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNewConsole | createNewProcessGroup,
	}
}

func terminateProcessTree(ctx context.Context, pid int) error {
	if pid <= 0 {
		return fmt.Errorf("invalid process id: %d", pid)
	}
	command := exec.CommandContext(ctx, "taskkill.exe", "/PID", strconv.Itoa(pid), "/T", "/F")
	command.Stdin = nil
	command.Stdout = nil
	command.Stderr = nil
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	if err := command.Run(); err != nil && processIsRunning(pid) {
		return fmt.Errorf("terminate process tree %d: %w", pid, err)
	}
	return nil
}

func processIsRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	var exitCode uint32
	return windows.GetExitCodeProcess(handle, &exitCode) == nil && exitCode == stillActive
}

func codexDesktopRunning() bool {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return true
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if !strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), "ChatGPT.exe") {
			continue
		}
		path, ok := processExecutablePath(entry.ProcessID)
		if !ok || isCodexDesktopExecutable(path) {
			return true
		}
	}
	return false
}

func processExecutablePath(pid uint32) (string, bool) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", false
	}
	defer windows.CloseHandle(handle)
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err != nil || size == 0 {
		return "", false
	}
	return windows.UTF16ToString(buffer[:size]), true
}

func isCodexDesktopExecutable(path string) bool {
	normalized := strings.ToLower(filepath.Clean(path))
	return strings.Contains(normalized, `\windowsapps\openai.codex_`) ||
		strings.Contains(normalized, `\openai\codex\`)
}

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
	if exitCode != stillActive {
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
