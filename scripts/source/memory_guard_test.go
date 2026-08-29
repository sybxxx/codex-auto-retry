package main

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestProcessMemoryGuardTriggersOnceAtLimit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	values := []uint64{10, 20, 30}
	index := 0
	var samples []memorySample
	limits := 0
	guard := newProcessMemoryGuard(25, time.Millisecond, func() (uint64, error) {
		mu.Lock()
		defer mu.Unlock()
		value := values[index]
		if index < len(values)-1 {
			index++
		}
		return value * 1024 * 1024, nil
	}, func(sample memorySample) { samples = append(samples, sample) }, func(memorySample) { limits++; cancel() })
	guard.Run(ctx)
	if limits != 1 {
		t.Fatalf("memory guard limit callback count = %d, want 1", limits)
	}
	if len(samples) != 3 {
		t.Fatalf("memory samples = %d, want 3", len(samples))
	}
}

func TestProcessMemoryGuardDoesNotRunWhenDisabled(t *testing.T) {
	called := false
	guard := newProcessMemoryGuard(0, time.Millisecond, func() (uint64, error) {
		called = true
		return 1, nil
	}, nil, func(memorySample) { t.Fatal("disabled memory guard triggered") })
	guard.Run(context.Background())
	if called {
		t.Fatal("disabled memory guard read process memory")
	}
}

func TestMemoryBytesToMBRoundsUp(t *testing.T) {
	if got := memoryBytesToMB(1024*1024 + 1); got != 2 {
		t.Fatalf("memoryBytesToMB rounded down: %d", got)
	}
}
