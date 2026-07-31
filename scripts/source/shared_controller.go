package main

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

type sharedServer interface {
	Endpoint() string
	Ensure(context.Context) error
	SupportsHome(string) bool
}

type sharedAppServerController struct {
	server            sharedServer
	checker           desktopTransportChecker
	settingsForThread func(string, string) (ResumeSettings, error)
}

var errCodexRestartRequired = errors.New("Codex must restart to use the shared app-server")

type appThreadReadResult struct {
	Thread *struct {
		ID             string          `json:"id"`
		ParentThreadID string          `json:"parentThreadId"`
		Status         appThreadStatus `json:"status"`
		Source         map[string]any  `json:"source"`
	} `json:"thread"`
}

type appThreadStatus struct {
	Type string `json:"type"`
}

type appGoal struct {
	Status    string          `json:"status"`
	UpdatedAt json.RawMessage `json:"updatedAt"`
}

type appGoalResult struct {
	Goal *appGoal `json:"goal"`
}

type appLoadedThreadsResult struct {
	Data []string `json:"data"`
}

func newSharedAppServerController(config Config, dataDir string, logger *safeLogger) *sharedAppServerController {
	return &sharedAppServerController{
		server: newSharedServerManager(config, dataDir, logger),
		checker: powerShellDesktopTransportChecker{
			configuredExecutable: config.PowerShellExecutable,
		},
		settingsForThread: findThreadResumeSettings,
	}
}

func (c *sharedAppServerController) Prepare(ctx context.Context) error {
	if err := c.server.Ensure(ctx); err != nil {
		return err
	}
	state, err := c.checker.State(ctx)
	if err != nil {
		return err
	}
	if state == desktopLegacyStdio {
		return errCodexRestartRequired
	}
	return nil
}

func (c *sharedAppServerController) Readiness(ctx context.Context) (string, error) {
	state, err := c.checker.State(ctx)
	if err != nil {
		return "", err
	}
	switch state {
	case desktopStopped:
		return "codex_not_running", nil
	case desktopLegacyStdio:
		return "codex_restart_required", nil
	case desktopSharedServer:
		if err := c.server.Ensure(ctx); err != nil {
			return "", err
		}
		return "ready", nil
	default:
		return "", errSharedServerUnavailable
	}
}

