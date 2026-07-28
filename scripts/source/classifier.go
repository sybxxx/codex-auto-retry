package main

import (
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
