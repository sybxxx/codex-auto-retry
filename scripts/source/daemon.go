package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

var errStopRequested = errors.New("stop requested")

type daemon struct {
	config      Config
	dataDir     string
	statePath   string
	statusPath  string
	stopPath    string
	controlPath string
	commandDir  string
	logger      *safeLogger
	runner      resumeRunner
	startedAt   time.Time

	mu                  sync.Mutex
	wg                  sync.WaitGroup
	state               RuntimeState
	active              map[string]RetryJob
	activeCtx           map[string]context.CancelFunc
	lastScan            time.Time
	lastError           string
	controllerState     string
	lastControllerProbe time.Time
	paused              bool
	writeState          func(string, any) error
	writeStatusFile     func(string, any) error
	stateWriteDeferred  bool
	statusWriteDeferred bool
}

func newDaemon(config Config, dataDir string, logger *safeLogger, runner resumeRunner) (*daemon, error) {
	configPath := filepath.Join(dataDir, "config.json")
	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		if err := writeJSONAtomic(configPath, config); err != nil {
			return nil, err
		}
	}
	statePath := filepath.Join(dataDir, "state.json")
	state, err := loadState(statePath)
	if err != nil {
		return nil, err
	}
	controlPath := filepath.Join(dataDir, "control.json")
	control, err := loadOrCreateControlState(controlPath)
	if err != nil {
		return nil, err
	}
	daemon := &daemon{
		config:          config,
		dataDir:         dataDir,
		statePath:       statePath,
		statusPath:      filepath.Join(dataDir, "status.json"),
		stopPath:        filepath.Join(dataDir, "stop.signal"),
		controlPath:     controlPath,
		commandDir:      filepath.Join(dataDir, "commands"),
		logger:          logger,
		runner:          runner,
		startedAt:       time.Now().UTC(),
		state:           state,
		active:          make(map[string]RetryJob),
		activeCtx:       make(map[string]context.CancelFunc),
		paused:          control.Paused,
		controllerState: "starting",
		writeState:      writeJSONAtomic,
		writeStatusFile: writeJSONAtomic,
	}
	daemon.reconcileStartupState(time.Now().UTC())
	return daemon, nil
}

func (d *daemon) run(ctx context.Context) error {
	_ = os.Remove(d.stopPath)
	if err := d.tick(ctx, time.Now().UTC()); err != nil {
		return err
	}
	ticker := time.NewTicker(time.Duration(d.config.PollIntervalSeconds) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-ticker.C:
			if err := d.tick(ctx, now.UTC()); err != nil {
				return err
			}
		}
	}
}

func (d *daemon) tick(ctx context.Context, now time.Time) error {
	if _, err := os.Stat(d.stopPath); err == nil {
		_ = os.Remove(d.stopPath)
		return errStopRequested
	}

	controllerReady := d.controllerRestartReady(ctx, now)
	d.mu.Lock()
	if controllerReady {
		d.reopenRestartRequiredLocked(now)
	}
	d.reloadConfigLocked()
	roots := discoverSessionRoots(d.config)
	baseline := !d.state.Initialized
	events, scanErr := scanSessions(roots, &d.state, now, baseline)
	if baseline {
		d.state.Initialized = true
		d.logger.Printf("initialized session baseline roots=%d", len(roots))
	}
	if scanErr != nil {
		d.lastError = scanErr.Error()
		d.logger.Printf("scan failed category=session_scan")
	} else {
		d.lastError = ""
	}
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].Event.Timestamp.Before(events[j].Event.Timestamp)
	})
	for _, item := range events {
		// Ignore old history only when another watched store already owns this
		// thread. Failures written while the watchdog was stopped must survive a
		// restart.
		if item.Mirrored && item.Event.Timestamp.Before(d.startedAt.Add(-3*time.Second)) {
			continue
		}
		d.handleEventLocked(item, now)
	}
	d.refreshControlsLocked(now)
	d.expireUnacknowledgedLocked(now)
	// Acknowledged turns normally finish through rollout events. If a previous
	// process stopped after task_started, release the mutex while the optional
	// app-server lifecycle probe verifies that the turn is still active. The
	// probe never starts a task and its result is revalidated under the lock.
	d.mu.Unlock()
	d.reconcileAwaitingLifecycle(ctx, now)
	d.mu.Lock()
	jobs := d.dispatchDueLocked(now)
	d.lastScan = now
	d.state.prune(now)
	stateErr := d.persistStateLocked()
	statusErr := d.writeStatusLocked(true, len(roots))
	d.mu.Unlock()

	for _, job := range jobs {
		d.wg.Add(1)
		go d.runJob(ctx, job)
	}
	if scanErr != nil {
		return nil
	}
	if stateErr != nil {
		// The in-memory state remains authoritative. Windows indexers, security
		// scanners, and settings readers can briefly hold state.json without
		// delete sharing, so one failed atomic replacement must not terminate the
		// watchdog and remove its tray icon. The next tick retries persistence.
		d.logger.Printf("state save deferred category=state_write")
	}
	if statusErr != nil {
		d.logger.Printf("status save deferred category=status_write")
	}
	return nil
}

