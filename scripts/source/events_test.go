package main

import (
	"encoding/json"
	"testing"
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
	line := []byte(`{"timestamp":"2026-07-26T09:00:00Z","type":"response_item","payload":{"type":"message","content":"HTTP 503 Service Unavailable task_complete"}}` + "\n")
	if _, ok := parseRelevantEvent(line); ok {
		t.Fatal("conversation content must not be parsed as a retry event")
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
