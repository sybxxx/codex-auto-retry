//go:build !windows

package main

import (
	"fmt"
	"runtime"
)

func currentProcessPrivateBytes() (uint64, error) {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats.Sys, nil
}

func processPrivateBytes(pid int) (uint64, error) {
	if pid <= 0 {
		return 0, fmt.Errorf("invalid process id: %d", pid)
	}
	return 0, fmt.Errorf("process memory inspection is unavailable")
}

func showMemoryLimitAlert(memorySample, int) {}