func (d *daemon) persistStateLocked() error {
	writer := d.writeState
	if writer == nil {
		writer = writeJSONAtomic
	}
	if err := writer(d.statePath, d.state); err != nil {
		d.stateWriteDeferred = true
		d.lastError = "state_write_deferred"
		return err
	}
	if d.stateWriteDeferred {
		d.stateWriteDeferred = false
		if d.lastError == "state_write_deferred" {
			d.lastError = ""
		}
		d.logger.Printf("state save recovered category=state_write")
	}
	return nil
}

func (d *daemon) controllerRestartReady(ctx context.Context, now time.Time) bool {
	runner, ok := d.runner.(controllerStateRunner)
	if !ok {
		return false
	}
	d.mu.Lock()
	// A stale failure state must clear even after every stopped task was
	// resolved; keying the probe off the queue alone left
	// "codex_restart_required" displayed forever once no retry was waiting.
	// Shared mode is a resident service boundary, not only a retry dependency.
	// Keep probing it even when the retry queue is empty so an app-server that
	// exits after login is recreated before Codex receives a dead endpoint.
	needsProbe := d.config.SharedAppServerEnabled ||
		d.controllerState == "codex_restart_required" ||
		d.controllerState == "codex_not_running"
	if !needsProbe {
		for _, thread := range d.state.Threads {
			if thread.Stopped != nil && (thread.Stopped.Reason == "codex_restart_required" ||
				thread.Stopped.Reason == "codex_not_running") {
				needsProbe = true
				break
			}
		}
	}
	if !needsProbe || (!d.lastControllerProbe.IsZero() && now.Sub(d.lastControllerProbe) < 10*time.Second) {
		d.mu.Unlock()
		return false
	}
	d.lastControllerProbe = now
	d.mu.Unlock()
	state := runner.ControllerState(ctx)
	d.mu.Lock()
	d.controllerState = state
	d.mu.Unlock()
	return state == "ready"
}

func (d *daemon) reopenRestartRequiredLocked(now time.Time) {
	for threadID, thread := range d.state.Threads {
		stopped := thread.Stopped
		if stopped == nil || stopped.Reason != "codex_restart_required" {
			continue
		}
		attempt := stopped.Attempts + 1
		consecutive := stopped.ConsecutiveRetries + 1
		if attempt > stopped.MaxAttempts || consecutive > stopped.MaxConsecutive {
			continue
		}
		thread.Stopped = nil
		thread.RecoveryAttempts = attempt
		thread.ConsecutiveRetries = consecutive
		thread.Pending = &PendingRetry{
			EventKey: stopped.EventKey, FailedTurnID: stopped.FailedTurnID, FailedAt: stopped.FailedAt,
			OriginTurnStartedAt: stopped.OriginTurnStartedAt,
			Class:               stopped.Class, DueAt: now, CodexHome: stopped.CodexHome, RolloutPath: stopped.RolloutPath,
			Attempt: attempt, MaxAttempts: stopped.MaxAttempts,
			ConsecutiveRetry: consecutive, MaxConsecutive: stopped.MaxConsecutive,
		}
		d.state.Threads[threadID] = thread
		d.logger.Printf("retry reopened thread=%s reason=codex_restart_completed", shortThreadID(threadID))
	}
}

