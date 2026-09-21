package main

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestAuthLimitIsConfigurableAndKeepsHistoricalCount(t *testing.T) {
	if got := classifyFailure("auth_unavailable: no auth available", defaultConfig()); !got.Retry || got.Class != classAuthTransient {
		t.Fatalf("temporary auth outage was not classified as retryable: %+v", got)
	}
	cfg := isolatedConfig(t.TempDir())
	cfg.MaxRecoveryAttempts = 1000
	cfg.MaxConsecutiveRetries = 100
	cfg.AuthMaxAttempts = 6
	now := time.Now().UTC()
	d := newTestDaemon(t, cfg, successfulRunner())
	threadID := "019fa94e-0103-7183-b405-36bd307b6dca"
	thread := ThreadState{
		RecoveryAttempts: 19, ConsecutiveRetries: 19, RecoveryStartedAt: now.Add(-time.Minute),
		LastStartedTurnID: "failed", LastStartedAt: now.Add(-30 * time.Second),
	}
	d.state.Threads[threadID] = thread
	event := failureScannedEvent(threadID, "failed", now)
	event.Event.ErrorText = "401 unauthorized: login required"
	d.scheduleFailureLocked(event, "auth-limit-event", now, thread, 20, 20, now.Add(-30*time.Second), false)
	stopped := d.state.Threads[threadID].Stopped
	if stopped == nil || stopped.Reason != "auth_attempt_limit" {
		t.Fatalf("auth-specific stop reason missing: %+v", stopped)
	}
	if stopped.Attempts != 19 || stopped.MaxAttempts != 6 || stopped.ConsecutiveRetries != 19 || stopped.MaxConsecutive != 6 {
		t.Fatalf("historical counters were clamped or limits were wrong: %+v", stopped)
	}
}

func TestAuthSettingsRoundTripAndOmission(t *testing.T) {
	svc := newManagementService(t.TempDir())
	limit := 40
	settings := RetrySettings{RetryPrompt: "continue", MaxRecoveryAttempts: 1000, MaxConsecutiveRetries: 100,
		AuthMaxAttempts: &limit, InitialDelaySeconds: 5, MaxDelaySeconds: 300, DelayStrategy: delayStrategyFixed}
	now := time.Now().UTC()
	got, err := svc.setRetrySettings(settings, now)
	if err != nil || got.AuthMaxAttempts != 40 {
		t.Fatalf("MCP settings roundtrip: %+v %v", got, err)
	}
	settings.AuthMaxAttempts = nil
	got, err = svc.setRetrySettings(settings, now)
	if err != nil || got.AuthMaxAttempts != 40 {
		t.Fatalf("old client reset auth limit: %+v %v", got, err)
	}
	for _, invalid := range []int{0, -1, 1001} {
		before, err := os.ReadFile(svc.configPath)
		if err != nil {
			t.Fatal(err)
		}
		settings.AuthMaxAttempts = &invalid
		if _, err := svc.setRetrySettings(settings, now); err == nil {
			t.Fatalf("accepted %d", invalid)
		}
		after, _ := os.ReadFile(svc.configPath)
		if string(before) != string(after) {
			t.Fatal("invalid setting changed config")
		}
	}
	var payload RetrySettings
	if err := json.Unmarshal([]byte(`{"retry_prompt":"continue","max_recovery_attempts":1000,"max_consecutive_retries":100,"auth_max_attempts":1000,"initial_delay_seconds":5,"max_delay_seconds":300,"delay_strategy":"fixed"}`), &payload); err != nil {
		t.Fatal(err)
	}
	if err := svc.setLocalSettings(payload, false, now); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadOrCreateConfig(svc.configPath)
	if err != nil {
		t.Fatal(err)
	}
	a, c := retryLimits(classAuthLimited, cfg)
	if cfg.AuthMaxAttempts != 1000 || a != 1000 || c != 100 {
		t.Fatalf("saved limit does not control effective budgets: %+v %d/%d", cfg, a, c)
	}
}

func TestAuthStopReasonRespectsBindingLimit(t *testing.T) {
	cfg := defaultConfig()
	cfg.MaxRecoveryAttempts, cfg.MaxConsecutiveRetries, cfg.AuthMaxAttempts = 1000, 100, 6
	if got := retryStopReasonForClass(classAuthLimited, cfg, 7, 6, 7, 6); got != "auth_attempt_limit" {
		t.Fatal(got)
	}
	if got := retryStopReasonForClass(classAuthLimited, cfg, 4, 6, 7, 6); got != "auth_attempt_limit" {
		t.Fatal(got)
	}
	cfg.MaxRecoveryAttempts = 3
	if got := retryStopReasonForClass(classAuthLimited, cfg, 4, 3, 4, 6); got != "recovery_attempt_limit" {
		t.Fatal(got)
	}
	cfg.MaxRecoveryAttempts, cfg.MaxConsecutiveRetries = 1000, 2
	if got := retryStopReasonForClass(classAuthLimited, cfg, 3, 6, 3, 2); got != "consecutive_retry_limit" {
		t.Fatal(got)
	}
}

func TestAuthLoweredPendingLimitRetainsCountAndReason(t *testing.T) {
	cfg := isolatedConfig(t.TempDir())
	cfg.MaxRecoveryAttempts, cfg.MaxConsecutiveRetries, cfg.AuthMaxAttempts = 1000, 100, 50
	d := newTestDaemon(t, cfg, successfulRunner())
	const id = "019fa94e-0103-7183-b405-36bd307b6dca"
	d.state.Threads[id] = ThreadState{Pending: &PendingRetry{Class: classAuthLimited, Attempt: 20, ConsecutiveRetry: 20, MaxAttempts: 50, MaxConsecutive: 50}}
	cfg.AuthMaxAttempts = 6
	if err := writeJSONAtomic(d.dataDir+string(os.PathSeparator)+"config.json", cfg); err != nil {
		t.Fatal(err)
	}
	d.reloadConfigLocked()
	s := d.state.Threads[id].Stopped
	if s == nil || s.Attempts != 19 || s.ConsecutiveRetries != 19 || s.Reason != "auth_attempt_limit" {
		t.Fatalf("lowered config erased counters: %+v", s)
	}
	if err := d.persistStateLocked(); err != nil {
		t.Fatal(err)
	}
	state, err := loadState(d.statePath)
	if err != nil {
		t.Fatal(err)
	}
	if state.Threads[id].Stopped.Attempts != 19 {
		t.Fatal("state reload clamped historical count")
	}
	rows := managedRetries(state, time.Now().UTC())
	if len(rows) != 1 || rows[0].RecoveryAttempt != 19 || rows[0].StopReason != "auth_attempt_limit" {
		t.Fatalf("management lost count or reason: %+v", rows)
	}
}

func TestAuthLimitConfigRejectsInvalidValues(t *testing.T) {
	cfg := defaultConfig()
	cfg.AuthMaxAttempts = maxRecoveryAttemptsLimit + 1
	if err := cfg.validate(); err == nil {
		t.Fatal("auth_max_attempts above the supported range was accepted")
	}
}
