package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type staticSharedServer struct {
	endpoint string
	home     string
	err      error
}

func (s staticSharedServer) Endpoint() string               { return s.endpoint }
func (s staticSharedServer) Ensure(context.Context) error   { return s.err }
func (s staticSharedServer) SupportsHome(value string) bool { return strings.EqualFold(value, s.home) }

type staticDesktopChecker struct {
	state desktopTransportState
	err   error
}

func (c staticDesktopChecker) State(context.Context) (desktopTransportState, error) {
	return c.state, c.err
}

type fakeAppServerClient struct {
	connection *websocket.Conn
	mu         sync.Mutex
}

type fakeAppServer struct {
	server *httptest.Server

	mu                sync.Mutex
	clients           map[*fakeAppServerClient]struct{}
	loadedThreads     map[string]bool
	threadID          string
	parentThreadID    string
	status            string
	goal              *appGoal
	calls             []string
	resumedThreadIDs  []string
	injectedThreadIDs []string
	turnInputs        [][]any
	providerRequests  int
}

func newFakeAppServer(t *testing.T, threadID string) *fakeAppServer {
	t.Helper()
	fake := &fakeAppServer{
		clients:       make(map[*fakeAppServerClient]struct{}),
		loadedThreads: make(map[string]bool),
		threadID:      threadID,
		status:        "idle",
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		connection, err := upgrader.Upgrade(response, request, nil)
		if err != nil {
			return
		}
		client := &fakeAppServerClient{connection: connection}
		fake.mu.Lock()
		fake.clients[client] = struct{}{}
		fake.mu.Unlock()
		defer func() {
			fake.mu.Lock()
			delete(fake.clients, client)
			fake.mu.Unlock()
			_ = connection.Close()
		}()
		for {
			var request map[string]json.RawMessage
			if err := connection.ReadJSON(&request); err != nil {
				return
			}
			var method string
			_ = json.Unmarshal(request["method"], &method)
			if len(request["id"]) == 0 {
				continue
			}
			result, requestErr := fake.handle(method, request["params"])
			message := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(request["id"])}
			if requestErr != "" {
				message["error"] = map[string]any{"code": -32000, "message": requestErr}
			} else {
				message["result"] = result
			}
			client.mu.Lock()
			err = connection.WriteJSON(message)
			client.mu.Unlock()
			if err != nil {
				return
			}
		}
	})
	fake.server = httptest.NewServer(handler)
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeAppServer) endpoint() string {
	return "ws" + strings.TrimPrefix(f.server.URL, "http")
}

func (f *fakeAppServer) handle(method string, raw json.RawMessage) (any, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, method)
	switch method {
	case "initialize":
		return map[string]any{"serverInfo": map[string]any{"name": "fake", "version": "1"}}, ""
	case "thread/loaded/list":
		data := make([]string, 0, len(f.loadedThreads))
		for threadID, loaded := range f.loadedThreads {
			if loaded {
				data = append(data, threadID)
			}
		}
		return map[string]any{"data": data}, ""
	case "thread/resume":
		var params struct {
			ThreadID string `json:"threadId"`
		}
		_ = json.Unmarshal(raw, &params)
		f.loadedThreads[params.ThreadID] = true
		f.resumedThreadIDs = append(f.resumedThreadIDs, params.ThreadID)
		if params.ThreadID == f.threadID {
			return f.threadResultLocked(), ""
		}
		return map[string]any{"thread": map[string]any{
			"id": params.ThreadID, "status": map[string]any{"type": "idle"}, "source": map[string]any{},
		}}, ""
	case "thread/read":
		var params struct {
			ThreadID string `json:"threadId"`
		}
		_ = json.Unmarshal(raw, &params)
		if !f.loadedThreads[params.ThreadID] {
			return nil, "thread not found until resumed"
		}
		return f.threadResultLocked(), ""
	case "thread/goal/get":
		return map[string]any{"goal": f.goal}, ""
	case "thread/goal/set":
		var params struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(raw, &params)
		if f.goal == nil {
			f.goal = &appGoal{}
		}
		f.goal.Status = params.Status
		f.goal.UpdatedAt = json.RawMessage(`"2026-07-31T08:00:00Z"`)
		return map[string]any{"goal": f.goal}, ""
	case "turn/start":
		var params struct {
			Input []any `json:"input"`
		}
		_ = json.Unmarshal(raw, &params)
		f.turnInputs = append(f.turnInputs, params.Input)
		f.providerRequests++
		go f.broadcast("turn/started", map[string]any{"threadId": f.threadID, "turn": map[string]any{"id": "retry-turn"}})
		return map[string]any{"turn": map[string]any{"id": "retry-turn"}}, ""
	case "thread/inject_items":
		var params struct {
			ThreadID string `json:"threadId"`
		}
		_ = json.Unmarshal(raw, &params)
		if !f.loadedThreads[params.ThreadID] {
			return nil, "thread not found until resumed"
		}
		f.injectedThreadIDs = append(f.injectedThreadIDs, params.ThreadID)
		return map[string]any{}, ""
	default:
		return nil, "unsupported method"
	}
}