func (c *sharedAppServerController) Dispatch(
	ctx context.Context,
	threadID string,
	prompt string,
	settings ResumeSettings,
	failedAt time.Time,
	originTurnStartedAt time.Time,
	recoveryEventID string,
	parentNotified bool,
	goalLimitRestart bool,
	failureClass FailureClass,
	codexHome string,
) (DispatchResult, error) {
	if !c.server.SupportsHome(codexHome) {
		return retryLaterResult("codex_home_not_shared", parentNotified), nil
	}
	if result, ready, err := c.preflight(ctx, parentNotified); err != nil || !ready {
		return result, err
	}
	client, err := dialAppServerRPC(ctx, c.server.Endpoint())
	if err != nil {
		return DispatchResult{}, errSharedServerUnavailable
	}
	defer client.Close()

	loaded, err := appThreadLoaded(ctx, client, threadID)
	if err != nil {
		return retryLaterResult(classifyAppServerError(err), parentNotified), nil
	}
	resumedStatus := ""
	if !loaded {
		var resumed appThreadReadResult
		if err := client.Call(ctx, "thread/resume", resumeParameters(threadID, settings), &resumed); err != nil {
			return retryLaterResult(classifyAppServerError(err), parentNotified), nil
		}
		if resumed.Thread != nil && resumed.Thread.Status.Type != "" {
			resumedStatus = resumed.Thread.Status.Type
		}
	}
	initial, err := readAppThread(ctx, client, threadID)
	if err != nil {
		return retryLaterResult(classifyAppServerError(err), parentNotified), nil
	}
	if initial.Thread == nil || initial.Thread.Status.Type == "" {
		return retryLaterResult("thread_state_unavailable", parentNotified), nil
	}
	if initial.Thread.Status.Type == "active" || resumedStatus == "active" {
		return DispatchResult{Outcome: outcomeUserActive, Reason: "thread_active", ParentNotified: parentNotified}, nil
	}
	if resumedStatus == "" {
		resumedStatus = initial.Thread.Status.Type
	}
	parentThreadID := appThreadParentID(initial.Thread.ParentThreadID, initial.Thread.Source)
	isSubagent := parentThreadID != ""

	var initialGoal *appGoal
	var initialGoalRevision string
	continuingWhileGoalHeld := false
	if !isSubagent {
		goal, goalErr := readAppGoal(ctx, client, threadID)
		if goalErr != nil {
			return retryLaterResult(classifyAppServerError(goalErr), parentNotified), nil
		}
		initialGoal = goal
		holdReason := appGoalHoldReason(initialGoal, failedAt, goalLimitRestart)
		continuingWhileGoalHeld = appHeldConversationAllowed(initialGoal, failedAt, originTurnStartedAt, goalLimitRestart)
		if holdReason != "" && !continuingWhileGoalHeld {
			return notApplicableResult(holdReason, parentNotified), nil
		}
		initialGoalRevision = appGoalRevision(initialGoal)
	}

	if isSubagent {
		if failureClass != classEmptyResponse {
			return notApplicableResult("subagent_non_empty_failure", parentNotified), nil
		}
		if !parentNotified {
			if !recoveryEventIDPattern.MatchString(recoveryEventID) {
				return retryLaterResult("subagent_recovery_event_unavailable", parentNotified), nil
			}
			if err := c.injectSubagentNotice(ctx, client, codexHome, parentThreadID, threadID, recoveryEventID); err != nil {
				return retryLaterResult(classifyAppServerError(err), parentNotified), nil
			}
			parentNotified = true
		}
		latest, err := readAppThread(ctx, client, threadID)
		if err != nil {
			return retryLaterResult(classifyAppServerError(err), parentNotified), nil
		}
		if latest.Thread != nil && (latest.Thread.Status.Type == "active" || resumedStatus == "active") {
			return DispatchResult{Outcome: outcomeUserActive, Reason: "thread_active", ParentNotified: parentNotified}, nil
		}
		if err := startAppConversation(ctx, client, threadID, prompt); err != nil {
			return retryLaterResult(classifyAppServerError(err), parentNotified), nil
		}
		return DispatchResult{Outcome: outcomeDispatched, Action: actionSubagentContinue, Reason: "subagent_resumed_in_background", ParentNotified: parentNotified}, nil
	}

	latestGoal, err := readAppGoal(ctx, client, threadID)
	if err != nil {
		return retryLaterResult(classifyAppServerError(err), parentNotified), nil
	}
	if (initialGoal == nil) != (latestGoal == nil) {
		return notApplicableResult("goal_status_changed", parentNotified), nil
	}
	if continuingWhileGoalHeld {
		if !appHeldConversationAllowed(latestGoal, failedAt, originTurnStartedAt, goalLimitRestart) ||
			appGoalRevision(latestGoal) != initialGoalRevision {
			return notApplicableResult("goal_status_changed", parentNotified), nil
		}
		if resumedStatus == "active" {
			return DispatchResult{Outcome: outcomeUserActive, Reason: "thread_active", ParentNotified: parentNotified}, nil
		}
		if err := startAppConversation(ctx, client, threadID, prompt); err != nil {
			return retryLaterResult(classifyAppServerError(err), parentNotified), nil
		}
		return DispatchResult{Outcome: outcomeDispatched, Action: actionConversationContinue, Reason: "silent_turn_started_with_goal_held", ParentNotified: parentNotified}, nil
	}
	if holdReason := appGoalHoldReason(latestGoal, failedAt, goalLimitRestart); holdReason != "" {
		return notApplicableResult(holdReason, parentNotified), nil
	}
	if latestGoal != nil && (latestGoal.Status == "active" || latestGoal.Status == "blocked") {
		if latestGoal.Status == "blocked" {
			var updated appGoalResult
			if err := client.Call(ctx, "thread/goal/set", map[string]any{"threadId": threadID, "status": "active"}, &updated); err != nil {
				return retryLaterResult(classifyAppServerError(err), parentNotified), nil
			}
		}
		return DispatchResult{Outcome: outcomeDispatched, Action: actionGoalResume, Reason: "goal_resumed_in_background", ParentNotified: parentNotified}, nil
	}
	if resumedStatus == "active" {
		return DispatchResult{Outcome: outcomeUserActive, Reason: "thread_active", ParentNotified: parentNotified}, nil
	}
	if err := startAppConversation(ctx, client, threadID, prompt); err != nil {
		return retryLaterResult(classifyAppServerError(err), parentNotified), nil
	}
	return DispatchResult{Outcome: outcomeDispatched, Action: actionConversationContinue, Reason: "silent_turn_started_in_background", ParentNotified: parentNotified}, nil
}

