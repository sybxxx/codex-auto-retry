package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type eventEnvelope struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type eventPayload struct {
	Type   string          `json:"type"`
	TurnID string          `json:"turn_id"`
	Error  json.RawMessage `json:"error"`
}

func parseRelevantEvent(line []byte) (RelevantEvent, bool) {
	if !bytes.Contains(line, []byte(`"event_msg"`)) ||
		(!bytes.Contains(line, []byte(`"task_started"`)) && !bytes.Contains(line, []byte(`"task_complete"`))) {
		return RelevantEvent{}, false
	}
	var envelope eventEnvelope
	if err := json.Unmarshal(line, &envelope); err != nil || envelope.Type != "event_msg" {
		return RelevantEvent{}, false
	}
	var payload eventPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return RelevantEvent{}, false
	}
	if payload.Type != "task_started" && payload.Type != "task_complete" {
		return RelevantEvent{}, false
	}
	timestamp, err := time.Parse(time.RFC3339Nano, envelope.Timestamp)
	if err != nil {
		timestamp = time.Now().UTC()
	}
	return RelevantEvent{
		Kind:      payload.Type,
		TurnID:    payload.TurnID,
		Timestamp: timestamp,
		ErrorText: extractErrorText(payload.Error),
	}, true
}

func extractErrorText(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	var text string
	if json.Unmarshal(trimmed, &text) == nil {
		return text
	}
	var value any
	if json.Unmarshal(trimmed, &value) != nil {
		return ""
	}
	parts := make([]string, 0, 4)
	collectErrorStrings(value, &parts, 0)
	return strings.Join(parts, " ")
}

func collectErrorStrings(value any, parts *[]string, depth int) {
	if depth > 4 || len(*parts) >= 12 {
		return
	}
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) != "" {
			*parts = append(*parts, typed)
		}
	case []any:
		for _, item := range typed {
			collectErrorStrings(item, parts, depth+1)
		}
	case map[string]any:
		for _, key := range []string{"type", "code", "message", "error", "detail", "reason"} {
			if item, ok := typed[key]; ok {
				collectErrorStrings(item, parts, depth+1)
			}
		}
	}
}

func eventKey(threadID string, event RelevantEvent) string {
	sum := sha256.Sum256([]byte(event.ErrorText))
	return fmt.Sprintf("%s|%s|%s|%s|%s", threadID, event.Kind, event.TurnID, event.Timestamp.UTC().Format(time.RFC3339Nano), hex.EncodeToString(sum[:6]))
}
