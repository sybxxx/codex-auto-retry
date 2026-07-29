package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParseRelevantEvent(t *testing.T) {
	line := makeEventLine(t, "2026-07-26T09:00:00.123Z", "task_complete", "turn-1", "HTTP 503 Service Unavailable")
	event, ok := parseRelevantEvent(line)
	if !ok {
		t.Fatal("expected relevant event")
	}
	if event.Kind != "task_complete" || event.TurnID != "turn-1" || event.ErrorText != "HTTP 503 Service Unavailable" {
		t.Fatalf("unexpected event: %+v", event)
	}
}

func TestParseObjectError(t *testing.T) {
	payload := map[string]any{
		"type":    "task_complete",
		"turn_id": "turn-2",
		"error": map[string]any{
			"type":    "provider_error",
			"code":    "auth_unavailable",
			"message": "no auth available",
		},
	}
	envelope := map[string]any{"timestamp": "2026-07-26T09:00:00Z", "type": "event_msg", "payload": payload}
	line, _ := json.Marshal(envelope)
	event, ok := parseRelevantEvent(append(line, '\n'))
	if !ok || event.ErrorText == "" {
		t.Fatalf("unexpected event: %+v, ok=%v", event, ok)
	}
	decision := classifyFailure(event.ErrorText, defaultConfig())
	if !decision.Retry {
		t.Fatalf("object error was not retryable: %+v", decision)
	}
}

func TestParseEmptySuccessfulCompletionWithoutRetainingMessage(t *testing.T) {
	for _, lastMessage := range []any{nil, "", "   "} {
		line := makeCompletionLine(t, lastMessage)
		event, ok := parseRelevantEvent(line)
		if !ok {
			t.Fatal("empty successful completion was not parsed")
		}
		if !event.FinalKnown || event.FinalPresent || event.ErrorText != "" {
			t.Fatalf("unexpected empty completion metadata: %+v", event)
		}
	}

	event, ok := parseRelevantEvent(makeCompletionLine(t, "completed answer"))
	if !ok || !event.FinalKnown || !event.FinalPresent {
		t.Fatalf("non-empty final response was not recognized: %+v", event)
	}
}

func TestCompletionWithoutLastAgentFieldRemainsBackwardCompatible(t *testing.T) {
	event, ok := parseRelevantEvent(makeEventLine(t, "2026-07-26T09:00:00Z", "task_complete", "turn-legacy", nil))
	if !ok || event.FinalKnown || event.FinalPresent {
		t.Fatalf("legacy completion state changed: %+v", event)
	}
}

