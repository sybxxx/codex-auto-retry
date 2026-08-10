package main

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

var statusPattern = regexp.MustCompile(`(?i)(?:status(?: code)?|http|unexpected status)[^0-9]{0,12}([1-5][0-9]{2})`)

func classifyFailure(errorText string, cfg Config) RetryDecision {
	text := strings.ToLower(strings.TrimSpace(errorText))
	if text == "" {
		return RetryDecision{Class: classNone, Reason: "empty error"}
	}

	if containsAny(text,
		"cancelled by user", "canceled by user", "user cancelled", "user canceled",
		"interrupted by user", "aborted by user", "turn aborted", "task was cancelled",
	) {
		return RetryDecision{Class: classNone, Reason: "user cancellation"}
	}

	// CC Switch can wrap a recoverable upstream outage in HTTP 400. The
	// structured error is more specific than the status code, so recognize it
	// before the generic permanent-400 rule below.
	if isCCSwitchUpstreamFailure(text) {
		return RetryDecision{Retry: true, Class: classTransient, Reason: "CC Switch upstream request failed"}
	}

	if containsAny(text,
		"context length", "maximum context", "prompt is too long", "input is too long",
		"invalid_request", "invalid request", "invalid payload", "malformed", "schema error",
		"model not found", "unknown model", "does not exist", "approval required",
		"permission denied", "policy violation", "content policy", "unsupported parameter",
	) {
		return RetryDecision{Class: classNone, Reason: "permanent request or policy error"}
	}

	status := extractStatus(text)
	switch {
	case status == 408 || status == 425:
		return RetryDecision{Retry: true, Class: classTransient, Reason: "temporary HTTP status"}
	case status == 429:
		return RetryDecision{Retry: true, Class: classRateLimit, Reason: "rate limited"}
	case status >= 500 && status <= 599:
		return RetryDecision{Retry: true, Class: classServer, Reason: "provider server error"}
	case status == 400 || status == 404 || status == 405 || status == 409 || status == 410 || status == 413 || status == 415 || status == 422:
		return RetryDecision{Class: classNone, Reason: "non-retryable HTTP status"}
	}

	if containsAny(text,
		"auth_unavailable", "auth unavailable", "no auth available", "no account is currently schedulable",
		"authentication service unavailable", "login service unavailable", "oauth service unavailable",
		"cooling down", "cooldown", "temporarily unavailable authentication",
	) {
		return RetryDecision{Retry: true, Class: classAuthTransient, Reason: "temporary authentication service failure"}
	}

	if containsAny(text,
		"rate limit", "too many requests", "quota temporarily", "capacity", "overloaded", "try again later",
	) {
		return RetryDecision{Retry: true, Class: classRateLimit, Reason: "provider capacity or rate limit"}
	}

	if containsAny(text,
		"connection reset", "connection refused", "connection closed", "connection error",
		"failed to connect", "could not connect", "network error", "network is unreachable",
		"dns error", "name resolution", "timed out", "timeout", "deadline exceeded",
		"error sending request", "request failed", "transport error", "broken pipe",
		"stream disconnected", "stream closed", "stream ended", "incomplete stream",
		"websocket", "tls handshake", "service unavailable", "bad gateway", "gateway timeout",
	) {
		return RetryDecision{Retry: true, Class: classTransient, Reason: "network or stream failure"}
	}

	if status == 401 || status == 403 || containsAny(text,
		"unauthorized", "not authenticated", "authentication required", "login required",
		"please log in", "token expired", "token refresh", "invalid api key", "incorrect api key",
		"credential", "access denied", "forbidden",
	) {
		return RetryDecision{Retry: true, Class: classAuthLimited, MaxAttempts: cfg.AuthMaxAttempts, Reason: "authentication may require user action"}
	}

	return RetryDecision{Retry: true, Class: classUnknown, MaxAttempts: cfg.UnknownMaxAttempts, Reason: "unknown provider failure"}
}

func classifyCompletionFailure(event RelevantEvent, cfg Config) RetryDecision {
	if strings.TrimSpace(event.ErrorText) != "" {
		return classifyFailure(event.ErrorText, cfg)
	}
	if event.FinalKnown && !event.FinalPresent {
		return RetryDecision{
			Retry:  true,
			Class:  classEmptyResponse,
			Reason: "provider completed without a final response",
		}
	}
	return RetryDecision{Class: classNone, Reason: "successful completion"}
}

func completionSucceeded(event RelevantEvent) bool {
	return strings.TrimSpace(event.ErrorText) == "" && (!event.FinalKnown || event.FinalPresent)
}

func containsAny(text string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(text, candidate) {
			return true
		}
	}
	return false
}

func extractStatus(text string) int {
	match := statusPattern.FindStringSubmatch(text)
	if len(match) != 2 {
		return 0
	}
	status, _ := strconv.Atoi(match[1])
	return status
}

// isCCSwitchUpstreamFailure accepts only the known CC Switch wrapper for a
// recoverable upstream request failure. Other 400 responses must continue to
// follow the permanent-error policy.
func isCCSwitchUpstreamFailure(text string) bool {
	var value any
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &value); err != nil {
		return false
	}
	return matchesCCSwitchUpstreamFailure(value, 0)
}

func matchesCCSwitchUpstreamFailure(value any, depth int) bool {
	if depth > 4 {
		return false
	}
	switch typed := value.(type) {
	case map[string]any:
		code, codeOK := jsonStringField(typed, "code")
		status, statusOK := jsonStatusField(typed, "upstream_status")
		message := strings.ToLower(jsonStringFieldValue(typed, "message"))
		cause := strings.ToLower(jsonStringFieldValue(typed, "cause"))
		if codeOK && strings.EqualFold(strings.TrimSpace(code), "cc_switch_upstream_error") &&
			statusOK && status == 400 &&
			(strings.Contains(message, "upstream request failed") || strings.Contains(cause, "upstream request failed")) {
			return true
		}
		for _, child := range typed {
			if matchesCCSwitchUpstreamFailure(child, depth+1) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if matchesCCSwitchUpstreamFailure(child, depth+1) {
				return true
			}
		}
	case string:
		var nested any
		if json.Unmarshal([]byte(strings.TrimSpace(typed)), &nested) == nil {
			return matchesCCSwitchUpstreamFailure(nested, depth+1)
		}
	}
	return false
}

func jsonStringField(value map[string]any, field string) (string, bool) {
	for key, raw := range value {
		if !strings.EqualFold(key, field) {
			continue
		}
		text, ok := raw.(string)
		return text, ok
	}
	return "", false
}

func jsonStringFieldValue(value map[string]any, field string) string {
	text, _ := jsonStringField(value, field)
	return text
}

func jsonStatusField(value map[string]any, field string) (int, bool) {
	for key, raw := range value {
		if !strings.EqualFold(key, field) {
			continue
		}
		switch typed := raw.(type) {
		case float64:
			return int(typed), typed == float64(int(typed))
		case json.Number:
			status, err := strconv.Atoi(string(typed))
			return status, err == nil
		case string:
			statusText := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(typed), "http"))
			statusText = strings.TrimSpace(statusText)
			status, err := strconv.Atoi(statusText)
			return status, err == nil
		default:
			return 0, false
		}
	}
	return 0, false
}
