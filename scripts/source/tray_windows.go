//go:build windows

package main

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

//go:embed ui/settings.ps1
var settingsPowerShell string

const (
	wmDestroy       = 0x0002
	wmCommand       = 0x0111
	wmTimer         = 0x0113
	wmClose         = 0x0010
	wmNull          = 0x0000
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205
	wmAppTray       = 0x8001

	nimAdd     = 0
	nimModify  = 1
	nimDelete  = 2
	nifMessage = 0x1
	nifIcon    = 0x2
	nifTip     = 0x4
	nifInfo    = 0x10

	mfString       = 0x0000
	mfGrayed       = 0x0001
	mfSeparator    = 0x0800
	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100

	menuOpenSettings = 1001
	menuTogglePause  = 1002
	menuExit         = 1003
	trayTimerID      = 1
)

type trayPoint struct{ X, Y int32 }
type trayMessage struct {
	HWnd     uintptr
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       trayPoint
	LPrivate uint32
}
type windowClass struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSmall  uintptr
}
type notifyIconData struct {
	Size             uint32
	HWnd             uintptr
	ID               uint32
	Flags            uint32
	CallbackMessage  uint32
	Icon             uintptr
	Tip              [128]uint16
	State            uint32
	StateMask        uint32
	Info             [256]uint16
	TimeoutOrVersion uint32
	InfoTitle        [64]uint16
	InfoFlags        uint32
	GUID             windows.GUID
	BalloonIcon      uintptr
}

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	shell32                 = windows.NewLazySystemDLL("shell32.dll")
	kernel32                = windows.NewLazySystemDLL("kernel32.dll")
	procRegisterClassEx     = user32.NewProc("RegisterClassExW")
	procCreateWindowEx      = user32.NewProc("CreateWindowExW")
	procDefWindowProc       = user32.NewProc("DefWindowProcW")
	procDestroyWindow       = user32.NewProc("DestroyWindow")
	procPostMessage         = user32.NewProc("PostMessageW")
	procGetMessage          = user32.NewProc("GetMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessage     = user32.NewProc("DispatchMessageW")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
	procSetTimer            = user32.NewProc("SetTimer")
	procKillTimer           = user32.NewProc("KillTimer")
	procLoadIcon            = user32.NewProc("LoadIconW")
	procCreatePopupMenu     = user32.NewProc("CreatePopupMenu")
	procAppendMenu          = user32.NewProc("AppendMenuW")
	procTrackPopupMenu      = user32.NewProc("TrackPopupMenu")
	procDestroyMenu         = user32.NewProc("DestroyMenu")
	procGetCursorPos        = user32.NewProc("GetCursorPos")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procShellNotifyIcon     = shell32.NewProc("Shell_NotifyIconW")
	procGetModuleHandle     = kernel32.NewProc("GetModuleHandleW")
	trayWindowProc          = syscall.NewCallback(trayWndProc)
	trayApps                sync.Map
)

type trayApp struct {
	hwnd         uintptr
	dataDir      string
	logger       *safeLogger
	cancel       context.CancelFunc
	service      *managementService
	icons        map[string]uintptr
	lastTip      string
	lastIcon     string
	lastStopped  int
	initialized  bool
	settingsMu   sync.Mutex
	settingsOpen bool
}

func runTray(ctx context.Context, cancel context.CancelFunc, dataDir string, logger *safeLogger) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	className, _ := windows.UTF16PtrFromString(fmt.Sprintf("CodexAutoRetryTray-%d", os.Getpid()))
	instance, _, _ := procGetModuleHandle.Call(0)
	icon, _, _ := procLoadIcon.Call(0, 32512)
	wc := windowClass{Size: uint32(unsafe.Sizeof(windowClass{})), WndProc: trayWindowProc, Instance: instance, Icon: icon, IconSmall: icon, ClassName: className}
	if result, _, err := procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc))); result == 0 {
		return fmt.Errorf("register tray window: %w", err)
	}
	hwnd, _, err := procCreateWindowEx.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(className)), 0, 0, 0, 0, 0, 0, 0, instance, 0)
	if hwnd == 0 {
		return fmt.Errorf("create tray window: %w", err)
	}
	app := &trayApp{
		hwnd: hwnd, dataDir: dataDir, logger: logger, cancel: cancel,
		service: newManagementService(dataDir),
		icons: map[string]uintptr{
			"running": loadSharedIcon(32516),
			"waiting": loadSharedIcon(32515),
			"paused":  loadSharedIcon(32515),
			"active":  loadSharedIcon(32516),
			"stopped": loadSharedIcon(32513),
		},
	}
	trayApps.Store(hwnd, app)
	defer trayApps.Delete(hwnd)
	if !app.addIcon() {
		procDestroyWindow.Call(hwnd)
		return fmt.Errorf("add tray icon")
	}
	procSetTimer.Call(hwnd, trayTimerID, 1000, 0)
	app.refresh()
	go func() {
		<-ctx.Done()
		procPostMessage.Call(hwnd, wmClose, 0, 0)
	}()
	var message trayMessage
	for {
		result, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if int32(result) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&message)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&message)))
	}
	return nil
}

func trayWndProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	value, ok := trayApps.Load(hwnd)
	if !ok {
		result, _, _ := procDefWindowProc.Call(hwnd, uintptr(message), wParam, lParam)
		return result
	}
	app := value.(*trayApp)
	switch message {
	case wmTimer:
		app.refresh()
		return 0
	case wmAppTray:
		switch uint32(lParam) {
		case wmLButtonDblClk:
			app.openSettings()
		case wmRButtonUp:
			app.showMenu()
		}
		return 0
	case wmCommand:
		app.handleCommand(uint16(wParam & 0xffff))
		return 0
	case wmClose:
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procKillTimer.Call(hwnd, trayTimerID)
		app.removeIcon()
		procPostQuitMessage.Call(0)
		return 0
	}
	result, _, _ := procDefWindowProc.Call(hwnd, uintptr(message), wParam, lParam)
	return result
}

func (a *trayApp) addIcon() bool {
	data := notifyIconData{Size: uint32(unsafe.Sizeof(notifyIconData{})), HWnd: a.hwnd, ID: 1, Flags: nifMessage | nifIcon | nifTip, CallbackMessage: wmAppTray, Icon: a.icons["running"]}
	copyUTF16(data.Tip[:], "Codex Auto Retry")
	result, _, _ := procShellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(&data)))
	return result != 0
}

func (a *trayApp) removeIcon() {
	data := notifyIconData{Size: uint32(unsafe.Sizeof(notifyIconData{})), HWnd: a.hwnd, ID: 1}
	procShellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&data)))
}

func (a *trayApp) refresh() {
	snapshot, err := a.service.snapshot(time.Now().UTC())
	if err != nil {
		a.setTip("Codex Auto Retry - 状态读取失败")
		return
	}
	tip := "Codex Auto Retry - 运行中"
	iconState := "running"
	if snapshot.Paused {
		tip = "Codex Auto Retry - 已暂停"
		iconState = "paused"
	} else if snapshot.ActiveRetries > 0 {
		tip = fmt.Sprintf("Codex Auto Retry - 正在重试 %d 个任务", snapshot.ActiveRetries)
		iconState = "active"
	} else if seconds, ok := nextRetrySeconds(snapshot.Retries); ok {
		tip = fmt.Sprintf("Codex Auto Retry - %d 秒后自动重试", seconds)
		iconState = "waiting"
	} else if snapshot.StoppedRetries > 0 {
		tip = fmt.Sprintf("Codex Auto Retry - %d 个任务已停止重试", snapshot.StoppedRetries)
		iconState = "stopped"
	}
	a.setVisual(iconState, tip)
	if a.initialized && snapshot.ShowNotifications && snapshot.StoppedRetries > a.lastStopped {
		a.notify("自动重试已停止", fmt.Sprintf("有 %d 个任务已达到连续重试上限。", snapshot.StoppedRetries))
	}
	a.lastStopped = snapshot.StoppedRetries
	a.initialized = true
}

func nextRetrySeconds(retries []ManagedRetry) (int64, bool) {
	var seconds int64
	found := false
	for _, retry := range retries {
		if retry.State != "pending" {
			continue
		}
		if !found || retry.SecondsRemaining < seconds {
			seconds, found = retry.SecondsRemaining, true
		}
	}
	return seconds, found
}

func (a *trayApp) setTip(tip string) {
	a.setVisual(a.lastIcon, tip)
}

func (a *trayApp) setVisual(iconState, tip string) {
	if iconState == "" {
		iconState = "running"
	}
	if tip == a.lastTip {
		if iconState == a.lastIcon {
			return
		}
	}
	a.lastTip = tip
	a.lastIcon = iconState
	data := notifyIconData{Size: uint32(unsafe.Sizeof(notifyIconData{})), HWnd: a.hwnd, ID: 1, Flags: nifTip | nifIcon, Icon: a.icons[iconState]}
	copyUTF16(data.Tip[:], tip)
	procShellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&data)))
}

func loadSharedIcon(resourceID uintptr) uintptr {
	icon, _, _ := procLoadIcon.Call(0, resourceID)
	return icon
}

func (a *trayApp) notify(title, text string) {
	data := notifyIconData{Size: uint32(unsafe.Sizeof(notifyIconData{})), HWnd: a.hwnd, ID: 1, Flags: nifInfo, InfoFlags: 0x1}
	copyUTF16(data.InfoTitle[:], title)
	copyUTF16(data.Info[:], text)
	procShellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&data)))
}