func (c *sharedAppServerController) BlockGoal(ctx context.Context, threadID string, settings *ResumeSettings, codexHome string) (DispatchResult, error) {
	if !c.server.SupportsHome(codexHome) {
		return retryLaterResult("codex_home_not_shared", false), nil
	}
	if result, ready, err := c.preflight(ctx, false); err != nil || !ready {
		return result, err
	}
	client, err := dialAppServerRPC(ctx, c.server.Endpoint())
	if err != nil {
		return DispatchResult{}, errSharedServerUnavailable
	}
	defer client.Close()
	loaded, err := appThreadLoaded(ctx, client, threadID)
	if err != nil {
		return retryLaterResult(classifyAppServerError(err), false), nil
	}
	if !loaded {
		if settings == nil {
			return retryLaterResult("thread_settings_unavailable", false), nil
		}
		if err := client.Call(ctx, "thread/resume", resumeParameters(threadID, *settings), nil); err != nil {
			return retryLaterResult(classifyAppServerError(err), false), nil
		}
	}
	goal, err := readAppGoal(ctx, client, threadID)
	if err != nil {
		return retryLaterResult(classifyAppServerError(err), false), nil
	}
	if goal == nil {
		return retryLaterResult("goal_state_unavailable", false), nil
	}
	switch goal.Status {
	case "active":
		if err := client.Call(ctx, "thread/goal/set", map[string]any{"threadId": threadID, "status": "blocked"}, nil); err != nil {
			return retryLaterResult(classifyAppServerError(err), false), nil
		}
		return DispatchResult{Outcome: outcomeDispatched, Action: actionGoalBlock, Reason: "goal_blocked_after_empty_response_limit"}, nil
	case "blocked":
		return DispatchResult{Outcome: outcomeDispatched, Action: actionGoalBlock, Reason: "goal_already_blocked"}, nil
	case "paused":
		return DispatchResult{Outcome: outcomeDispatched, Action: actionGoalBlock, Reason: "goal_already_paused"}, nil
	case "completed", "complete":
		return DispatchResult{Outcome: outcomeDispatched, Action: actionGoalBlock, Reason: "goal_already_completed"}, nil
	case "usageLimited":
		return DispatchResult{Outcome: outcomeDispatched, Action: actionGoalBlock, Reason: "goal_already_usage_limited"}, nil
	case "budgetLimited":
		return DispatchResult{Outcome: outcomeDispatched, Action: actionGoalBlock, Reason: "goal_already_budget_limited"}, nil
	default:
		return retryLaterResult("goal_state_unavailable", false), nil
	}
}

func (c *sharedAppServerController) preflight(ctx context.Context, parentNotified bool) (DispatchResult, bool, error) {
	state, err := c.checker.State(ctx)
	if err != nil {
		return DispatchResult{}, false, err
	}
	switch state {
	case desktopStopped:
		return retryLaterResult("codex_not_running", parentNotified), false, nil
	case desktopLegacyStdio:
		return retryLaterResult("codex_restart_required", parentNotified), false, nil
	case desktopSharedServer:
		if err := c.server.Ensure(ctx); err != nil {
			return DispatchResult{}, false, err
		}
		return DispatchResult{}, true, nil
	default:
		return DispatchResult{}, false, errSharedServerUnavailable
	}
}

func readAppThread(ctx context.Context, client *appServerRPCClient, threadID string) (appThreadReadResult, error) {
	var result appThreadReadResult
	err := client.Call(ctx, "thread/read", map[string]any{"threadId": threadID, "includeTurns": false}, &result)
	return result, err
}

