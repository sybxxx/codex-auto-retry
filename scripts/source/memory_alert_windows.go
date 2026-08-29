//go:build windows

package main

import (
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const memoryAlertTimeout = 15 * time.Second

func showMemoryLimitAlert(sample memorySample, limitMB int) {
	title, titleErr := windows.UTF16PtrFromString("Codex Auto Retry 已自动停止")
	message, messageErr := windows.UTF16PtrFromString(fmt.Sprintf(
		"后台进程私有内存已达到 %d MB，超过设定上限 %d MB。\n\n已自动停止自动重试服务，Codex 和任务数据未被删除。请关闭其他异常进程后，从启动管理器重新启动服务。",
		memoryBytesToMB(sample.PrivateBytes), limitMB,
	))
	if titleErr != nil || messageErr != nil {
		return
	}
	user32 := windows.NewLazySystemDLL("user32.dll")
	// MessageBoxTimeoutW is available on supported Windows versions and keeps a
	// safety alert from holding the worker alive forever when unattended.
	timeoutProc := user32.NewProc("MessageBoxTimeoutW")
	result, _, _ := timeoutProc.Call(
		0,
		uintptr(unsafe.Pointer(message)),
		uintptr(unsafe.Pointer(title)),
		0x30|0x10000,
		0,
		uintptr(memoryAlertTimeout.Milliseconds()),
	)
	if result != 0 {
		return
	}
	// Older or restricted hosts may not export MessageBoxTimeoutW. Fall back to
	// a normal box in a bounded goroutine; process shutdown still wins.
	done := make(chan struct{})
	go func() {
		box := user32.NewProc("MessageBoxW")
		box.Call(0, uintptr(unsafe.Pointer(message)), uintptr(unsafe.Pointer(title)), 0x30|0x10000)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(memoryAlertTimeout):
	}
}
