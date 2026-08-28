package main

import "time"

const appVersion = "0.7.7"

type FailureClass string

const (
	classNone          FailureClass = "none"
	classTransient     FailureClass = "transient"
	classRateLimit     FailureClass = "rate_limit"
	classServer        FailureClass = "server"
	classAuthTransient FailureClass = "auth_transient"
	classAuthLimited   FailureClass = "auth_limited"
	classEmptyResponse FailureClass = "empty_response"
	classUnknown       FailureClass = "unknown"
)

type RetryDecision struct {
	Retry          bool
	Class          FailureClass
	MaxAttempts    int
	MaxConsecutive int
	Reason         string
}

type RelevantEvent struct {
	Kind            string
	ThreadID        string
	TurnID          string
	Timestamp       time.Time
	ErrorText       string
	FinalKnown      bool
	FinalPresent    bool
	AbortReason     string
	GoalStatus      string
	GoalUpdatedAt   time.Time
	ParentThreadID  string
	RecoveryEventID string
}

type PendingRetry struct {
	EventKey            string       `json:"event_key"`
	FailedTurnID        string       `json:"turn_id"`
	FailedAt            time.Time    `json:"failed_at,omitempty"`
	OriginTurnStartedAt time.Time    `json:"origin_turn_started_at,omitempty"`
	Class               FailureClass `json:"class"`
	DueAt               time.Time    `json:"due_at"`
	CodexHome           string       `json:"codex_home"`
	RolloutPath         string       `json:"rollout_path,omitempty"`
	Attempt             int          `json:"attempt"` // Per-fault recovery attempt; retained for state compatibility.
	MaxAttempts         int          `json:"max_attempts,omitempty"`
	ConsecutiveRetry    int          `json:"consecutive_retry"`
	MaxConsecutive      int          `json:"max_consecutive_retries,omitempty"`
	DispatchFailures    int          `json:"dispatch_failures,omitempty"`
	ParentNotified      bool         `json:"parent_notified,omitempty"`
	GoalLimitRestart    bool         `json:"goal_limit_restart,omitempty"`
}

type RetryAction string

const (
	actionDispatching          RetryAction = "dispatching"
	actionGoalResume           RetryAction = "goal_resume"
	actionGoalActive           RetryAction = "goal_active"
	actionConversationContinue RetryAction = "conversation_continue"
	actionSubagentContinue     RetryAction = "subagent_continue"
	actionGoalBlock            RetryAction = "goal_block"
)

type AwaitingRetry struct {
	EventKey             string       `json:"event_key"`
	FailedTurnID         string       `json:"failed_turn_id"`
	FailedAt             time.Time    `json:"failed_at,omitempty"`
	OriginTurnStartedAt  time.Time    `json:"origin_turn_started_at,omitempty"`
	RetryTurnID          string       `json:"retry_turn_id,omitempty"`
	Class                FailureClass `json:"class"`
	Action               RetryAction  `json:"action"`
	Attempt              int          `json:"attempt"`
	MaxAttempts          int          `json:"max_attempts,omitempty"`
	ConsecutiveRetry     int          `json:"consecutive_retry"`
	MaxConsecutive       int          `json:"max_consecutive_retries,omitempty"`
	DispatchFailures     int          `json:"dispatch_failures,omitempty"`
	ParentNotified       bool         `json:"parent_notified,omitempty"`
	GoalLimitRestart     bool         `json:"goal_limit_restart,omitempty"`
	DispatchStartedAt    time.Time    `json:"dispatch_started_at"`
	StartDeadline        time.Time    `json:"start_deadline"`
	StartedAt            time.Time    `json:"started_at,omitempty"`
	LifecycleChecks      int          `json:"lifecycle_checks,omitempty"`
	LastLifecycleCheckAt time.Time    `json:"last_lifecycle_check_at,omitempty"`
	CodexHome            string       `json:"codex_home"`
	RolloutPath          string       `json:"rollout_path,omitempty"`
}

type GoalStopRequest struct {
	EventKey         string    `json:"event_key"`
	Reason           string    `json:"reason"`
	RequestedAt      time.Time `json:"requested_at"`
	DueAt            time.Time `json:"due_at"`
	DispatchFailures int       `json:"dispatch_failures,omitempty"`
}