func (d *daemon) refreshControlsLocked(now time.Time) {
	control, err := loadOrCreateControlState(d.controlPath)
	if err != nil {
		d.lastError = err.Error()
		d.logger.Printf("control state failed category=control_state")
		return
	}
	d.paused = control.Paused
	commands, invalid, err := loadControlCommandFiles(d.commandDir)
	if err != nil {
		d.lastError = err.Error()
		d.logger.Printf("control commands failed category=control_commands")
		return
	}
	for _, path := range invalid {
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			d.lastError = removeErr.Error()
		}
		d.logger.Printf("control command discarded category=invalid_command")
	}
	for _, item := range commands {
		d.applyControlCommandLocked(item.Command, now)
		if removeErr := os.Remove(item.Path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			d.lastError = removeErr.Error()
			d.logger.Printf("control command cleanup failed category=control_commands")
		}
	}
}

func (d *daemon) expireUnacknowledgedLocked(now time.Time) {
	for threadID, thread := range d.state.Threads {
		if thread.Awaiting == nil || thread.Awaiting.RetryTurnID != "" || thread.Awaiting.StartDeadline.After(now) {
			continue
		}
		if _, active := d.active[threadID]; active {
			continue
		}
		d.rescheduleAwaitingLocked(threadID, thread, now, "start_not_acknowledged")
	}
}

const (
	awaitingLifecycleConfirmations = 2
	awaitingLifecycleProbeInterval = 5 * time.Second
)

type awaitingLifecycleCandidate struct {
	threadID  string
	eventKey  string
	turnID    string
	codexHome string
}

// reconcileAwaitingLifecycle repairs the narrow crash window after Codex has
// acknowledged a retry turn but before its task_complete event reaches the
// rollout file. An active turn remains untouched. A non-active result must be
// observed twice, separated by a probe interval, before the retry is advanced;
// this prevents a transient app-server status from creating a duplicate turn.
func (d *daemon) reconcileAwaitingLifecycle(ctx context.Context, now time.Time) {
	reader, ok := d.runner.(retryLifecycleReader)
	if !ok {
		return
	}

	d.mu.Lock()
	candidates := make([]awaitingLifecycleCandidate, 0)
	for threadID, thread := range d.state.Threads {
		awaiting := thread.Awaiting
		if awaiting == nil || awaiting.RetryTurnID == "" {
			continue
		}
		startedAt := awaiting.StartedAt
		if startedAt.IsZero() {
			startedAt = awaiting.DispatchStartedAt
		}
		if startedAt.IsZero() || now.Sub(startedAt) < time.Duration(d.config.StartAckTimeoutSeconds)*time.Second {
			continue
		}
		if !awaiting.LastLifecycleCheckAt.IsZero() && now.Sub(awaiting.LastLifecycleCheckAt) < awaitingLifecycleProbeInterval {
			continue
		}
		awaiting.LastLifecycleCheckAt = now
		thread.Awaiting = awaiting
		d.state.Threads[threadID] = thread
		candidates = append(candidates, awaitingLifecycleCandidate{
			threadID: threadID, eventKey: awaiting.EventKey, turnID: awaiting.RetryTurnID,
			codexHome: awaiting.CodexHome,
		})
	}
	d.mu.Unlock()

	for _, candidate := range candidates {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		status, err := reader.RetryThreadStatus(probeCtx, candidate.threadID, candidate.codexHome)
		cancel()

		d.mu.Lock()
		thread, found := d.state.Threads[candidate.threadID]
		awaiting := thread.Awaiting
		if !found || awaiting == nil || awaiting.EventKey != candidate.eventKey || awaiting.RetryTurnID != candidate.turnID {
			d.mu.Unlock()
			continue
		}
		if err != nil {
			reason := controllerFailureReason(DispatchResult{}, err)
			d.controllerState = reason
			nextFailures := awaiting.DispatchFailures + 1
			if controllerFailureNeedsAction(reason) || nextFailures >= d.config.ControllerFailureLimit {
				d.stopAwaitingForControllerLocked(candidate.threadID, thread, now, reason)
				d.mu.Unlock()
				continue
			}
			// A transient controller/read failure is not evidence that the turn
			// stopped. Keep the inactive confirmation count intact, but bound
			// repeated probe failures using the same local-controller limit as
			// initial dispatch.
			awaiting.DispatchFailures = nextFailures
			thread.Awaiting = awaiting
			d.state.Threads[candidate.threadID] = thread
			d.logger.Printf("retry lifecycle probe deferred thread=%s reason=%s controller_failures=%d/%d", shortThreadID(candidate.threadID), reason, nextFailures, d.config.ControllerFailureLimit)
			d.mu.Unlock()
			continue
		}
		d.controllerState = "ready"
		awaiting.DispatchFailures = 0
		if retryThreadIsActive(status) {
			awaiting.LifecycleChecks = 0
			thread.Awaiting = awaiting
			d.state.Threads[candidate.threadID] = thread
			d.mu.Unlock()
			continue
		}
		awaiting.LifecycleChecks++
		thread.Awaiting = awaiting
		if awaiting.LifecycleChecks < awaitingLifecycleConfirmations {
			d.state.Threads[candidate.threadID] = thread
			d.logger.Printf("retry lifecycle inactive confirmation thread=%s status=%s confirmation=%d/%d", shortThreadID(candidate.threadID), status, awaiting.LifecycleChecks, awaitingLifecycleConfirmations)
			d.mu.Unlock()
			continue
		}
		d.advanceInactiveAwaitingLocked(candidate.threadID, thread, now, status)
		d.mu.Unlock()
	}
}

