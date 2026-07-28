package main

import (
	"encoding/json"
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

func TestIgnoreConversationContent(t *testing.T) {
	line := []byte(`{"timestamp":"2026-07-26T09:00:00Z","type":"response_item","payload":{"type":"message","content":"HTTP 503 Service Unavailable task_complete thread_goal_updated paused blocked"}}` + "\n")
	if _, ok := parseRelevantEvent(line); ok {
		t.Fatal("conversation content must not be parsed as a retry event")
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
