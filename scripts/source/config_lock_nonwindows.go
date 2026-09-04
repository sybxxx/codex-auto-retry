//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

func acquireConfigFileLock(path string, timeout time.Duration) (*instanceLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return &instanceLock{file: file}, nil
		} else if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			_ = file.Close()
			return nil, fmt.Errorf("config file is locked by another process: %w", err)
		}
		if time.Now().After(deadline) {
			_ = file.Close()
			return nil, fmt.Errorf("config file lock timed out")
		}
		time.Sleep(25 * time.Millisecond)
	}
}
