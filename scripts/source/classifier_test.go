package main

import (
	"strings"
	"testing"
)

func TestClassifyFailure(t *testing.T) {
	cfg := defaultConfig()
	tests := []struct {
		name      string
		errorText string
		retry     bool
		class     FailureClass
		max       int
	}{
		{"cockpit auth pool", "unexpected status 503 Service Unavailable: auth_unavailable: no auth available", true, classServer, 0},
		{"rate limit", "HTTP 429 Too Many Requests", true, classRateLimit, 0},
		{"network reset", "error sending request: connection reset by peer", true, classTransient, 0},
		{"stream stopped", "stream disconnected before response.completed", true, classTransient, 0},
		{"temporary auth", "authentication service unavailable, try again later", true, classAuthTransient, 0},
		{"expired login", "HTTP 401 Unauthorized: token expired", true, classAuthLimited, cfg.AuthMaxAttempts},
		{"bad request", "HTTP 400 invalid_request_error: invalid payload", false, classNone, 0},
		{"missing model", "HTTP 404 model not found", false, classNone, 0},
		{"context limit", "maximum context length exceeded", false, classNone, 0},
		{"user cancel", "task was cancelled by user", false, classNone, 0},
		{"unknown provider", "vendor channel temporarily broke in an undocumented way", true, classUnknown, cfg.UnknownMaxAttempts},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision := classifyFailure(test.errorText, cfg)
			if decision.Retry != test.retry || decision.Class != test.class || decision.MaxAttempts != test.max {
				t.Fatalf("classifyFailure() = %+v", decision)
			}
		})
	}
}

func TestExponentialRetryDelayCaps(t *testing.T) {
	cfg := defaultConfig()
	cfg.InitialDelaySeconds = 5
	cfg.MaxDelaySeconds = 60
	want := []int{5, 10, 20, 40, 60, 60}
	for index, seconds := range want {
		if got := int(retryDelay(index+1, cfg).Seconds()); got != seconds {
			t.Fatalf("attempt %d delay = %d, want %d", index+1, got, seconds)
		}
	}
}

func TestLinearRetryDelayCaps(t *testing.T) {
	cfg := defaultConfig()
	cfg.DelayStrategy = delayStrategyLinear
	cfg.InitialDelaySeconds = 2
	cfg.DelayIncrementSeconds = 2
	cfg.MaxDelaySeconds = 10
	want := []int{2, 4, 6, 8, 10, 10}
	for index, seconds := range want {
		if got := int(retryDelay(index+1, cfg).Seconds()); got != seconds {
			t.Fatalf("attempt %d linear delay = %d, want %d", index+1, got, seconds)
		}
	}
}

func TestFixedRetryDelayDoesNotIncrease(t *testing.T) {
	cfg := defaultConfig()
	cfg.DelayStrategy = delayStrategyFixed
	cfg.InitialDelaySeconds = 7
	cfg.MaxDelaySeconds = 3
	for attempt := 1; attempt <= 8; attempt++ {
		if got := int(retryDelay(attempt, cfg).Seconds()); got != 7 {
			t.Fatalf("attempt %d fixed delay = %d, want 7", attempt, got)
		}
	}
}

func TestClassifyCompletionFailure(t *testing.T) {
	cfg := defaultConfig()
	empty := RelevantEvent{FinalKnown: true}
	decision := classifyCompletionFailure(empty, cfg)
	if !decision.Retry || decision.Class != classEmptyResponse || !strings.Contains(decision.Reason, "without a final response") {
		t.Fatalf("empty successful completion was not retryable: %+v", decision)
	}
	if completionSucceeded(empty) {
		t.Fatal("empty successful completion was treated as recovered")
	}

	completed := RelevantEvent{FinalKnown: true, FinalPresent: true}
	if decision := classifyCompletionFailure(completed, cfg); decision.Retry || decision.Class != classNone {
		t.Fatalf("non-empty completion was treated as failed: %+v", decision)
	}
	if !completionSucceeded(completed) {
		t.Fatal("non-empty completion was not treated as recovered")
	}

	legacy := RelevantEvent{}
	if !completionSucceeded(legacy) {
		t.Fatal("legacy completion without final-message metadata was not preserved")
	}

	explicit := RelevantEvent{ErrorText: "HTTP 503", FinalKnown: true}
	if decision := classifyCompletionFailure(explicit, cfg); !decision.Retry || decision.Class != classServer {
		t.Fatalf("explicit error classification changed: %+v", decision)
	}
}