func retryThreadIsActive(status string) bool {
	switch status {
	case "active", "running", "in_progress":
		return true
	default:
		return false
	}
}

func (d *daemon) advanceInactiveAwaitingLocked(threadID string, thread ThreadState, now time.Time, status string) {
	awaiting := thread.Awaiting
	if awaiting == nil {
		return
	}
	nextAttempt := awaiting.Attempt + 1
	nextConsecutive := awaiting.ConsecutiveRetry + 1
	if thread.CurrentTurnProgress {
		nextConsecutive = 1
	}
	if nextAttempt > awaiting.MaxAttempts || nextConsecutive > awaiting.MaxConsecutive {
		completedAttempts := completedRetryCount(nextAttempt, awaiting.MaxAttempts)
		completedConsecutive := completedRetryCount(nextConsecutive, awaiting.MaxConsecutive)
		reason := retryStopReason(nextAttempt, awaiting.MaxAttempts, nextConsecutive, awaiting.MaxConsecutive)
		if awaiting.Class == classEmptyResponse && thread.GoalStatus == "active" {
			reason = goalEmptyResponseStopReason
		}
		thread.Pending = nil
		thread.Awaiting = nil
		thread.RecoveryAttempts = completedAttempts
		thread.ConsecutiveRetries = completedConsecutive
		thread.CurrentTurnProgress = false
		historicalSince := awaiting.StartedAt
		if historicalSince.IsZero() {
			historicalSince = awaiting.DispatchStartedAt
		}
		thread.Stopped = &StoppedRetry{
			EventKey: awaiting.EventKey, FailedTurnID: awaiting.FailedTurnID, FailedAt: awaiting.FailedAt,
			OriginTurnStartedAt: awaiting.OriginTurnStartedAt,
			Class:               awaiting.Class, StoppedAt: now, CodexHome: awaiting.CodexHome, RolloutPath: awaiting.RolloutPath,
			Attempts: completedAttempts, MaxAttempts: awaiting.MaxAttempts,
			ConsecutiveRetries: completedConsecutive, MaxConsecutive: awaiting.MaxConsecutive, Reason: reason,
			Historical: !historicalSince.IsZero() && now.Sub(historicalSince) > stoppedRetryDisplayWindow,
		}
		if reason == goalEmptyResponseStopReason {
			thread.GoalStop = &GoalStopRequest{EventKey: awaiting.EventKey, Reason: reason, RequestedAt: now, DueAt: now}
		}
		d.state.Threads[threadID] = thread
		d.logger.Printf("retry lifecycle stopped thread=%s status=%s recovery_attempts=%d consecutive_retries=%d reason=%s", shortThreadID(threadID), status, completedAttempts, completedConsecutive, reason)
		return
	}

	thread.RecoveryAttempts = nextAttempt
	thread.ConsecutiveRetries = nextConsecutive
	thread.CurrentTurnProgress = false
	thread.Awaiting = nil
	thread.Stopped = nil
	thread.GoalStop = nil
	thread.Pending = &PendingRetry{
		EventKey: awaiting.EventKey, FailedTurnID: awaiting.FailedTurnID, FailedAt: awaiting.FailedAt,
		OriginTurnStartedAt: awaiting.OriginTurnStartedAt, Class: awaiting.Class,
		DueAt: now.Add(retryDelay(nextConsecutive, d.config)), CodexHome: awaiting.CodexHome, RolloutPath: awaiting.RolloutPath,
		Attempt: nextAttempt, MaxAttempts: awaiting.MaxAttempts,
		ConsecutiveRetry: nextConsecutive, MaxConsecutive: awaiting.MaxConsecutive,
		DispatchFailures: awaiting.DispatchFailures, ParentNotified: awaiting.ParentNotified,
		GoalLimitRestart: awaiting.GoalLimitRestart,
	}
	d.state.Threads[threadID] = thread
	d.logger.Printf("retry lifecycle rescheduled thread=%s status=%s attempt=%d consecutive_retry=%d", shortThreadID(threadID), status, nextAttempt, nextConsecutive)
}