func (f *fakeAppServer) threadResultLocked() map[string]any {
	source := map[string]any{}
	if f.parentThreadID != "" {
		source = map[string]any{"subAgent": map[string]any{"thread_spawn": map[string]any{"parent_thread_id": f.parentThreadID}}}
	}
	return map[string]any{"thread": map[string]any{
		"id": f.threadID, "parentThreadId": f.parentThreadID,
		"status": map[string]any{"type": f.status}, "source": source,
	}}
}

func (f *fakeAppServer) broadcast(method string, params any) {
	f.mu.Lock()
	clients := make([]*fakeAppServerClient, 0, len(f.clients))
	for client := range f.clients {
		clients = append(clients, client)
	}
	f.mu.Unlock()
	for _, client := range clients {
		client.mu.Lock()
		_ = client.connection.WriteJSON(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
		client.mu.Unlock()
	}
}

func (f *fakeAppServer) snapshot() ([]string, [][]any, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	calls := append([]string(nil), f.calls...)
	inputs := append([][]any(nil), f.turnInputs...)
	return calls, inputs, f.providerRequests
}

func (f *fakeAppServer) markLoaded(threadIDs ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, threadID := range threadIDs {
		f.loadedThreads[threadID] = true
	}
}

func (f *fakeAppServer) recoveryTargets() ([]string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.resumedThreadIDs...), append([]string(nil), f.injectedThreadIDs...)
}

func newTestSharedController(fake *fakeAppServer, home string) *sharedAppServerController {
	return &sharedAppServerController{
		server:  staticSharedServer{endpoint: fake.endpoint(), home: home},
		checker: staticDesktopChecker{state: desktopSharedServer},
		settingsForThread: func(string, string) (ResumeSettings, error) {
			return testResumeSettings(), nil
		},
	}
}

