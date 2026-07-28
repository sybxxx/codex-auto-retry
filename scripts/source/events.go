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
	Type             string          `json:"type"`
	ThreadID         string          `json:"threadId"`
	TurnID           string          `json:"turn_id"`
	Error            json.RawMessage `json:"error"`
	LastAgentMessage json.RawMessage `json:"last_agent_message"`
	Reason           string          `json:"reason"`
	Goal             *struct {
		ThreadID  string `json:"threadId"`
		Status    string `json:"status"`
		UpdatedAt int64  `json:"updatedAt"`
	} `json:"goal"`
}

func parseRelevantEvent(line []byte) (RelevantEvent, bool) {
	if !bytes.Contains(line, []byte(`"event_msg"`)) ||
		(!bytes.Contains(line, []byte(`"task_started"`)) &&
			!bytes.Contains(line, []byte(`"task_complete"`)) &&
			!bytes.Contains(line, []byte(`"turn_aborted"`)) &&
			!bytes.Contains(line, []byte(`"thread_goal_updated"`))) {
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
	if payload.Type != "task_started" && payload.Type != "task_complete" &&
		payload.Type != "turn_aborted" &&
		payload.Type != "thread_goal_updated" {
		return RelevantEvent{}, false
	}
	timestamp, err := time.Parse(time.RFC3339Nano, envelope.Timestamp)
	if err != nil {
		timestamp = time.Now().UTC()
	}
	event := RelevantEvent{
		Kind:        payload.Type,
		TurnID:      payload.TurnID,
		Timestamp:   timestamp,
		ErrorText:   extractErrorText(payload.Error),
		AbortReason: strings.TrimSpace(payload.Reason),
	}
	if payload.Type == "task_complete" {
		event.FinalKnown, event.FinalPresent = completionMessageState(payload.LastAgentMessage)
	}
	if payload.Type == "thread_goal_updated" {
		if payload.Goal == nil || strings.TrimSpace(payload.Goal.Status) == "" {
			return RelevantEvent{}, false
		}
		goalThreadID := strings.ToLower(strings.TrimSpace(payload.ThreadID))
		if goalThreadID == "" {
			goalThreadID = strings.ToLower(strings.TrimSpace(payload.Goal.ThreadID))
		}
		if goalThreadID == "" || threadIDFromPath(goalThreadID+".jsonl") != goalThreadID {
			return RelevantEvent{}, false
		}
		event.ThreadID = goalThreadID
		event.GoalStatus = strings.TrimSpace(payload.Goal.Status)
		event.GoalUpdatedAt = persistedGoalTime(payload.Goal.UpdatedAt, timestamp)
	}
	return event, true
}

func completionMessageState(raw json.RawMessage) (known bool, present bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return false, false
	}
	if bytes.Equal(trimmed, []byte("null")) {
		return true, false
	}
	var text string
	if json.Unmarshal(trimmed, &text) == nil {
		return true, strings.TrimSpace(text) != ""
	}
	// An unexpected non-string value is treated as present so a future schema
	// change fails closed instead of creating a false retry.
	return true, true
}

func persistedGoalTime(value int64, fallback time.Time) time.Time {
	if value <= 0 {
		return fallback
	}
	if value > 10_000_000_000 {
		return time.UnixMilli(value).UTC()
	}
	return time.Unix(value, 0).UTC()
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
	completionState := fmt.Sprintf("%t|%t", event.FinalKnown, event.FinalPresent)
	sum := sha256.Sum256([]byte(event.ErrorText + "|" + completionState + "|" + event.AbortReason + "|" + event.GoalStatus + "|" + event.GoalUpdatedAt.UTC().Format(time.RFC3339Nano)))
	return fmt.Sprintf("%s|%s|%s|%s|%s", threadID, event.Kind, event.TurnID, event.Timestamp.UTC().Format(time.RFC3339Nano), hex.EncodeToString(sum[:6]))
}