type StoppedRetry struct {
	EventKey            string       `json:"event_key"`
	FailedTurnID        string       `json:"failed_turn_id"`
	FailedAt            time.Time    `json:"failed_at,omitempty"`
	OriginTurnStartedAt time.Time    `json:"origin_turn_started_at,omitempty"`
	Class               FailureClass `json:"class"`
	StoppedAt           time.Time    `json:"stopped_at"`
	CodexHome           string       `json:"codex_home"`
	RolloutPath         string       `json:"rollout_path,omitempty"`
	Attempts            int          `json:"attempts"` // Completed recovery attempts; retained for state compatibility.
	MaxAttempts         int          `json:"max_attempts"`
	ConsecutiveRetries  int          `json:"consecutive_retries"`
	MaxConsecutive      int          `json:"max_consecutive_retries"`
	Reason              string       `json:"reason"`
	Historical          bool         `json:"historical,omitempty"`
}

type ThreadState struct {
	RecoveryAttempts    int              `json:"recovery_attempts,omitempty"`
	ConsecutiveRetries  int              `json:"consecutive_retries,omitempty"`
	LegacyFailures      int              `json:"consecutive_failures,omitempty"`
	LastFailureAt       time.Time        `json:"last_failure_at,omitempty"`
	LastAutoRetryAt     time.Time        `json:"last_auto_retry_at,omitempty"`
	GoalStatus          string           `json:"goal_status,omitempty"`
	GoalUpdatedAt       time.Time        `json:"goal_updated_at,omitempty"`
	GoalObservedAt      time.Time        `json:"goal_observed_at,omitempty"`
	GoalHeld            bool             `json:"goal_held,omitempty"`
	LastStartedTurnID   string           `json:"last_started_turn_id,omitempty"`
	LastStartedAt       time.Time        `json:"last_started_at,omitempty"`
	LastExternalTurnID  string           `json:"last_external_turn_id,omitempty"`
	LastExternalTurnAt  time.Time        `json:"last_external_turn_at,omitempty"`
	CurrentTurnProgress bool             `json:"current_turn_progress,omitempty"`
	LastAbortedTurnID   string           `json:"last_aborted_turn_id,omitempty"`
	LastAbortedAt       time.Time        `json:"last_aborted_at,omitempty"`
	Pending             *PendingRetry    `json:"pending,omitempty"`
	Awaiting            *AwaitingRetry   `json:"awaiting,omitempty"`
	Stopped             *StoppedRetry    `json:"stopped,omitempty"`
	GoalStop            *GoalStopRequest `json:"goal_stop,omitempty"`
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
	Kind                RetryJobKind
	ThreadID            string
	FailedTurnID        string
	FailedAt            time.Time
	OriginTurnStartedAt time.Time
	EventKey            string
	Class               FailureClass
	CodexHome           string
	RolloutPath         string
	Attempt             int
	MaxAttempts         int
	ConsecutiveRetry    int
	MaxConsecutive      int
	DispatchFailures    int
	ParentNotified      bool
	GoalLimitRestart    bool
	RecoveryEventID     string
}

type RetryJobKind string

const (
	jobRecovery  RetryJobKind = "recovery"
	jobGoalBlock RetryJobKind = "goal_block"
)

type DispatchOutcome string

const (
	outcomeDispatched    DispatchOutcome = "dispatched"
	outcomeAwaitingStart DispatchOutcome = "awaiting_start"
	outcomeUserActive    DispatchOutcome = "user_active"
	outcomeRetryLater    DispatchOutcome = "retry_later"
	outcomeNotApplicable DispatchOutcome = "not_applicable"
)

type DispatchResult struct {
	Outcome        DispatchOutcome `json:"outcome"`
	Action         RetryAction     `json:"action"`
	Reason         string          `json:"reason,omitempty"`
	ParentNotified bool            `json:"parent_notified,omitempty"`
}

type StatusSnapshot struct {
	Version                string    `json:"version"`
	Running                bool      `json:"running"`
	PID                    int       `json:"pid"`
	StartedAt              time.Time `json:"started_at"`
	LastScanAt             time.Time `json:"last_scan_at"`
	WatchedRoots           int       `json:"watched_roots"`
	PendingRetries         int       `json:"pending_retries"`
	ActiveRetries          int       `json:"active_retries"`
	Paused                 bool      `json:"paused"`
	SharedAppServerEnabled bool      `json:"shared_app_server_enabled"`
	ControllerState        string    `json:"controller_state,omitempty"`
	LastError              string    `json:"last_error,omitempty"`
	LogPath                string    `json:"log_path"`
}