func readAppGoal(ctx context.Context, client *appServerRPCClient, threadID string) (*appGoal, error) {
	var result appGoalResult
	if err := client.Call(ctx, "thread/goal/get", map[string]any{"threadId": threadID}, &result); err != nil {
		return nil, err
	}
	return result.Goal, nil
}

func appThreadLoaded(ctx context.Context, client *appServerRPCClient, threadID string) (bool, error) {
	var result appLoadedThreadsResult
	if err := client.Call(ctx, "thread/loaded/list", map[string]any{}, &result); err != nil {
		return false, err
	}
	for _, value := range result.Data {
		if value == threadID {
			return true, nil
		}
	}
	return false, nil
}

func resumeParameters(threadID string, settings ResumeSettings) map[string]any {
	config := map[string]any{"model_reasoning_effort": settings.Effort}
	if settings.Summary != "" {
		config["model_reasoning_summary"] = settings.Summary
	}
	params := map[string]any{
		"threadId": threadID, "excludeTurns": true, "cwd": settings.CWD,
		"runtimeWorkspaceRoots": settings.RuntimeWorkspaceRoots,
		"model":                 settings.Model, "approvalPolicy": settings.ApprovalPolicy,
		"permissions": settings.Permissions, "config": config,
	}
	if settings.ApprovalsReviewer != "" {
		params["approvalsReviewer"] = settings.ApprovalsReviewer
	}
	if settings.ModelProvider != "" {
		params["modelProvider"] = settings.ModelProvider
	}
	if settings.ServiceTier != "" {
		params["serviceTier"] = settings.ServiceTier
	}
	if settings.Personality != "" {
		params["personality"] = settings.Personality
	}
	return params
}

func startAppConversation(ctx context.Context, client *appServerRPCClient, threadID, prompt string) error {
	err := client.Call(ctx, "turn/start", map[string]any{"threadId": threadID, "input": []any{}}, nil)
	if err == nil || !emptyAppInputUnsupported(err) {
		return err
	}
	return client.Call(ctx, "turn/start", map[string]any{
		"threadId": threadID,
		"input":    []any{map[string]any{"type": "text", "text": prompt, "text_elements": []any{}}},
	}, nil)
}

func (c *sharedAppServerController) injectSubagentNotice(
	ctx context.Context,
	client *appServerRPCClient,
	codexHome string,
	parentID string,
	childID string,
	eventID string,
) error {
	loaded, err := appThreadLoaded(ctx, client, parentID)
	if err != nil {
		return err
	}
	if !loaded {
		resolver := c.settingsForThread
		if resolver == nil {
			resolver = findThreadResumeSettings
		}
		settings, err := resolver(codexHome, parentID)
		if err != nil {
			return err
		}
		if err := client.Call(ctx, "thread/resume", resumeParameters(parentID, settings), nil); err != nil {
			return err
		}
	}
	payload, _ := json.Marshal(map[string]any{
		"parent_thread_id": parentID, "child_thread_id": childID, "recovery_event_id": eventID,
		"action": "resume_existing_child", "spawn_replacement": false,
		"instruction": "The watchdog is resuming the exact existing child. Do not resume or spawn any child for this recovery event.",
	})
	text := "codex-auto-retry:subagent-empty-response-recovery:v1:" + string(payload)
	return client.Call(ctx, "thread/inject_items", map[string]any{
		"threadId": parentID,
		"items": []any{map[string]any{
			"type": "message", "id": "msg_codex_auto_retry_" + strings.TrimPrefix(eventID, "car-"),
			"role": "developer", "content": []any{map[string]any{"type": "input_text", "text": text}},
		}},
	}, nil)
}

func appThreadParentID(direct string, source map[string]any) string {
	if direct != "" {
		return direct
	}
	subagent, _ := source["subAgent"].(map[string]any)
	spawn, _ := subagent["thread_spawn"].(map[string]any)
	value, _ := spawn["parent_thread_id"].(string)
	return value
}

