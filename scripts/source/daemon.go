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
	return &daemon{
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
	}, nil
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
		return fmt.Errorf("save state: %w", stateErr)
	}
	if statusErr != nil {
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

func (d *daemon) applyControlCommandLocked(command ControlCommand, now time.Time) {
	thread, found := d.state.Threads[command.ThreadID]
	if !found || thread.Pending == nil {
		d.logger.Printf("control command ignored thread=%s reason=retry_not_pending", shortThreadID(command.ThreadID))
		return
	}
	switch command.Action {
	case commandRetryNow:
		thread.Pending.DueAt = now
		d.state.Threads[command.ThreadID] = thread
		d.logger.Printf("retry expedited thread=%s", shortThreadID(command.ThreadID))
	case commandCancelRetry:
		thread.Pending = nil
		thread.ConsecutiveFailures = 0
		d.state.Threads[command.ThreadID] = thread
		d.logger.Printf("retry cancelled thread=%s reason=user_control", shortThreadID(command.ThreadID))
	}
}

func (d *daemon) handleEventLocked(item scannedEvent, now time.Time) {
	event := item.Event
	key := eventKey(item.ThreadID, event)
	if _, exists := d.state.ProcessedEvents[key]; exists {
		return
	}
	d.state.ProcessedEvents[key] = now
	thread := d.state.Threads[item.ThreadID]

	switch event.Kind {
	case "task_started":
		d.handleTaskStartedLocked(item.ThreadID, event, thread)
	case "task_complete":
		d.handleTaskCompleteLocked(item, key, now, thread)
	case "thread_goal_updated":
		d.handleGoalUpdatedLocked(item.ThreadID, event, thread)
	}
}

func (d *daemon) handleGoalUpdatedLocked(threadID string, event RelevantEvent, thread ThreadState) {
	staleUpdate := !thread.GoalUpdatedAt.IsZero() && event.GoalUpdatedAt.Before(thread.GoalUpdatedAt)
	staleObservation := event.GoalUpdatedAt.Equal(thread.GoalUpdatedAt) &&
		!thread.GoalObservedAt.IsZero() && !event.Timestamp.After(thread.GoalObservedAt)
	if staleUpdate || staleObservation {
		d.logger.Printf("goal update ignored thread=%s reason=stale_goal_state", shortThreadID(threadID))
		return
	}
	thread.GoalStatus = event.GoalStatus
	thread.GoalUpdatedAt = event.GoalUpdatedAt
	thread.GoalObservedAt = event.Timestamp
	thread.GoalHeld = goalRequiresHold(event.GoalStatus, event.GoalUpdatedAt, thread.LastFailureAt)
	if !thread.GoalHeld {
		d.state.Threads[threadID] = thread
		return
	}
	hadRetry := thread.Pending != nil || thread.Awaiting != nil
	if _, active := d.active[threadID]; active {
		hadRetry = true
	}
	if cancel := d.activeCtx[threadID]; cancel != nil {
		cancel()
	}
	thread.Pending = nil
	thread.Awaiting = nil
	thread.ConsecutiveFailures = 0
	d.state.Threads[threadID] = thread
	if hadRetry {
		d.logger.Printf("retry cancelled thread=%s reason=goal_held", shortThreadID(threadID))
	}
}

func (d *daemon) handleTaskStartedLocked(threadID string, event RelevantEvent, thread ThreadState) {
	if thread.Awaiting != nil {
		awaiting := thread.Awaiting
		withinDispatchWindow := !event.Timestamp.Before(awaiting.DispatchStartedAt.Add(-2*time.Second)) &&
			(awaiting.StartDeadline.IsZero() || !event.Timestamp.After(awaiting.StartDeadline.Add(2*time.Second)))
		if awaiting.RetryTurnID == "" && event.TurnID != "" && withinDispatchWindow {
			awaiting.RetryTurnID = event.TurnID
			awaiting.StartedAt = event.Timestamp
			thread.Awaiting = awaiting
			d.state.Threads[threadID] = thread
			d.logger.Printf("retry acknowledged thread=%s attempt=%d", shortThreadID(threadID), awaiting.Attempt)
			return
		}
		if awaiting.RetryTurnID == event.TurnID {
			return
		}
		d.cancelRetryLocked(threadID, thread, "manual_task_started")
		return
	}
	if thread.Pending != nil {
		d.cancelRetryLocked(threadID, thread, "manual_task_started")
		return
	}
	thread.ConsecutiveFailures = 0
	d.state.Threads[threadID] = thread
}

func (d *daemon) handleTaskCompleteLocked(item scannedEvent, key string, now time.Time, thread ThreadState) {
	event := item.Event
	if thread.Awaiting != nil {
		awaiting := thread.Awaiting
		if awaiting.RetryTurnID == "" || awaiting.RetryTurnID != event.TurnID {
			d.logger.Printf("completion ignored thread=%s reason=turn_mismatch", shortThreadID(item.ThreadID))
			return
		}
		if event.ErrorText == "" {
			d.logger.Printf("retry chain recovered thread=%s attempt=%d", shortThreadID(item.ThreadID), awaiting.Attempt)
			d.resetRetryStateLocked(item.ThreadID, thread)
			return
		}
		thread.Awaiting = nil
		d.scheduleFailureLocked(item, key, now, thread, awaiting.Attempt+1)
		return
	}

	if thread.Pending != nil {
		if thread.Pending.FailedTurnID != event.TurnID {
			d.logger.Printf("completion ignored thread=%s reason=pending_turn_mismatch", shortThreadID(item.ThreadID))
			return
		}
		if event.ErrorText == "" {
			d.resetRetryStateLocked(item.ThreadID, thread)
		}
		return
	}

	if event.ErrorText == "" {
		thread.ConsecutiveFailures = 0
		d.state.Threads[item.ThreadID] = thread
		return
	}
	d.scheduleFailureLocked(item, key, now, thread, 1)
}

func (d *daemon) scheduleFailureLocked(item scannedEvent, key string, now time.Time, thread ThreadState, attempt int) {
	if thread.GoalHeld {
		if thread.GoalStatus == "blocked" && goalBlockedByFailure(thread.GoalUpdatedAt, item.Event.Timestamp) {
			thread.GoalHeld = false
		} else {
			thread.Pending = nil
			thread.Awaiting = nil
			thread.ConsecutiveFailures = 0
			thread.LastFailureAt = item.Event.Timestamp
			d.state.Threads[item.ThreadID] = thread
			d.logger.Printf("retry skipped thread=%s category=non_retryable reason=goal_held", shortThreadID(item.ThreadID))
			return
		}
	}
	decision := classifyFailure(item.Event.ErrorText, d.config)
	if !decision.Retry {
		d.resetRetryStateLocked(item.ThreadID, thread)
		d.logger.Printf("retry skipped thread=%s category=non_retryable reason=%s", shortThreadID(item.ThreadID), decision.Reason)
		return
	}
	if decision.MaxAttempts > 0 && attempt > decision.MaxAttempts {
		thread.Pending = nil
		thread.Awaiting = nil
		thread.ConsecutiveFailures = attempt
		thread.LastFailureAt = item.Event.Timestamp
		d.state.Threads[item.ThreadID] = thread
		d.logger.Printf("retry exhausted thread=%s category=%s attempts=%d", shortThreadID(item.ThreadID), decision.Class, attempt-1)
		return
	}

	delay := retryDelay(attempt, d.config)
	thread.ConsecutiveFailures = attempt
	thread.LastFailureAt = item.Event.Timestamp
	thread.Awaiting = nil
	thread.Pending = &PendingRetry{
		EventKey:     key,
		FailedTurnID: item.Event.TurnID,
		FailedAt:     item.Event.Timestamp,
		Class:        decision.Class,
		DueAt:        now.Add(delay),
		CodexHome:    item.Root.CodexHome,
		RolloutPath:  item.RolloutPath,
		Attempt:      attempt,
		MaxAttempts:  decision.MaxAttempts,
	}
	d.state.Threads[item.ThreadID] = thread
	d.logger.Printf("retry scheduled thread=%s category=%s attempt=%d delay_seconds=%d", shortThreadID(item.ThreadID), decision.Class, attempt, int(delay.Seconds()))
}

func goalRequiresHold(status string, goalUpdatedAt, failureAt time.Time) bool {
	if status == "active" {
		return false
	}
	if status == "blocked" {
		return !goalBlockedByFailure(goalUpdatedAt, failureAt)
	}
	return true
}

func goalBlockedByFailure(goalUpdatedAt, failureAt time.Time) bool {
	if goalUpdatedAt.IsZero() || failureAt.IsZero() {
		return false
	}
	return !goalUpdatedAt.Before(failureAt.Add(-2*time.Second)) &&
		!goalUpdatedAt.After(failureAt.Add(5*time.Second))
}

func (d *daemon) cancelRetryLocked(threadID string, thread ThreadState, reason string) {
	if cancel := d.activeCtx[threadID]; cancel != nil {
		cancel()
	}
	thread.Pending = nil
	thread.Awaiting = nil
	thread.ConsecutiveFailures = 0
	d.state.Threads[threadID] = thread
	d.logger.Printf("retry cancelled thread=%s reason=%s", shortThreadID(threadID), reason)
}

func (d *daemon) resetRetryStateLocked(threadID string, thread ThreadState) {
	thread.Pending = nil
	thread.Awaiting = nil
	thread.ConsecutiveFailures = 0
	d.state.Threads[threadID] = thread
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
		EventKey:         awaiting.EventKey,
		FailedTurnID:     awaiting.FailedTurnID,
		FailedAt:         awaiting.FailedAt,
		Class:            awaiting.Class,
		DueAt:            now.Add(delay),
		CodexHome:        awaiting.CodexHome,
		RolloutPath:      awaiting.RolloutPath,
		Attempt:          awaiting.Attempt,
		MaxAttempts:      awaiting.MaxAttempts,
		DispatchFailures: dispatchFailures,
	}
	d.state.Threads[threadID] = thread
	d.logger.Printf("retry rescheduled thread=%s reason=%s delay_seconds=%d", shortThreadID(threadID), reason, int(delay.Seconds()))
}

