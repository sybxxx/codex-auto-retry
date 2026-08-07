//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const hiddenConsoleSmokeHelper = "CODEX_AUTO_RETRY_HIDDEN_CONSOLE_HELPER"

func TestProcessOwnsRuntimeRejectsReusedPIDFromAnotherExecutable(t *testing.T) {
	dataDir := t.TempDir()
	if !processOwnsRuntime(os.Getpid(), dataDir) {
		t.Fatal("live test process was not accepted when no installed runtime path exists")
	}
	if err := os.WriteFile(filepath.Join(dataDir, "codex-auto-retry.exe"), []byte("not this process"), 0o600); err != nil {
		t.Fatal(err)
	}
	if processOwnsRuntime(os.Getpid(), dataDir) {
		t.Fatal("PID owned by another executable was accepted as the watchdog")
	}
}

func TestHiddenInheritedConsoleAttributes(t *testing.T) {
	attributes := hiddenInheritedConsoleAttributes()
	if !attributes.HideWindow {
		t.Fatal("shared app-server console is not hidden")
	}
	if attributes.CreationFlags&createNewConsole == 0 {
		t.Fatal("shared app-server does not receive an inheritable console")
	}
	if attributes.CreationFlags&createNoWindow != 0 {
		t.Fatal("CREATE_NO_WINDOW would leave descendants without a console to inherit")
	}
}

func TestIsCodexDesktopExecutable(t *testing.T) {
	for _, test := range []struct {
		path string
		want bool
	}{
		{path: `C:\Program Files\WindowsApps\OpenAI.Codex_26.727.6591.0_x64__2p2nqsd0c76g0\app\ChatGPT.exe`, want: true},
		{path: `C:\Users\test\AppData\Local\OpenAI\Codex\ChatGPT.exe`, want: true},
		{path: `C:\Program Files\WindowsApps\OpenAI.ChatGPT_1.0.0.0_x64__test\app\ChatGPT.exe`, want: false},
	} {
		if got := isCodexDesktopExecutable(test.path); got != test.want {
			t.Fatalf("isCodexDesktopExecutable(%q) = %v, want %v", test.path, got, test.want)
		}
	}
}

func TestHiddenInheritedConsoleDoesNotExposeDescendantWindow(t *testing.T) {
	if os.Getenv(hiddenConsoleSmokeHelper) == "1" {
		runHiddenConsoleSmokeHelper(t)
		return
	}
	if os.Getenv("CODEX_AUTO_RETRY_WINDOW_SMOKE") != "1" {
		t.Skip("set CODEX_AUTO_RETRY_WINDOW_SMOKE=1 for the real Windows window check")
	}
	title := fmt.Sprintf("CodexAutoRetryHiddenConsole-%d", time.Now().UnixNano())
	readyPath := filepath.Join(t.TempDir(), "ready")
	command := exec.Command(os.Args[0], "-test.run=^TestHiddenInheritedConsoleDoesNotExposeDescendantWindow$")
	command.Env = append(os.Environ(),
		hiddenConsoleSmokeHelper+"=1",
		"CODEX_AUTO_RETRY_HIDDEN_CONSOLE_TITLE="+title,
		"CODEX_AUTO_RETRY_HIDDEN_CONSOLE_READY="+readyPath,
	)
	command.SysProcAttr = hiddenInheritedConsoleAttributes()
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = terminateProcessTree(ctx, command.Process.Pid)
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		if !processIsRunning(command.Process.Pid) {
			t.Fatal("hidden console helper exited before becoming ready")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(readyPath); err != nil {
		t.Fatal("hidden console helper did not become ready")
	}
	time.Sleep(400 * time.Millisecond)
	for _, visibleTitle := range visibleWindowTitles() {
		if strings.Contains(visibleTitle, title) {
			t.Fatalf("hidden inherited console became visible: %q", visibleTitle)
		}
	}
}

func runHiddenConsoleSmokeHelper(t *testing.T) {
	title := os.Getenv("CODEX_AUTO_RETRY_HIDDEN_CONSOLE_TITLE")
	readyPath := os.Getenv("CODEX_AUTO_RETRY_HIDDEN_CONSOLE_READY")
	if title == "" || readyPath == "" {
		t.Fatal("hidden console helper settings are missing")
	}
	titlePointer, err := windows.UTF16PtrFromString(title)
	if err != nil {
		t.Fatal(err)
	}
	setConsoleTitle := windows.NewLazySystemDLL("kernel32.dll").NewProc("SetConsoleTitleW")
	if result, _, callErr := setConsoleTitle.Call(uintptr(unsafe.Pointer(titlePointer))); result == 0 {
		t.Fatalf("set console title: %v", callErr)
	}
	child := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "Start-Sleep -Seconds 15")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(readyPath, []byte("ready"), 0o600); err != nil {
		_ = child.Process.Kill()
		t.Fatal(err)
	}
	_ = child.Wait()
}

func visibleWindowTitles() []string {
	user32 := windows.NewLazySystemDLL("user32.dll")
	enumWindows := user32.NewProc("EnumWindows")
	isWindowVisible := user32.NewProc("IsWindowVisible")
	getWindowTextLength := user32.NewProc("GetWindowTextLengthW")
	getWindowText := user32.NewProc("GetWindowTextW")
	var titles []string
	callback := syscall.NewCallback(func(window uintptr, _ uintptr) uintptr {
		visible, _, _ := isWindowVisible.Call(window)
		if visible == 0 {
			return 1
		}
		length, _, _ := getWindowTextLength.Call(window)
		if length == 0 {
			return 1
		}
		buffer := make([]uint16, int(length)+1)
		getWindowText.Call(window, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
		titles = append(titles, windows.UTF16ToString(buffer))
		return 1
	})
	enumWindows.Call(callback, 0)
	return titles
}
