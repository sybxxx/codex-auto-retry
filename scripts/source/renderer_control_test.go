package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type fakeCDPServer struct {
	server      *httptest.Server
	port        int
	result      any
	mu          sync.Mutex
	expressions []string
}

func newFakeCDPServer(t *testing.T, result any) *fakeCDPServer {
	t.Helper()
	fake := &fakeCDPServer{result: result}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	handler := http.NewServeMux()
	handler.HandleFunc("/json/list", func(response http.ResponseWriter, request *http.Request) {
		websocketURL := "ws://" + request.Host + "/devtools/page/codex"
		_ = json.NewEncoder(response).Encode([]cdpTarget{{
			Type:                 "page",
			Title:                "Codex",
			URL:                  "app://-/index.html",
			WebSocketDebuggerURL: websocketURL,
		}})
	})
	handler.HandleFunc("/devtools/page/codex", func(response http.ResponseWriter, request *http.Request) {
		connection, err := upgrader.Upgrade(response, request, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		for {
			var message cdpRequest
			if err := connection.ReadJSON(&message); err != nil {
				return
			}
			if message.Method == "Runtime.evaluate" {
				if params, ok := message.Params.(map[string]any); ok {
					if expression, ok := params["expression"].(string); ok {
						fake.mu.Lock()
						fake.expressions = append(fake.expressions, expression)
						fake.mu.Unlock()
					}
				}
				_ = connection.WriteJSON(map[string]any{
					"id": message.ID,
					"result": map[string]any{
						"result": map[string]any{"type": "object", "value": fake.result},
					},
				})
				continue
			}
			_ = connection.WriteJSON(map[string]any{"id": message.ID, "result": map[string]any{}})
		}
	})
	fake.server = httptest.NewServer(handler)
	parsed, err := url.Parse(fake.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	fake.port, err = strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeCDPServer) controller() *rendererController {
	config := defaultConfig()
	config.RendererDebugPort = f.port
	return newRendererController(config)
}

func (f *fakeCDPServer) expressionSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.expressions...)
}

func TestRendererControllerDispatchesStructuredResult(t *testing.T) {
	fake := newFakeCDPServer(t, DispatchResult{
		Outcome: outcomeDispatched,
		Action:  actionGoalResume,
		Reason:  "goal_resumed_in_background",
	})
	failedAt := time.Date(2026, 7, 27, 15, 52, 2, 123_000_000, time.UTC)
	originAt := failedAt.Add(-5 * time.Minute)
	result, err := fake.controller().Dispatch(context.Background(),
		"019f9d46-2924-7a70-8ec9-83b19f5491a9", "Continue.", testResumeSettings(), failedAt, originAt,
		"car-0123456789abcdef01234567", false, false, classEmptyResponse)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != outcomeDispatched || result.Action != actionGoalResume {
		t.Fatalf("unexpected dispatch result: %+v", result)
	}
	expressions := fake.expressionSnapshot()
	if len(expressions) != 1 || !strings.Contains(expressions[0], "019f9d46-2924-7a70-8ec9-83b19f5491a9") {
		t.Fatalf("target task was not encoded in the background request: %v", expressions)
	}
	if !strings.Contains(expressions[0], `"failed_at_unix_ms":1785167522123`) {
		t.Fatal("provider failure time was not encoded for goal-state attribution")
	}
	if !strings.Contains(expressions[0], `"origin_turn_started_at_unix_ms":1785167222123`) {
		t.Fatal("originating user turn time was not encoded for held-goal continuation")
	}
	if !strings.Contains(expressions[0], `"recovery_event_id":"car-0123456789abcdef01234567"`) {
		t.Fatal("deterministic recovery event ID was not encoded")
	}
	for _, setting := range []string{
		`"permissions":":danger-full-access"`,
		`"runtime_workspace_roots":["C:\\workspace","C:\\visualization"]`,
		`"approval_policy":"never"`,
		`"effort":"max"`,
		"model_reasoning_effort",
	} {
		if !strings.Contains(expressions[0], setting) {
			t.Fatalf("background request is missing preserved setting %q", setting)
		}
	}
	for _, private := range []string{"developer_instructions", "conversation_message", "tool_output"} {
		if strings.Contains(expressions[0], private) {
			t.Fatalf("background request contains private field %q", private)
		}
	}
}