func (d *daemon) dispatchDueLocked(now time.Time) []RetryJob {
	if d.paused {
		return nil
	}
	available := d.config.MaxParallelRetries - len(d.active)
	if available <= 0 {
		return nil
	}
	type candidate struct {
		threadID string
		pending  PendingRetry
	}
	candidates := make([]candidate, 0)
	for threadID, thread := range d.state.Threads {
		if thread.GoalHeld {
			if thread.Pending != nil || thread.Awaiting != nil {
				thread.Pending = nil
				thread.Awaiting = nil
				thread.ConsecutiveFailures = 0
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
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].pending.DueAt.Before(candidates[j].pending.DueAt)
	})
	if available > len(candidates) {
		available = len(candidates)
	}
	jobs := make([]RetryJob, 0, available)
	for _, item := range candidates[:available] {
		thread := d.state.Threads[item.threadID]
		thread.Pending = nil
		thread.LastAutoRetryAt = now
		thread.Awaiting = &AwaitingRetry{
			EventKey:          item.pending.EventKey,
			FailedTurnID:      item.pending.FailedTurnID,
			FailedAt:          item.pending.FailedAt,
			Class:             item.pending.Class,
			Action:            actionDispatching,
			Attempt:           item.pending.Attempt,
			MaxAttempts:       item.pending.MaxAttempts,
			DispatchFailures:  item.pending.DispatchFailures,
			DispatchStartedAt: now,
			StartDeadline:     now.Add(time.Duration(d.config.StartAckTimeoutSeconds) * time.Second),
			CodexHome:         item.pending.CodexHome,
			RolloutPath:       item.pending.RolloutPath,
		}
		d.state.Threads[item.threadID] = thread
		job := RetryJob{
			ThreadID:         item.threadID,
			FailedTurnID:     item.pending.FailedTurnID,
			FailedAt:         item.pending.FailedAt,
			EventKey:         item.pending.EventKey,
			Class:            item.pending.Class,
			CodexHome:        item.pending.CodexHome,
			RolloutPath:      item.pending.RolloutPath,
			Attempt:          item.pending.Attempt,
			MaxAttempts:      item.pending.MaxAttempts,
			DispatchFailures: item.pending.DispatchFailures,
		}
		d.active[item.threadID] = job
		jobs = append(jobs, job)
	}
	return jobs
}