func (d *daemon) rescheduleAwaitingLocked(threadID string, thread ThreadState, now time.Time, reason string) {
	d.rescheduleAwaitingWithPolicyLocked(threadID, thread, now, reason, true)
}

func (d *daemon) rescheduleAwaitingWithPolicyLocked(threadID string, thread ThreadState, now time.Time, reason string, countFailure bool) {
	awaiting := thread.Awaiting
	if awaiting == nil {
		return
	}
	dispatchFailures := awaiting.DispatchFailures
	if countFailure {
		dispatchFailures++
	}
	if controllerFailureNeedsAction(reason) || (countFailure && dispatchFailures >= d.config.ControllerFailureLimit) {
		d.stopAwaitingForControllerLocked(threadID, thread, now, reason)
		return
	}
	delayIndex := dispatchFailures
	if delayIndex < 1 {
		delayIndex = 1
	}
	delay := retryDelay(delayIndex, d.config)
	thread.Awaiting = nil
	thread.Pending = &PendingRetry{
		EventKey:            awaiting.EventKey,
		FailedTurnID:        awaiting.FailedTurnID,
		FailedAt:            awaiting.FailedAt,
		OriginTurnStartedAt: awaiting.OriginTurnStartedAt,
		Class:               awaiting.Class,
		DueAt:               now.Add(delay),
		CodexHome:           awaiting.CodexHome,
		RolloutPath:         awaiting.RolloutPath,
		Attempt:             awaiting.Attempt,
		MaxAttempts:         awaiting.MaxAttempts,
		ConsecutiveRetry:    awaiting.ConsecutiveRetry,
		MaxConsecutive:      awaiting.MaxConsecutive,
		DispatchFailures:    dispatchFailures,
		ParentNotified:      awaiting.ParentNotified,
		GoalLimitRestart:    awaiting.GoalLimitRestart,
	}
	d.state.Threads[threadID] = thread
	d.logger.Printf("retry rescheduled thread=%s reason=%s delay_seconds=%d", shortThreadID(threadID), reason, int(delay.Seconds()))
}

func (d *daemon) stopAwaitingForControllerLocked(threadID string, thread ThreadState, now time.Time, reason string) {
	awaiting := thread.Awaiting
	if awaiting == nil {
		return
	}
	thread.Pending = nil
	thread.Awaiting = nil
	thread.GoalStop = nil
	thread.RecoveryAttempts = completedRetryCount(awaiting.Attempt, awaiting.MaxAttempts)
	thread.ConsecutiveRetries = completedRetryCount(awaiting.ConsecutiveRetry, awaiting.MaxConsecutive)
	thread.Stopped = &StoppedRetry{
		EventKey: awaiting.EventKey, FailedTurnID: awaiting.FailedTurnID, FailedAt: awaiting.FailedAt,
		OriginTurnStartedAt: awaiting.OriginTurnStartedAt,
		Class:               awaiting.Class, StoppedAt: now, CodexHome: awaiting.CodexHome, RolloutPath: awaiting.RolloutPath,
		Attempts: thread.RecoveryAttempts, MaxAttempts: awaiting.MaxAttempts,
		ConsecutiveRetries: thread.ConsecutiveRetries, MaxConsecutive: awaiting.MaxConsecutive, Reason: reason,
	}
	d.state.Threads[threadID] = thread
	d.lastError = reason
	d.logger.Printf("retry stopped thread=%s reason=%s controller_failures=%d", shortThreadID(threadID), reason, awaiting.DispatchFailures+1)
}