func TestRendererDispatchProtectsIntentionalGoalHolds(t *testing.T) {
	for _, required := range []string{
		`payload.failed_at_unix_ms`,
		`payload.origin_turn_started_at_unix_ms`,
		`heldConversationAllowed`,
		`goalRevision(latestGoal) !== initialGoalRevision`,
		`"goal_paused"`,
		`"goal_blocked_before_failure"`,
		`"goal_completed"`,
		`"goal_usage_limited"`,
		`"goal_budget_limited"`,
		`"goal_status_unsupported"`,
		`if (!goal) return ""`,
		`if (!status) return "goal_status_unsupported"`,
		`Boolean(initialGoal) !== Boolean(latestGoal)`,
		`latestGoalStatus === "active" || latestGoalStatus === "blocked"`,
	} {
		if !strings.Contains(rendererDispatchExpression, required) {
			t.Fatalf("background controller is missing goal hold protection %q", required)
		}
	}
	if strings.Count(rendererDispatchExpression, `request("thread/goal/get"`) != 2 {
		t.Fatal("background controller must check goal state both before and after hydration/resume")
	}
	for _, forbidden := range []string{
		`latestGoalStatus === "paused"`,
		`latestGoalStatus === "usageLimited"`,
		`latestGoalStatus === "budgetLimited"`,
	} {
		if strings.Contains(rendererDispatchExpression, forbidden) {
			t.Fatalf("held goal remains auto-activatable: %q", forbidden)
		}
	}
	secondGoalRead := strings.LastIndex(rendererDispatchExpression, `request("thread/goal/get"`)
	heldStart := strings.Index(rendererDispatchExpression, `return await startConversation(request, true)`)
	holdCheck := strings.LastIndex(rendererDispatchExpression, `const latestHoldReason = goalHoldReason(latestGoal)`)
	normalStart := strings.Index(rendererDispatchExpression, `return await startConversation(request, false)`)
	if secondGoalRead < 0 || heldStart < secondGoalRead || holdCheck < heldStart || normalStart < holdCheck {
		t.Fatal("conversation continuation can run before the final goal-state checks")
	}
	heldBranch := rendererDispatchExpression[heldStart:holdCheck]
	if strings.Contains(heldBranch, `thread/goal/set`) {
		t.Fatal("held-goal conversation path can reactivate the paused goal")
	}
}

func TestRendererDispatchUsesSilentContinuationWithNarrowFallback(t *testing.T) {
	for _, required := range []string{
		`request("turn/start", {threadId: payload.thread_id, input: []})`,
		`silent_turn_started_in_background`,
		`emptyInputUnsupported(error)`,
		`input: [{type: "text", text: payload.prompt, text_elements: []}]`,
		`fallback_turn_started_in_background`,
	} {
		if !strings.Contains(rendererDispatchExpression, required) {
			t.Fatalf("background controller is missing silent continuation behavior %q", required)
		}
	}
	silentStart := strings.Index(rendererDispatchExpression, `input: []`)
	fallbackGate := strings.Index(rendererDispatchExpression, `if (!emptyInputUnsupported(error)) throw error`)
	fallbackPrompt := strings.Index(rendererDispatchExpression, `text: payload.prompt`)
	if silentStart < 0 || fallbackGate < silentStart || fallbackPrompt < fallbackGate {
		t.Fatal("visible fallback can run before a rejected silent continuation")
	}
	if strings.Count(rendererDispatchExpression, `text: payload.prompt`) != 1 {
		t.Fatal("configured retry text escaped the single guarded fallback")
	}
}