func (d *daemon) runJob(ctx context.Context, job RetryJob) {
	defer d.wg.Done()
	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	d.mu.Lock()
	thread := d.state.Threads[job.ThreadID]
	if thread.Awaiting == nil || thread.Awaiting.EventKey != job.EventKey ||
		thread.Awaiting.Attempt != job.Attempt || thread.GoalHeld {
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

	if err != nil || result.Outcome == outcomeRetryLater {
		d.rescheduleAwaitingLocked(job.ThreadID, thread, finishedAt, controllerFailureReason(result, err))
	} else if result.Outcome == outcomeUserActive {
		// User or turn activity is temporary. A matching task_started event will
		// still cancel or acknowledge this retry on the next scan; until then the
		// failed task remains independently queued.
		d.rescheduleAwaitingLocked(job.ThreadID, thread, finishedAt, controllerFailureReason(result, nil))
	} else if result.Outcome == outcomeNotApplicable {
		thread.ConsecutiveFailures = 0
		thread.Pending = nil
		thread.Awaiting = nil
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

func goalHoldStatusForReason(reason string) (string, bool) {
	switch reason {
	case "goal_paused":
		return "paused", true
	case "goal_completed":
		return "completed", true
	case "goal_usage_limited":
		return "usageLimited", true
	case "goal_budget_limited":
		return "budgetLimited", true
	case "goal_blocked_before_failure":
		return "blocked", true
	case "goal_status_changed", "goal_status_unsupported":
		return "", true
	default:
		return "", false
	}
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