func TestSharedControllerResumesUnloadedTaskAndDesktopObservesRetry(t *testing.T) {
	threadID := "019fa94e-0103-7183-b405-36bd307b6db3"
	home := `C:\Users\test\.codex`
	fake := newFakeAppServer(t, threadID)
	observer, err := dialAppServerRPC(context.Background(), fake.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()

	observed := make(chan string, 1)
	go func() {
		_ = observer.connection.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			var message struct {
				Method string `json:"method"`
				Params struct {
					ThreadID string `json:"threadId"`
				} `json:"params"`
			}
			if err := observer.connection.ReadJSON(&message); err != nil {
				return
			}
			if message.Method == "turn/started" {
				observed <- message.Params.ThreadID
				return
			}
		}
	}()

	result, err := newTestSharedController(fake, home).Dispatch(
		context.Background(), threadID, "继续", testResumeSettings(), time.Now().UTC(),
		time.Now().Add(-time.Minute).UTC(), "", false, false, classEmptyResponse, home,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != outcomeDispatched || result.Action != actionConversationContinue {
		t.Fatalf("unexpected dispatch result: %+v", result)
	}
	select {
	case observedThread := <-observed:
		if observedThread != threadID {
			t.Fatalf("desktop observed retry for a different task: %s", observedThread)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("desktop client did not observe the retry started by the watchdog client")
	}
	calls, inputs, providerRequests := fake.snapshot()
	resumeIndex, readIndex := -1, -1
	for index, method := range calls {
		if method == "thread/resume" && resumeIndex == -1 {
			resumeIndex = index
		}
		if method == "thread/read" && readIndex == -1 {
			readIndex = index
		}
	}
	if resumeIndex == -1 || readIndex == -1 || resumeIndex > readIndex {
		t.Fatalf("unloaded task was read before it was resumed: %v", calls)
	}
	if len(inputs) != 1 || len(inputs[0]) != 0 || providerRequests != 1 {
		t.Fatalf("silent same-task retry was not issued exactly once: inputs=%v requests=%d", inputs, providerRequests)
	}
}

func TestSharedControllerLoadsParentBeforeResumingUnloadedSubagent(t *testing.T) {
	threadID := "019fa94e-0103-7183-b405-36bd307b6dba"
	parentThreadID := "019fa94e-0103-7183-b405-36bd307b6dbb"
	home := `C:\Users\test\.codex`
	fake := newFakeAppServer(t, threadID)
	fake.parentThreadID = parentThreadID

	result, err := newTestSharedController(fake, home).Dispatch(
		context.Background(), threadID, "继续", testResumeSettings(), time.Now().UTC(),
		time.Now().Add(-time.Minute).UTC(), "car-0123456789abcdef01234567", false, false,
		classEmptyResponse, home,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != outcomeDispatched || result.Action != actionSubagentContinue || !result.ParentNotified {
		t.Fatalf("unexpected subagent dispatch result: %+v", result)
	}
	resumed, injected := fake.recoveryTargets()
	if len(resumed) != 2 || resumed[0] != threadID || resumed[1] != parentThreadID {
		t.Fatalf("child and parent were not loaded in recovery order: %v", resumed)
	}
	if len(injected) != 1 || injected[0] != parentThreadID {
		t.Fatalf("recovery notice was not injected into the exact parent: %v", injected)
	}
	_, inputs, providerRequests := fake.snapshot()
	if len(inputs) != 1 || len(inputs[0]) != 0 || providerRequests != 1 {
		t.Fatalf("exact child was not silently resumed once: inputs=%v requests=%d", inputs, providerRequests)
	}
}

func TestSharedControllerDoesNotResumeIntentionallyPausedGoal(t *testing.T) {
	threadID := "019fa94e-0103-7183-b405-36bd307b6db4"
	home := `C:\Users\test\.codex`
	fake := newFakeAppServer(t, threadID)
	fake.markLoaded(threadID)
	fake.goal = &appGoal{Status: "paused", UpdatedAt: json.RawMessage(`"2026-07-31T08:00:00Z"`)}
	result, err := newTestSharedController(fake, home).Dispatch(
		context.Background(), threadID, "继续", testResumeSettings(), time.Now().UTC(),
		time.Time{}, "", false, false, classEmptyResponse, home,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != outcomeNotApplicable || result.Reason != "goal_paused" {
		t.Fatalf("intentional goal pause was not preserved: %+v", result)
	}
	calls, _, providerRequests := fake.snapshot()
	if providerRequests != 0 || containsString(calls, "thread/goal/set") || containsString(calls, "turn/start") {
		t.Fatalf("paused goal was changed or continued: %v", calls)
	}
}

func TestSharedControllerResumesGoalBlockedByProviderFailure(t *testing.T) {
	threadID := "019fa94e-0103-7183-b405-36bd307b6dbd"
	home := `C:\Users\test\.codex`
	failureAt := time.Date(2026, 7, 31, 8, 0, 0, 0, time.UTC)
	fake := newFakeAppServer(t, threadID)
	fake.markLoaded(threadID)
	fake.goal = &appGoal{Status: "blocked", UpdatedAt: json.RawMessage(`1785484800`)}

	result, err := newTestSharedController(fake, home).Dispatch(
		context.Background(), threadID, "继续", testResumeSettings(), failureAt,
		failureAt.Add(-time.Minute), "", false, false, classServer, home,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != outcomeDispatched || result.Action != actionGoalResume || fake.goal.Status != "active" {
		t.Fatalf("provider-blocked goal was not resumed: result=%+v goal=%+v", result, fake.goal)
	}
	_, _, providerRequests := fake.snapshot()
	if providerRequests != 0 {
		t.Fatalf("native goal recovery also started a duplicate conversation turn: %d", providerRequests)
	}
}

func TestSharedControllerPreservesGoalBlockedAfterFailure(t *testing.T) {
	threadID := "019fa94e-0103-7183-b405-36bd307b6dbe"
	home := `C:\Users\test\.codex`
	failureAt := time.Date(2026, 7, 31, 8, 0, 0, 0, time.UTC)
	fake := newFakeAppServer(t, threadID)
	fake.markLoaded(threadID)
	fake.goal = &appGoal{Status: "blocked", UpdatedAt: json.RawMessage(`1785484860`)}

	result, err := newTestSharedController(fake, home).Dispatch(
		context.Background(), threadID, "继续", testResumeSettings(), failureAt,
		failureAt.Add(-time.Minute), "", false, false, classServer, home,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != outcomeNotApplicable || result.Reason != "goal_blocked_before_failure" || fake.goal.Status != "blocked" {
		t.Fatalf("later goal block was not preserved: result=%+v goal=%+v", result, fake.goal)
	}
}

func TestSharedControllerRetriesLaterConversationWithoutUnpausingGoal(t *testing.T) {
	threadID := "019fa94e-0103-7183-b405-36bd307b6dbf"
	home := `C:\Users\test\.codex`
	pausedAt := time.Date(2026, 7, 31, 8, 0, 0, 0, time.UTC)
	turnStartedAt := pausedAt.Add(time.Minute)
	fake := newFakeAppServer(t, threadID)
	fake.markLoaded(threadID)
	fake.goal = &appGoal{Status: "paused", UpdatedAt: json.RawMessage(`1785484800`)}

	result, err := newTestSharedController(fake, home).Dispatch(
		context.Background(), threadID, "继续", testResumeSettings(), turnStartedAt.Add(time.Minute),
		turnStartedAt, "", false, false, classEmptyResponse, home,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != outcomeDispatched || result.Action != actionConversationContinue || fake.goal.Status != "paused" {
		t.Fatalf("held-goal conversation was not recovered independently: result=%+v goal=%+v", result, fake.goal)
	}
	_, inputs, providerRequests := fake.snapshot()
	if len(inputs) != 1 || len(inputs[0]) != 0 || providerRequests != 1 {
		t.Fatalf("held-goal conversation was not silently continued once: inputs=%v requests=%d", inputs, providerRequests)
	}
}

func TestSharedControllerBlocksUnloadedGoalAtRetryLimit(t *testing.T) {
	threadID := "019fa94e-0103-7183-b405-36bd307b6db5"
	home := `C:\Users\test\.codex`
	fake := newFakeAppServer(t, threadID)
	fake.goal = &appGoal{Status: "active", UpdatedAt: json.RawMessage(`"2026-07-31T08:00:00Z"`)}
	settings := testResumeSettings()
	result, err := newTestSharedController(fake, home).BlockGoal(
		context.Background(), threadID, &settings, home,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != outcomeDispatched || result.Action != actionGoalBlock || fake.goal.Status != "blocked" {
		t.Fatalf("unloaded goal was not blocked: result=%+v goal=%+v", result, fake.goal)
	}
	calls, _, _ := fake.snapshot()
	if !containsString(calls, "thread/resume") || !containsString(calls, "thread/goal/set") {
		t.Fatalf("unloaded goal recovery omitted resume or goal update: %v", calls)
	}
}

func TestSharedControllerBlocksLoadedGoalWithoutRolloutSettings(t *testing.T) {
	threadID := "019fa94e-0103-7183-b405-36bd307b6db8"
	home := `C:\Users\test\.codex`
	fake := newFakeAppServer(t, threadID)
	fake.markLoaded(threadID)
	fake.goal = &appGoal{Status: "active", UpdatedAt: json.RawMessage(`"2026-07-31T08:00:00Z"`)}
	result, err := newTestSharedController(fake, home).BlockGoal(context.Background(), threadID, nil, home)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != outcomeDispatched || result.Action != actionGoalBlock || fake.goal.Status != "blocked" {
		t.Fatalf("loaded goal incorrectly required rollout settings: result=%+v goal=%+v", result, fake.goal)
	}
}

func TestSharedControllerFailsClosedWhenUnloadedGoalHasNoSettings(t *testing.T) {
	threadID := "019fa94e-0103-7183-b405-36bd307b6db9"
	home := `C:\Users\test\.codex`
	fake := newFakeAppServer(t, threadID)
	fake.goal = &appGoal{Status: "active", UpdatedAt: json.RawMessage(`"2026-07-31T08:00:00Z"`)}
	result, err := newTestSharedController(fake, home).BlockGoal(context.Background(), threadID, nil, home)
	if err != nil || result.Outcome != outcomeRetryLater || result.Reason != "thread_settings_unavailable" {
		t.Fatalf("unloaded goal used replacement settings: result=%+v err=%v", result, err)
	}
}

func TestSharedControllerRequiresOneCodexRestartForLegacyTransport(t *testing.T) {
	controller := &sharedAppServerController{
		server:  staticSharedServer{home: `C:\Users\test\.codex`},
		checker: staticDesktopChecker{state: desktopLegacyStdio},
	}
	result, err := controller.Dispatch(
		context.Background(), "019fa94e-0103-7183-b405-36bd307b6db6", "继续", testResumeSettings(),
		time.Now().UTC(), time.Time{}, "", false, false, classServer, `C:\Users\test\.codex`,
	)
	if err != nil || result.Outcome != outcomeRetryLater || result.Reason != "codex_restart_required" {
		t.Fatalf("legacy transport did not produce a bounded restart state: result=%+v err=%v", result, err)
	}
}

func TestAppGoalUpdatedAtAcceptsProtocolTimestampFormats(t *testing.T) {
	want := time.Date(2026, 7, 31, 8, 0, 0, 0, time.UTC)
	for _, raw := range []string{
		`1785484800`,
		`1785484800000`,
		`"1785484800000"`,
		`"2026-07-31T08:00:00Z"`,
	} {
		if got := appGoalUpdatedAt(&appGoal{UpdatedAt: json.RawMessage(raw)}); !got.Equal(want) {
			t.Fatalf("goal timestamp %s parsed as %s, want %s", raw, got, want)
		}
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
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