func (d *daemon) stopPendingForControllerLocked(threadID string, thread ThreadState, now time.Time, reason string) {
	pending := thread.Pending
	if pending == nil {
		return
	}
	thread.Pending = nil
	thread.Awaiting = nil
	thread.GoalStop = nil
	thread.RecoveryAttempts = completedRetryCount(pending.Attempt, pending.MaxAttempts)
	thread.ConsecutiveRetries = completedRetryCount(pending.ConsecutiveRetry, pending.MaxConsecutive)
	thread.CurrentTurnProgress = false
	thread.Stopped = &StoppedRetry{
		EventKey: pending.EventKey, FailedTurnID: pending.FailedTurnID, FailedAt: pending.FailedAt,
		OriginTurnStartedAt: pending.OriginTurnStartedAt,
		Class:               pending.Class, StoppedAt: now, CodexHome: pending.CodexHome, RolloutPath: pending.RolloutPath,
		Attempts: thread.RecoveryAttempts, MaxAttempts: pending.MaxAttempts,
		ConsecutiveRetries: thread.ConsecutiveRetries, MaxConsecutive: pending.MaxConsecutive,
		Reason: reason,
	}
	d.state.Threads[threadID] = thread
	d.lastError = reason
	d.logger.Printf("retry stopped before dispatch thread=%s reason=%s", shortThreadID(threadID), reason)
}

func controllerFailureNeedsAction(reason string) bool {
	switch reason {
	case "codex_not_running", "codex_restart_required", "codex_home_not_shared", "shared_app_server_port_reserved", "shared_app_server_port_conflict", "shared_app_server_environment_conflict", "shared_app_server_disabled":
		return true
	default:
		return false
	}
}

func controllerFailureNeedsFailOpen(reason string) bool {
	switch reason {
	case "shared_app_server_port_reserved", "shared_app_server_port_conflict", "shared_app_server_environment_conflict",
		"codex_background_channel_unavailable", "codex_background_dispatch_failed",
		"controller_timeout", "controller_invalid_result", "controller_unavailable":
		return true
	default:
		return false
	}
}

