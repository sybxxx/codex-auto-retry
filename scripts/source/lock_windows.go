//go:build windows

package main

import (
	"fmt"
	"syscall"
)

type instanceLock struct {
	handle syscall.Handle
}

func acquireInstanceLock(path string) (*instanceLock, error) {
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(
		pathPtr,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		0,
		nil,
		syscall.OPEN_ALWAYS,
		syscall.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("another watchdog instance is already running: %w", err)
	}
	return &instanceLock{handle: handle}, nil
}

func (l *instanceLock) Close() error {
	if l == nil || l.handle == 0 {
		return nil
	}
	err := syscall.CloseHandle(l.handle)
	l.handle = 0
	return err
}
