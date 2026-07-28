package main

import "time"

const appVersion = "0.4.2"

type FailureClass string

const (
	classNone          FailureClass = "none"
	classTransient     FailureClass = "transient"
	classRateLimit     FailureClass = "rate_limit"
	classServer        FailureClass = "server"
	classAuthTransient FailureClass = "auth_transient"
	classAuthLimited   FailureClass = "auth_limited"
	classUnknown       FailureClass = "unknown"
)

type RetryDecision struct {
	Retry       bool
	Class       FailureClass
	MaxAttempts int
	Reason      string
}

type RelevantEvent struct {
	Kind          string
	ThreadID      string
	TurnID        string
	Timestamp     time.Time
	ErrorText     string
	GoalStatus    string
	GoalUpdatedAt time.Time
}

type PendingRetry struct {
	EventKey         string       `json:"event_key"`
	FailedTurnID     string       `json:"turn_id"`
	FailedAt         time.Time    `json:"failed_at,omitempty"`
	Class            FailureClass `json:"class"`
	DueAt            time.Time    `json:"due_at"`
	CodexHome        string       `json:"codex_home"`
	RolloutPath      string       `json:"rollout_path,omitempty"`
	Attempt          int          `json:"attempt"`
	MaxAttempts      int          `json:"max_attempts,omitempty"`
	DispatchFailures int          `json:"dispatch_failures,omitempty"`
}

type RetryAction string

const (
	actionDispatching          RetryAction = "dispatching"
	actionGoalResume           RetryAction = "goal_resume"
	actionGoalActive           RetryAction = "goal_active"
	actionConversationContinue RetryAction = "conversation_continue"
)

type AwaitingRetry struct {
	EventKey          string       `json:"event_key"`
	FailedTurnID      string       `json:"failed_turn_id"`
	FailedAt          time.Time    `json:"failed_at,omitempty"`
	RetryTurnID       string       `json:"retry_turn_id,omitempty"`
	Class             FailureClass `json:"class"`
	Action            RetryAction  `json:"action"`
	Attempt           int          `json:"attempt"`
	MaxAttempts       int          `json:"max_attempts,omitempty"`
	DispatchFailures  int          `json:"dispatch_failures,omitempty"`
	DispatchStartedAt time.Time    `json:"dispatch_started_at"`
	StartDeadline     time.Time    `json:"start_deadline"`
	StartedAt         time.Time    `json:"started_at,omitempty"`
	CodexHome         string       `json:"codex_home"`
	RolloutPath       string       `json:"rollout_path,omitempty"`
}

type StoppedRetry struct {
	EventKey     string       `json:"event_key"`
	FailedTurnID string       `json:"failed_turn_id"`
	FailedAt     time.Time    `json:"failed_at,omitempty"`
	Class        FailureClass `json:"class"`
	StoppedAt    time.Time    `json:"stopped_at"`
	CodexHome    string       `json:"codex_home"`
	RolloutPath  string       `json:"rollout_path,omitempty"`
	Attempts     int          `json:"attempts"`
	MaxAttempts  int          `json:"max_attempts"`
	Reason       string       `json:"reason"`
}

type ThreadState struct {
	ConsecutiveFailures int            `json:"consecutive_failures"`
	LastFailureAt       time.Time      `json:"last_failure_at,omitempty"`
	LastAutoRetryAt     time.Time      `json:"last_auto_retry_at,omitempty"`
	GoalStatus          string         `json:"goal_status,omitempty"`
	GoalUpdatedAt       time.Time      `json:"goal_updated_at,omitempty"`
	GoalObservedAt      time.Time      `json:"goal_observed_at,omitempty"`
	GoalHeld            bool           `json:"goal_held,omitempty"`
	Pending             *PendingRetry  `json:"pending,omitempty"`
	Awaiting            *AwaitingRetry `json:"awaiting,omitempty"`
	Stopped             *StoppedRetry  `json:"stopped,omitempty"`
}

type FileCursor struct {
	Offset   int64     `json:"offset"`
	LastSeen time.Time `json:"last_seen"`
}

type RuntimeState struct {
	Version         int                    `json:"version"`
	Initialized     bool                   `json:"initialized"`
	Files           map[string]FileCursor  `json:"files"`
	Threads         map[string]ThreadState `json:"threads"`
	ProcessedEvents map[string]time.Time   `json:"processed_events"`
}

type RetryJob struct {
	ThreadID         string
	FailedTurnID     string
	FailedAt         time.Time
	EventKey         string
	Class            FailureClass
	CodexHome        string
	RolloutPath      string
	Attempt          int
	MaxAttempts      int
	DispatchFailures int
}

type DispatchOutcome string

const (
	outcomeDispatched    DispatchOutcome = "dispatched"
	outcomeAwaitingStart DispatchOutcome = "awaiting_start"
	outcomeUserActive    DispatchOutcome = "user_active"
	outcomeRetryLater    DispatchOutcome = "retry_later"
	outcomeNotApplicable DispatchOutcome = "not_applicable"
)

type DispatchResult struct {
	Outcome DispatchOutcome `json:"outcome"`
	Action  RetryAction     `json:"action"`
	Reason  string          `json:"reason,omitempty"`
}

type StatusSnapshot struct {
	Version        string    `json:"version"`
	Running        bool      `json:"running"`
	PID            int       `json:"pid"`
	StartedAt      time.Time `json:"started_at"`
	LastScanAt     time.Time `json:"last_scan_at"`
	WatchedRoots   int       `json:"watched_roots"`
	PendingRetries int       `json:"pending_retries"`
	ActiveRetries  int       `json:"active_retries"`
	Paused         bool      `json:"paused"`
	LastError      string    `json:"last_error,omitempty"`
	LogPath        string    `json:"log_path"`
}