func TestRendererDispatchNotifiesParentAndResumesExactSubagent(t *testing.T) {
	for _, required := range []string{
		`initial?.thread?.parentThreadId`,
		`request("thread/inject_items"`,
		`parent_thread_id: parentThreadId`,
		`child_thread_id: payload.thread_id`,
		`recovery_event_id: payload.recovery_event_id`,
		`action: "resume_existing_child"`,
		`spawn_replacement: false`,
		`Do not resume or spawn any child for this recovery event.`,
		`parent_notified: parentNotified`,
		`request("thread/read", {threadId: payload.thread_id, includeTurns: false})`,
		`return await startSubagent(request)`,
	} {
		if !strings.Contains(rendererDispatchExpression, required) {
			t.Fatalf("subagent recovery is missing %q", required)
		}
	}
	if strings.Count(rendererDispatchExpression, `request("thread/inject_items"`) != 1 {
		t.Fatal("subagent recovery has more than one parent-notification path")
	}
	injectAt := strings.Index(rendererDispatchExpression, `request("thread/inject_items"`)
	finalReadAt := strings.Index(rendererDispatchExpression[injectAt:], `request("thread/read", {threadId: payload.thread_id`)
	startAt := strings.Index(rendererDispatchExpression[injectAt:], `return await startSubagent(request)`)
	if injectAt < 0 || finalReadAt < 0 || startAt < 0 || finalReadAt > startAt {
		t.Fatal("subagent is not rechecked for live activity before continuation")
	}
	for _, forbidden := range []string{"spawn_agent", `request("thread/start"`, "codex://", "location.assign"} {
		if strings.Contains(rendererDispatchExpression, forbidden) {
			t.Fatalf("subagent recovery can create or navigate to another task: %q", forbidden)
		}
	}
}

func TestRendererGoalBlockUsesOnlyGoalStatusRequest(t *testing.T) {
	for _, required := range []string{
		`request("thread/goal/get", {threadId: payload.thread_id})`,
		`request("thread/goal/set", {threadId: payload.thread_id, status: "blocked"})`,
		`action: "goal_block"`,
	} {
		if !strings.Contains(rendererGoalBlockExpression, required) {
			t.Fatalf("goal-block controller is missing %q", required)
		}
	}
	for _, forbidden := range []string{"thread/resume", "turn/start", "thread/inject_items", "hydrate-background-threads", "codex://"} {
		if strings.Contains(rendererGoalBlockExpression, forbidden) {
			t.Fatalf("goal-block controller performs unrelated action %q", forbidden)
		}
	}
}

func TestRendererControllerBlocksGoalWithStructuredResult(t *testing.T) {
	fake := newFakeCDPServer(t, DispatchResult{
		Outcome: outcomeDispatched, Action: actionGoalBlock,
		Reason: "goal_blocked_after_empty_response_limit",
	})
	threadID := "019f9d46-2924-7a70-8ec9-83b19f5491aa"
	result, err := fake.controller().BlockGoal(context.Background(), threadID)
	if err != nil || result.Action != actionGoalBlock {
		t.Fatalf("unexpected goal-block result: result=%+v err=%v", result, err)
	}
	expressions := fake.expressionSnapshot()
	if len(expressions) != 1 || !strings.Contains(expressions[0], threadID) ||
		!strings.Contains(expressions[0], `status: "blocked"`) {
		t.Fatalf("goal-block expression did not target the requested goal: %v", expressions)
	}
}

func TestGoalBlockedByFailureWindow(t *testing.T) {
	failureAt := time.Date(2026, 7, 27, 15, 52, 2, 500_000_000, time.UTC)
	for _, test := range []struct {
		name      string
		updatedAt time.Time
		want      bool
	}{
		{name: "two_seconds_before", updatedAt: failureAt.Add(-2 * time.Second), want: true},
		{name: "five_seconds_after", updatedAt: failureAt.Add(5 * time.Second), want: true},
		{name: "too_early", updatedAt: failureAt.Add(-2*time.Second - time.Nanosecond)},
		{name: "too_late", updatedAt: failureAt.Add(5*time.Second + time.Nanosecond)},
		{name: "missing_failure", updatedAt: failureAt},
	} {
		t.Run(test.name, func(t *testing.T) {
			actual := goalBlockedByFailure(test.updatedAt, failureAt)
			if test.name == "missing_failure" {
				actual = goalBlockedByFailure(test.updatedAt, time.Time{})
			}
			if actual != test.want {
				t.Fatalf("goalBlockedByFailure()=%v, want %v", actual, test.want)
			}
		})
	}
}