func (d *daemon) dispatchDueLocked(now time.Time) []RetryJob {
	available := d.config.MaxParallelRetries - len(d.active)
	if available <= 0 {
		return nil
	}
	jobs := make([]RetryJob, 0, available)
	type goalStopCandidate struct {
		threadID string
		request  GoalStopRequest
	}
	goalStops := make([]goalStopCandidate, 0)
	for threadID, thread := range d.state.Threads {
		if thread.GoalStop == nil || thread.GoalStop.DueAt.After(now) {
			continue
		}
		if _, active := d.active[threadID]; active {
			continue
		}
		goalStops = append(goalStops, goalStopCandidate{threadID: threadID, request: *thread.GoalStop})
	}
	sort.Slice(goalStops, func(i, j int) bool { return goalStops[i].request.DueAt.Before(goalStops[j].request.DueAt) })
	for _, item := range goalStops {
		if available == 0 {
			break
		}
		thread := d.state.Threads[item.threadID]
		job := RetryJob{Kind: jobGoalBlock, ThreadID: item.threadID, EventKey: item.request.EventKey,
			DispatchFailures: item.request.DispatchFailures}
		if thread.Stopped != nil {
			job.Class = thread.Stopped.Class
			job.Attempt = thread.Stopped.Attempts
			job.CodexHome = thread.Stopped.CodexHome
			job.RolloutPath = thread.Stopped.RolloutPath
		}
		d.active[item.threadID] = job
		jobs = append(jobs, job)
		available--
	}
	if available == 0 {
		return jobs
	}
	if d.paused {
		return jobs
	}
	if !d.config.SharedAppServerEnabled || d.controllerState == "shared_app_server_disabled" {
		// The fail-open mode deliberately has no recovery transport. Do not
		// promote a due item to AwaitingRetry: doing so looks like a retry was
		// started and then failed, even though Codex never received a request.
		stopReason := "shared_app_server_disabled"
		if controllerFailureNeedsFailOpen(d.controllerState) {
			stopReason = d.controllerState
		}
		for threadID, thread := range d.state.Threads {
			if thread.Pending == nil || thread.Pending.DueAt.After(now) || thread.Awaiting != nil {
				continue
			}
			d.stopPendingForControllerLocked(threadID, thread, now, stopReason)
		}
		return jobs
	}
	type candidate struct {
		threadID string
		pending  PendingRetry
	}
	candidates := make([]candidate, 0)
	for threadID, thread := range d.state.Threads {
		if thread.GoalHeld && !goalLimitRestartPending(thread) && !heldConversationStillAllowed(thread, thread.GoalStatus, thread.GoalUpdatedAt) {
			if thread.Pending != nil || thread.Awaiting != nil {
				thread.Pending = nil
				thread.Awaiting = nil
				thread.RecoveryAttempts = 0
				thread.ConsecutiveRetries = 0
				thread.CurrentTurnProgress = false
				thread.Stopped = nil
				d.state.Threads[threadID] = thread
			}
			continue
		}
		if thread.Pending == nil || thread.Pending.DueAt.After(now) || thread.Awaiting != nil {
			continue
		}
		if _, active := d.active[threadID]; active {
			continue
		}
		candidates = append(candidates, candidate{threadID: threadID, pending: *thread.Pending})
	}
	if len(candidates) == 0 {
		return jobs
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].pending.DueAt.Before(candidates[j].pending.DueAt)
	})
	if available > len(candidates) {
		available = len(candidates)
	}
	for _, item := range candidates[:available] {
		thread := d.state.Threads[item.threadID]
		thread.Pending = nil
		thread.LastAutoRetryAt = now
		thread.Awaiting = &AwaitingRetry{
			EventKey:            item.pending.EventKey,
			FailedTurnID:        item.pending.FailedTurnID,
			FailedAt:            item.pending.FailedAt,
			OriginTurnStartedAt: item.pending.OriginTurnStartedAt,
			Class:               item.pending.Class,
			Action:              actionDispatching,
			Attempt:             item.pending.Attempt,
			MaxAttempts:         item.pending.MaxAttempts,
			ConsecutiveRetry:    item.pending.ConsecutiveRetry,
			MaxConsecutive:      item.pending.MaxConsecutive,
			DispatchFailures:    item.pending.DispatchFailures,
			ParentNotified:      item.pending.ParentNotified,
			GoalLimitRestart:    item.pending.GoalLimitRestart,
			DispatchStartedAt:   now,
			StartDeadline:       now.Add(time.Duration(d.config.StartAckTimeoutSeconds) * time.Second),
			CodexHome:           item.pending.CodexHome,
			RolloutPath:         item.pending.RolloutPath,
		}
		d.state.Threads[item.threadID] = thread
		job := RetryJob{
			Kind:                jobRecovery,
			ThreadID:            item.threadID,
			FailedTurnID:        item.pending.FailedTurnID,
			FailedAt:            item.pending.FailedAt,
			OriginTurnStartedAt: item.pending.OriginTurnStartedAt,
			EventKey:            item.pending.EventKey,
			Class:               item.pending.Class,
			CodexHome:           item.pending.CodexHome,
			RolloutPath:         item.pending.RolloutPath,
			Attempt:             item.pending.Attempt,
			MaxAttempts:         item.pending.MaxAttempts,
			ConsecutiveRetry:    item.pending.ConsecutiveRetry,
			MaxConsecutive:      item.pending.MaxConsecutive,
			DispatchFailures:    item.pending.DispatchFailures,
			ParentNotified:      item.pending.ParentNotified,
			GoalLimitRestart:    item.pending.GoalLimitRestart,
			RecoveryEventID:     recoveryEventID(item.threadID, item.pending.EventKey),
		}
		d.active[item.threadID] = job
		jobs = append(jobs, job)
	}
	return jobs
}

