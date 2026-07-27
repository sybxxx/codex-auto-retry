package main

import "testing"

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

func TestRetryDelayCaps(t *testing.T) {
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
