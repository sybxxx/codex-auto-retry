package main

import (
	"context"
	"sync/atomic"
	"time"
)

const memoryCheckInterval = 5 * time.Second

type memorySample struct {
	PrivateBytes uint64
	CheckedAt    time.Time
}

type processMemoryReader func() (uint64, error)

// processMemoryGuard is deliberately independent from the daemon ticker. A
// blocked session scan or provider request must not prevent the safety limit
// from being enforced.
type processMemoryGuard struct {
	limitBytes uint64
	interval   time.Duration
	read       processMemoryReader
	onSample   func(memorySample)
	onLimit    func(memorySample)
	triggered  atomic.Bool
}

func newProcessMemoryGuard(limitMB int, interval time.Duration, read processMemoryReader, onSample, onLimit func(memorySample)) *processMemoryGuard {
	if read == nil {
		read = currentProcessPrivateBytes
	}
	if interval <= 0 {
		interval = memoryCheckInterval
	}
	var limitBytes uint64
	if limitMB > 0 {
		limitBytes = uint64(limitMB) * 1024 * 1024
	}
	return &processMemoryGuard{
		limitBytes: limitBytes,
		interval:   interval,
		read:       read,
		onSample:   onSample,
		onLimit:    onLimit,
	}
}

func (g *processMemoryGuard) Run(ctx context.Context) {
	if g == nil || g.limitBytes == 0 || g.read == nil {
		return
	}
	check := func() bool {
		bytes, err := g.read()
		if err != nil {
			return false
		}
		sample := memorySample{PrivateBytes: bytes, CheckedAt: time.Now().UTC()}
		if g.onSample != nil {
			g.onSample(sample)
		}
		if bytes < g.limitBytes || !g.triggered.CompareAndSwap(false, true) {
			return false
		}
		if g.onLimit != nil {
			g.onLimit(sample)
		}
		return true
	}
	if check() {
		return
	}
	ticker := time.NewTicker(g.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if check() {
				return
			}
		}
	}
}

func memoryBytesToMB(bytes uint64) int64 {
	if bytes == 0 {
		return 0
	}
	return int64((bytes + 1024*1024 - 1) / (1024 * 1024))
}