func (d *daemon) runJob(ctx context.Context, job RetryJob) {
	defer d.wg.Done()
	if job.Kind == jobGoalBlock {
		d.runGoalBlockJob(ctx, job)
		return
	}
	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	d.mu.Lock()
	thread := d.state.Threads[job.ThreadID]
	if thread.Awaiting == nil || thread.Awaiting.EventKey != job.EventKey ||
		thread.Awaiting.Attempt != job.Attempt ||
		(thread.GoalHeld && !job.GoalLimitRestart && !heldConversationStillAllowed(thread, thread.GoalStatus, thread.GoalUpdatedAt)) {
		delete(d.active, job.ThreadID)
		d.mu.Unlock()
		d.logger.Printf("retry action ignored thread=%s reason=stale_job", shortThreadID(job.ThreadID))
		return
	}
	d.activeCtx[job.ThreadID] = cancel
	d.mu.Unlock()
	d.logger.Printf("retry action started thread=%s category=%s attempt=%d", shortThreadID(job.ThreadID), job.Class, job.Attempt)
	result, err := d.runner.Resume(jobCtx, job)
	finishedAt := time.Now().UTC()

	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.active, job.ThreadID)
	delete(d.activeCtx, job.ThreadID)
	thread = d.state.Threads[job.ThreadID]
	if thread.Awaiting == nil || thread.Awaiting.EventKey != job.EventKey || thread.Awaiting.Attempt != job.Attempt {
		d.logger.Printf("retry action ignored thread=%s reason=stale_result", shortThreadID(job.ThreadID))
		_ = d.writeStatusLocked(true, len(discoverSessionRoots(d.config)))
		return
	}

	if result.ParentNotified {
		thread.Awaiting.ParentNotified = true
	}
	if err != nil || result.Outcome == outcomeRetryLater {
		reason := controllerFailureReason(result, err)
		d.controllerState = reason
		d.rescheduleAwaitingWithPolicyLocked(job.ThreadID, thread, finishedAt, reason, true)
	} else if result.Outcome == outcomeUserActive {
		// User or turn activity is temporary. A matching task_started event will
		// still cancel or acknowledge this retry on the next scan; until then the
		// failed task remains independently queued.
		d.controllerState = "ready"
		thread.Awaiting.DispatchFailures = 0
		d.rescheduleAwaitingWithPolicyLocked(job.ThreadID, thread, finishedAt, controllerFailureReason(result, nil), false)
	} else if result.Outcome == outcomeNotApplicable {
		d.controllerState = "ready"
		thread.RecoveryAttempts = 0
		thread.ConsecutiveRetries = 0
		thread.CurrentTurnProgress = false
		thread.Pending = nil
		thread.Awaiting = nil
		thread.Stopped = nil
		thread.GoalStop = nil
		if status, held := goalHoldStatusForReason(result.Reason); held {
			thread.GoalHeld = true
			if status != "" {
				thread.GoalStatus = status
			}
		}
		d.state.Threads[job.ThreadID] = thread
		d.logger.Printf("retry skipped thread=%s reason=%s", shortThreadID(job.ThreadID), result.Reason)
	} else {
		d.controllerState = "ready"
		thread.Awaiting.Action = result.Action
		thread.Awaiting.DispatchFailures = 0
		thread.Awaiting.StartDeadline = finishedAt.Add(time.Duration(d.config.StartAckTimeoutSeconds) * time.Second)
		d.state.Threads[job.ThreadID] = thread
		d.logger.Printf("retry action accepted thread=%s action=%s attempt=%d", shortThreadID(job.ThreadID), result.Action, job.Attempt)
	}
	if stateErr := d.persistStateLocked(); stateErr != nil {
		d.logger.Printf("state save deferred category=state_write")
	}
	_ = d.writeStatusLocked(true, len(discoverSessionRoots(d.config)))
}

func (d *daemon) waitForJobs() {
	d.wg.Wait()
}

func (d *daemon) writeStatus(running bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.writeStatusLocked(running, len(discoverSessionRoots(d.config)))
}

func (d *daemon) writeStatusLocked(running bool, rootCount int) error {
	pending := 0
	active := 0
	for _, thread := range d.state.Threads {
		if thread.Pending != nil {
			pending++
		}
		if thread.Awaiting != nil {
			active++
		}
	}
	if !running {
		pending = 0
		active = 0
	}
	status := StatusSnapshot{
		Version:                appVersion,
		Running:                running,
		PID:                    os.Getpid(),
		StartedAt:              d.startedAt,
		LastScanAt:             d.lastScan,
		WatchedRoots:           rootCount,
		PendingRetries:         pending,
		ActiveRetries:          active,
		Paused:                 d.paused,
		SharedAppServerEnabled: d.config.SharedAppServerEnabled,
		ControllerState:        d.controllerState,
		LastError:              d.lastError,
		LogPath:                d.logger.path,
	}
	writer := d.writeStatusFile
	if writer == nil {
		writer = writeJSONAtomic
	}
	if err := writer(d.statusPath, status); err != nil {
		d.statusWriteDeferred = true
		return err
	}
	if d.statusWriteDeferred {
		d.statusWriteDeferred = false
		d.logger.Printf("status save recovered category=status_write")
	}
	return nil
}