func appGoalUpdatedAt(goal *appGoal) time.Time {
	if goal == nil || len(goal.UpdatedAt) == 0 {
		return time.Time{}
	}
	var number float64
	if json.Unmarshal(goal.UpdatedAt, &number) == nil && number > 0 {
		if number > 100000000000 {
			return time.UnixMilli(int64(number)).UTC()
		}
		return time.Unix(int64(number), 0).UTC()
	}
	var text string
	if json.Unmarshal(goal.UpdatedAt, &text) == nil {
		if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
			return parsed.UTC()
		}
		if value, err := strconv.ParseFloat(text, 64); err == nil && value > 0 {
			if value > 100000000000 {
				return time.UnixMilli(int64(value)).UTC()
			}
			return time.Unix(int64(value), 0).UTC()
		}
	}
	return time.Time{}
}

func appBlockedByFailure(goal *appGoal, failedAt time.Time, goalLimitRestart bool) bool {
	if goal == nil || goal.Status != "blocked" {
		return false
	}
	if goalLimitRestart {
		return true
	}
	updated := appGoalUpdatedAt(goal)
	return !failedAt.IsZero() && !updated.IsZero() &&
		!updated.Before(failedAt.Add(-2*time.Second)) && !updated.After(failedAt.Add(5*time.Second))
}

func appHeldConversationAllowed(goal *appGoal, failedAt, startedAt time.Time, goalLimitRestart bool) bool {
	if goal == nil || startedAt.IsZero() {
		return false
	}
	updated := appGoalUpdatedAt(goal)
	if updated.IsZero() || updated.After(startedAt) {
		return false
	}
	return goal.Status == "paused" || (goal.Status == "blocked" && !appBlockedByFailure(goal, failedAt, goalLimitRestart))
}

func appGoalRevision(goal *appGoal) string {
	if goal == nil {
		return ""
	}
	return goal.Status + "|" + string(goal.UpdatedAt)
}

func appGoalHoldReason(goal *appGoal, failedAt time.Time, goalLimitRestart bool) string {
	if goal == nil || goal.Status == "active" {
		return ""
	}
	switch goal.Status {
	case "blocked":
		if appBlockedByFailure(goal, failedAt, goalLimitRestart) {
			return ""
		}
		return "goal_blocked_before_failure"
	case "paused":
		return "goal_paused"
	case "completed", "complete":
		return "goal_completed"
	case "usageLimited":
		return "goal_usage_limited"
	case "budgetLimited":
		return "goal_budget_limited"
	default:
		return "goal_status_unsupported"
	}
}

func classifyAppServerError(err error) string {
	if errors.Is(err, errResumeSettingsUnavailable) {
		return "thread_settings_unavailable"
	}
	var requestError *appServerRequestError
	if errors.As(err, &requestError) {
		message := strings.ToLower(requestError.Message)
		switch {
		case strings.Contains(message, "active"), strings.Contains(message, "already running"), strings.Contains(message, "in progress"):
			return "thread_active"
		case strings.Contains(message, "not found"), strings.Contains(message, "unknown thread"), strings.Contains(message, "no rollout"):
			return "thread_not_found"
		case strings.Contains(message, "model provider"), strings.Contains(message, "configuration"):
			return "thread_config_unavailable"
		case strings.Contains(message, "not initialized"), strings.Contains(message, "not ready"):
			return "codex_app_not_ready"
		}
	}
	return "app_server_request_failed"
}

func emptyAppInputUnsupported(err error) bool {
	var requestError *appServerRequestError
	if !errors.As(err, &requestError) {
		return false
	}
	message := strings.ToLower(requestError.Message)
	return strings.Contains(message, "input must not be empty") ||
		strings.Contains(message, "input array must not be empty") ||
		strings.Contains(message, "at least one input") ||
		(strings.Contains(message, "input") && strings.Contains(message, "minitems"))
}

func retryLaterResult(reason string, parentNotified bool) DispatchResult {
	return DispatchResult{Outcome: outcomeRetryLater, Reason: reason, ParentNotified: parentNotified}
}

func notApplicableResult(reason string, parentNotified bool) DispatchResult {
	return DispatchResult{Outcome: outcomeNotApplicable, Reason: reason, ParentNotified: parentNotified}
}