func (a *trayApp) showMenu() {
	snapshot, _ := a.service.snapshot(time.Now().UTC())
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	status := a.lastTip
	if strings.HasPrefix(status, "Codex Auto Retry - ") {
		status = strings.TrimPrefix(status, "Codex Auto Retry - ")
	}
	appendTrayMenu(menu, mfGrayed, 0, status)
	appendTrayMenu(menu, mfSeparator, 0, "")
	appendTrayMenu(menu, mfString, menuOpenSettings, "打开设置…")
	pauseText := "暂停自动重试"
	if snapshot.Paused {
		pauseText = "恢复自动重试"
	}
	appendTrayMenu(menu, mfString, menuTogglePause, pauseText)
	appendTrayMenu(menu, mfSeparator, 0, "")
	appendTrayMenu(menu, mfString, menuExit, "退出")
	var point trayPoint
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&point)))
	procSetForegroundWindow.Call(a.hwnd)
	command, _, _ := procTrackPopupMenu.Call(menu, tpmRightButton|tpmReturnCmd, uintptr(point.X), uintptr(point.Y), 0, a.hwnd, 0)
	if command != 0 {
		a.handleCommand(uint16(command))
	}
	procPostMessage.Call(a.hwnd, wmNull, 0, 0)
}

func appendTrayMenu(menu uintptr, flags uint32, id uint16, label string) {
	var ptr *uint16
	if label != "" {
		ptr, _ = windows.UTF16PtrFromString(label)
	}
	procAppendMenu.Call(menu, uintptr(flags), uintptr(id), uintptr(unsafe.Pointer(ptr)))
}

func (a *trayApp) handleCommand(command uint16) {
	switch command {
	case menuOpenSettings:
		a.openSettings()
	case menuTogglePause:
		snapshot, err := a.service.snapshot(time.Now().UTC())
		if err == nil {
			_, _ = a.service.setPaused(!snapshot.Paused, time.Now().UTC())
		}
		a.refresh()
	case menuExit:
		a.cancel()
		procPostMessage.Call(a.hwnd, wmClose, 0, 0)
	}
}

func (a *trayApp) openSettings() {
	a.settingsMu.Lock()
	if a.settingsOpen {
		a.settingsMu.Unlock()
		return
	}
	a.settingsOpen = true
	a.settingsMu.Unlock()
	scriptPath, err := ensureSettingsScript(a.dataDir)
	if err != nil {
		a.logger.Printf("settings open failed category=settings_script")
		a.settingsFinished()
		return
	}
	config, err := loadOrCreateConfig(filepath.Join(a.dataDir, "config.json"))
	if err != nil {
		a.logger.Printf("settings open failed category=settings_config")
		a.settingsFinished()
		return
	}
	powerShell, err := resolvePowerShellExecutable(config.PowerShellExecutable)
	if err != nil {
		a.logger.Printf("settings open failed category=settings_shell")
		a.settingsFinished()
		return
	}
	executable, err := os.Executable()
	if err != nil {
		a.logger.Printf("settings open failed category=settings_executable")
		a.settingsFinished()
		return
	}
	arguments := []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-STA", "-File", scriptPath, "-DataDir", a.dataDir, "-Executable", executable}
	if os.Getenv("CODEX_AUTO_RETRY_SETTINGS_SMOKE") == "1" {
		arguments = append(arguments, "-SmokeTest")
	}
	command := exec.Command(powerShell, arguments...)
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	if err := command.Start(); err != nil {
		a.logger.Printf("settings open failed category=settings_start")
		a.settingsFinished()
		return
	}
	go func() {
		if err := command.Wait(); err != nil {
			a.logger.Printf("settings process stopped category=settings_process")
		}
		a.settingsFinished()
	}()
}

func (a *trayApp) settingsFinished() {
	a.settingsMu.Lock()
	a.settingsOpen = false
	a.settingsMu.Unlock()
}

func ensureSettingsScript(dataDir string) (string, error) {
	path := filepath.Join(dataDir, "settings.ps1")
	content := ensureUTF8BOM([]byte(settingsPowerShell))
	current, _ := os.ReadFile(path)
	if string(current) == string(content) {
		return path, nil
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func ensureUTF8BOM(content []byte) []byte {
	bom := []byte{0xef, 0xbb, 0xbf}
	if bytes.HasPrefix(content, bom) {
		return content
	}
	return append(bom, content...)
}

func copyUTF16(destination []uint16, value string) {
	encoded, _ := windows.UTF16FromString(value)
	if len(encoded) > len(destination) {
		encoded = encoded[:len(destination)]
	}
	copy(destination, encoded)
	if len(destination) > 0 {
		destination[len(destination)-1] = 0
	}
}