func TestParseTurnAbortedLifecycleEvent(t *testing.T) {
	payload := map[string]any{
		"type":    "turn_aborted",
		"turn_id": "turn-cancelled",
		"reason":  "interrupted",
	}
	line, err := json.Marshal(map[string]any{
		"timestamp": "2026-07-26T09:00:00Z",
		"type":      "event_msg",
		"payload":   payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	event, ok := parseRelevantEvent(append(line, '\n'))
	if !ok || event.Kind != "turn_aborted" || event.TurnID != "turn-cancelled" || event.AbortReason != "interrupted" {
		t.Fatalf("unexpected abort event: %+v", event)
	}
}

func TestIgnoreConversationContent(t *testing.T) {
	line := []byte(`{"timestamp":"2026-07-26T09:00:00Z","type":"response_item","payload":{"type":"message","content":"HTTP 503 Service Unavailable task_complete thread_goal_updated paused blocked"}}` + "\n")
	if _, ok := parseRelevantEvent(line); ok {
		t.Fatal("conversation content must not be parsed as a retry event")
	}
}

func TestParseProgressWithoutRetainingContent(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{"assistant message", `{"type":"message","role":"assistant","content":"private assistant response"}`},
		{"custom tool result", `{"type":"custom_tool_call_output","output":"private tool output"}`},
		{"function result", `{"type":"function_call_output","output":"private function output"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			line := []byte(`{"timestamp":"2026-07-26T09:00:00Z","type":"response_item","payload":` + test.payload + `}`)
			event, ok := parseRelevantEvent(line)
			if !ok || event.Kind != "task_progress" || event.ErrorText != "" {
				t.Fatalf("progress metadata was not parsed privately: %+v, ok=%v", event, ok)
			}
		})
	}
}

func TestIgnoreNonVisibleResponseItems(t *testing.T) {
	for _, payload := range []string{
		`{"type":"message","role":"user","content":"private user message"}`,
		`{"type":"message","role":"user","content":"private user message","internal_chat_message_metadata_passthrough":{"turn_id":"019fa94e-0103-7183-b405-36bd307b6db6"}}`,
		`{"type":"reasoning","summary":"private reasoning"}`,
	} {
		line := []byte(`{"timestamp":"2026-07-26T09:00:00Z","type":"response_item","payload":` + payload + `}`)
		if _, ok := parseRelevantEvent(line); ok {
			t.Fatalf("non-visible progress was accepted: %s", payload)
		}
	}
}

func TestParseExplicitUserInputMarkerWithoutRetainingContent(t *testing.T) {
	line, err := json.Marshal(map[string]any{
		"timestamp": "2026-07-26T09:00:00Z",
		"type":      "event_msg",
		"payload": map[string]any{
			"type": "user_message", "message": "private user input",
			"images": []any{"private-image"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	event, ok := parseRelevantEvent(append(line, '\n'))
	if !ok || event.Kind != "task_user_input" || event.TurnID != "" || event.ErrorText != "" ||
		event.ParentThreadID != "" || event.RecoveryEventID != "" {
		t.Fatalf("user marker was not privacy-bounded: %+v, ok=%v", event, ok)
	}
}

func TestParseSubagentRecoveryNotice(t *testing.T) {
	parentID := "019fa94e-0103-7183-b405-36bd307b6db7"
	childID := "019fa94e-0103-7183-b405-36bd307b6db8"
	eventID := "car-0123456789abcdef01234567"
	notice := recoveryNoticePrefix + `{"parent_thread_id":"` + parentID + `","child_thread_id":"` + childID +
		`","recovery_event_id":"` + eventID + `","action":"resume_existing_child","spawn_replacement":false}`
	line, err := json.Marshal(map[string]any{
		"timestamp": "2026-07-26T09:00:00Z", "type": "response_item",
		"payload": map[string]any{
			"type": "message", "role": "developer",
			"content": []any{map[string]any{"type": "input_text", "text": notice}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	event, ok := parseRelevantEvent(append(line, '\n'))
	if !ok || event.Kind != "subagent_recovery_notice" || event.ThreadID != childID ||
		event.ParentThreadID != parentID || event.RecoveryEventID != eventID {
		t.Fatalf("recovery notice was not parsed: %+v, ok=%v", event, ok)
	}
	invalidLine, err := json.Marshal(map[string]any{
		"timestamp": "2026-07-26T09:00:00Z", "type": "response_item",
		"payload": map[string]any{
			"type": "message", "role": "developer",
			"content": []any{map[string]any{"type": "input_text", "text": strings.Replace(notice, `"spawn_replacement":false`, `"spawn_replacement":true`, 1)}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := parseRelevantEvent(invalidLine); ok {
		t.Fatal("replacement-spawning notice was accepted")
	}
}

func TestParseGoalLifecycleEventWithoutRetainingObjective(t *testing.T) {
	writtenAt := time.Date(2026, 7, 27, 15, 50, 53, 0, time.UTC)
	line := makeGoalEventLine(t, writtenAt.Add(time.Second), "paused", writtenAt.Unix(), "private objective must be ignored")
	event, ok := parseRelevantEvent(line)
	if !ok {
		t.Fatal("expected goal lifecycle event")
	}
	if event.Kind != "thread_goal_updated" || event.ThreadID != goalEventThreadID || event.GoalStatus != "paused" || !event.GoalUpdatedAt.Equal(writtenAt) {
		t.Fatalf("unexpected goal event: %+v", event)
	}
	if event.ErrorText != "" {
		t.Fatalf("goal objective leaked into parsed event: %q", event.ErrorText)
	}
}

func TestParseGoalUpdatedAtMilliseconds(t *testing.T) {
	updatedAt := time.Date(2026, 7, 27, 15, 52, 8, 123_000_000, time.UTC)
	event, ok := parseRelevantEvent(makeGoalEventLine(t, updatedAt, "active", updatedAt.UnixMilli(), "ignored"))
	if !ok || !event.GoalUpdatedAt.Equal(updatedAt) {
		t.Fatalf("millisecond timestamp was not preserved: %+v, ok=%v", event, ok)
	}
}

func TestRejectGoalLifecycleEventWithoutStatus(t *testing.T) {
	line := makeGoalEventLine(t, time.Now().UTC(), "", time.Now().Unix(), "ignored")
	if _, ok := parseRelevantEvent(line); ok {
		t.Fatal("goal event without a status must be rejected")
	}
}

func TestRejectGoalLifecycleEventWithoutTargetThread(t *testing.T) {
	envelope := map[string]any{
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"type":      "event_msg",
		"payload": map[string]any{
			"type": "thread_goal_updated",
			"goal": map[string]any{"status": "paused", "updatedAt": time.Now().Unix()},
		},
	}
	line, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := parseRelevantEvent(line); ok {
		t.Fatal("goal event without its target task must be rejected")
	}
}

func TestRejectGoalLifecycleEventWithPrefixedTargetThread(t *testing.T) {
	envelope := map[string]any{
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"type":      "event_msg",
		"payload": map[string]any{
			"type":     "thread_goal_updated",
			"threadId": "prefix-" + goalEventThreadID,
			"goal": map[string]any{
				"status": "paused", "updatedAt": time.Now().Unix(),
			},
		},
	}
	line, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := parseRelevantEvent(line); ok {
		t.Fatal("goal event with a non-exact target task ID must be rejected")
	}
}

func makeEventLine(t *testing.T, timestamp, kind, turnID string, errorValue any) []byte {
	t.Helper()
	payload := map[string]any{"type": kind, "turn_id": turnID}
	if kind == "task_complete" {
		payload["error"] = errorValue
	}
	envelope := map[string]any{"timestamp": timestamp, "type": "event_msg", "payload": payload}
	line, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return append(line, '\n')
}

func makeCompletionLine(t *testing.T, lastMessage any) []byte {
	t.Helper()
	payload := map[string]any{
		"type":               "task_complete",
		"turn_id":            "turn-empty",
		"last_agent_message": lastMessage,
	}
	line, err := json.Marshal(map[string]any{
		"timestamp": "2026-07-26T09:00:00Z",
		"type":      "event_msg",
		"payload":   payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	return append(line, '\n')
}

func makeGoalEventLine(t *testing.T, timestamp time.Time, status string, updatedAt int64, objective string) []byte {
	t.Helper()
	envelope := map[string]any{
		"timestamp": timestamp.Format(time.RFC3339Nano),
		"type":      "event_msg",
		"payload": map[string]any{
			"type":     "thread_goal_updated",
			"threadId": goalEventThreadID,
			"goal": map[string]any{
				"threadId":  goalEventThreadID,
				"status":    status,
				"updatedAt": updatedAt,
				"objective": objective,
			},
		},
	}
	line, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return append(line, '\n')
}

const goalEventThreadID = "019fa3f6-a793-78a3-8ae6-947340d954b2"
