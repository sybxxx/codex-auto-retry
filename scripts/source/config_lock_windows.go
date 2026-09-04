//go:build windows

package main

import (
	"errors"
	"fmt"
	"syscall"
	"time"
)

const configLockSharingViolation = syscall.Errno(32)

func acquireConfigFileLock(path string, timeout time.Duration) (*instanceLock, error) {
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		handle, openErr := syscall.CreateFile(
			pathPtr,
			syscall.GENERIC_READ|syscall.GENERIC_WRITE,
			0,
			nil,
			syscall.OPEN_ALWAYS,
			syscall.FILE_ATTRIBUTE_NORMAL,
			0,
		)
		if openErr == nil {
			return &instanceLock{handle: handle}, nil
		}
		if !errors.Is(openErr, configLockSharingViolation) || time.Now().After(deadline) {
			return nil, fmt.Errorf("config file is locked by another process: %w", openErr)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
