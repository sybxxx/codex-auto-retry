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
	result, err := fake.controller().Dispatch(context.Background(),
		"019f9d46-2924-7a70-8ec9-83b19f5491a9", "Continue.", testResumeSettings())
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
			_, err := controller.Dispatch(context.Background(), test.threadID, "Continue.", settings)
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
