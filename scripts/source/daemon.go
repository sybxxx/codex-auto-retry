package main

import (
	"context"
	"errors"
	"fmt"
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

	mu        sync.Mutex
	wg        sync.WaitGroup
	state     RuntimeState
	active    map[string]RetryJob
	activeCtx map[string]context.CancelFunc
	lastScan  time.Time
	lastError string
	paused    bool
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
		config:      config,
		dataDir:     dataDir,
		statePath:   statePath,
		statusPath:  filepath.Join(dataDir, "status.json"),
		stopPath:    filepath.Join(dataDir, "stop.signal"),
		controlPath: controlPath,
		commandDir:  filepath.Join(dataDir, "commands"),
		logger:      logger,
		runner:      runner,
		startedAt:   time.Now().UTC(),
		state:       state,
		active:      make(map[string]RetryJob),
		activeCtx:   make(map[string]context.CancelFunc),
		paused:      control.Paused,
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

	d.mu.Lock()
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
	jobs := d.dispatchDueLocked(now)
	d.lastScan = now
	d.state.prune(now)
	stateErr := writeJSONAtomic(d.statePath, d.state)
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
		d.logger.Printf("state save failed category=state_write")
		return fmt.Errorf("save state: %w", stateErr)
	}
	if statusErr != nil {
		d.logger.Printf("status save failed category=status_write")
		return fmt.Errorf("save status: %w", statusErr)
	}
	return nil
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

func (d *daemon) rescheduleAwaitingLocked(threadID string, thread ThreadState, now time.Time, reason string) {
	awaiting := thread.Awaiting
	if awaiting == nil {
		return
	}
	dispatchFailures := awaiting.DispatchFailures + 1
	delay := retryDelay(dispatchFailures, d.config)
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
		d.rescheduleAwaitingLocked(job.ThreadID, thread, finishedAt, controllerFailureReason(result, err))
	} else if result.Outcome == outcomeUserActive {
		// User or turn activity is temporary. A matching task_started event will
		// still cancel or acknowledge this retry on the next scan; until then the
		// failed task remains independently queued.
		d.rescheduleAwaitingLocked(job.ThreadID, thread, finishedAt, controllerFailureReason(result, nil))
	} else if result.Outcome == outcomeNotApplicable {
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
		thread.Awaiting.Action = result.Action
		thread.Awaiting.StartDeadline = finishedAt.Add(time.Duration(d.config.StartAckTimeoutSeconds) * time.Second)
		d.state.Threads[job.ThreadID] = thread
		d.logger.Printf("retry action accepted thread=%s action=%s attempt=%d", shortThreadID(job.ThreadID), result.Action, job.Attempt)
	}
	if stateErr := writeJSONAtomic(d.statePath, d.state); stateErr != nil {
		d.lastError = stateErr.Error()
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
		Version:        appVersion,
		Running:        running,
		PID:            os.Getpid(),
		StartedAt:      d.startedAt,
		LastScanAt:     d.lastScan,
		WatchedRoots:   rootCount,
		PendingRetries: pending,
		ActiveRetries:  active,
		Paused:         d.paused,
		LastError:      d.lastError,
		LogPath:        d.logger.path,
	}
	return writeJSONAtomic(d.statusPath, status)
}