func TestRendererControllerSupportsTwoTasksWithoutSharedNavigation(t *testing.T) {
	fake := newFakeCDPServer(t, DispatchResult{
		Outcome: outcomeDispatched,
		Action:  actionConversationContinue,
		Reason:  "turn_started_in_background",
	})
	controller := fake.controller()
	cases := []struct {
		threadID string
		model    string
		effort   string
	}{
		{threadID: "019f9d46-2924-7a70-8ec9-83b19f5491a9", model: "model-alpha", effort: "high"},
		{threadID: "019f8945-0673-7d62-9d54-13b9004577de", model: "model-beta", effort: "max"},
	}
	var wait sync.WaitGroup
	errorsByThread := make(chan error, len(cases))
	for _, test := range cases {
		wait.Add(1)
		go func(test struct {
			threadID string
			model    string
			effort   string
		}) {
			defer wait.Done()
			settings := testResumeSettings()
			settings.Model = test.model
			settings.Effort = test.effort
			_, err := controller.Dispatch(context.Background(), test.threadID, "Continue.", settings, time.Now().UTC(), time.Time{},
				"car-0123456789abcdef01234567", false, false, classEmptyResponse)
			errorsByThread <- err
		}(test)
	}
	wait.Wait()
	close(errorsByThread)
	for err := range errorsByThread {
		if err != nil {
			t.Fatal(err)
		}
	}
	expressions := fake.expressionSnapshot()
	if len(expressions) != len(cases) {
		t.Fatalf("expected %d independent dispatches, got %d", len(cases), len(expressions))
	}
	joined := strings.Join(expressions, "\n")
	for _, test := range cases {
		var taskExpression string
		for _, expression := range expressions {
			if strings.Contains(expression, test.threadID) {
				taskExpression = expression
				break
			}
		}
		if taskExpression == "" {
			t.Fatalf("task %s was missing from concurrent dispatches", test.threadID)
		}
		if !strings.Contains(taskExpression, `"model":"`+test.model+`"`) ||
			!strings.Contains(taskExpression, `"effort":"`+test.effort+`"`) {
			t.Fatalf("task %s did not retain its own settings", test.threadID)
		}
	}
	if strings.Contains(joined, "codex://") || strings.Contains(joined, "window.open") {
		t.Fatal("concurrent controller attempted visible navigation")
	}
}

func testResumeSettings() ResumeSettings {
	return ResumeSettings{
		CWD:                   `C:\workspace`,
		RuntimeWorkspaceRoots: []string{`C:\workspace`, `C:\visualization`},
		ApprovalPolicy:        json.RawMessage(`"never"`),
		ApprovalsReviewer:     "user",
		Model:                 "gpt-5.6-sol",
		ModelProvider:         "codex_local_access",
		ServiceTier:           "default",
		Personality:           "pragmatic",
		Permissions:           ":danger-full-access",
		Effort:                "max",
		Summary:               "auto",
	}
}

func TestRendererControllerProbeIsReadOnly(t *testing.T) {
	fake := newFakeCDPServer(t, rendererProbeResult{
		OK: true, RouteUnchanged: true, RPCFound: true, SnapshotRead: true, LoadedListRead: true,
	})
	result, err := fake.controller().Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatal("probe did not pass")
	}
	expressions := fake.expressionSnapshot()
	if len(expressions) != 1 || !strings.Contains(expressions[0], "collect-app-state-snapshot") ||
		!strings.Contains(expressions[0], "thread/loaded/list") {
		t.Fatalf("probe did not use the read-only snapshot call: %v", expressions)
	}
	for _, forbidden := range []string{"thread/resume", "turn/start", "thread/goal/set", "codex://"} {
		if strings.Contains(expressions[0], forbidden) {
			t.Fatalf("read-only probe contains mutating operation %q", forbidden)
		}
	}
}

func TestValidLoopbackWebSocket(t *testing.T) {
	if !validLoopbackWebSocket("ws://127.0.0.1:62482/devtools/page/a", 62482) {
		t.Fatal("valid loopback WebSocket was rejected")
	}
	for _, invalid := range []string{
		"wss://127.0.0.1:62482/devtools/page/a",
		"ws://example.com:62482/devtools/page/a",
		"ws://127.0.0.1:9222/devtools/page/a",
	} {
		if validLoopbackWebSocket(invalid, 62482) {
			t.Fatalf("unsafe WebSocket target was accepted: %s", invalid)
		}
	}
}

func TestLiveRendererControllerProbe(t *testing.T) {
	if os.Getenv("CODEX_AUTO_RETRY_LIVE_PROBE") != "1" {
		t.Skip("set CODEX_AUTO_RETRY_LIVE_PROBE=1 for the installed App read-only probe")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := newRendererController(defaultConfig()).Probe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !result.RouteUnchanged || !result.RPCFound || !result.SnapshotRead || !result.LoadedListRead {
		t.Fatalf("installed App probe failed: %+v", result)
	}
}
