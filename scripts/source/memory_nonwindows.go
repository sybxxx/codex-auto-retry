//go:build !windows

package main

import "runtime"

func currentProcessPrivateBytes() (uint64, error) {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats.Sys, nil
}

func showMemoryLimitAlert(memorySample, int) {}
