package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type syntheticTurnContext struct {
	Timestamp string         `json:"timestamp"`
	Type      string         `json:"type"`
	Payload   map[string]any `json:"payload"`
}

func TestLoadLatestResumeSettingsReadsOnlyLatestTurnContext(t *testing.T) {
	oldContext := marshalSyntheticTurnContext(t, syntheticSettingsPayload("old-model", "low", "old-secret"))
	latestPayload := syntheticSettingsPayload("gpt-5.6-sol", "max", "private-developer-instructions")
	latestPayload["collaboration_mode"] = map[string]any{
		"mode": "default",
		"settings": map[string]any{
			"developer_instructions": strings.Repeat("private-developer-instructions", 4000),
		},
	}
	latestContext := marshalSyntheticTurnContext(t, latestPayload)
	if len(latestContext) <= resumeSettingsSearchBlockBytes {
		t.Fatal("synthetic context does not cross the reverse-reader block boundary")
	}
	decoy := `{"timestamp":"2026-07-27T00:00:00Z","type":"response_item","payload":{"text":"\"type\":\"turn_context\""}}`
	settingsEvent := marshalSyntheticThreadSettings(t, map[string]any{
		"model":                     "provider-model",
		"model_provider_id":         "relay-provider",
		"service_tier":              "priority",
		"approval_policy":           "never",
		"approvals_reviewer":        "user",
		"permission_profile":        map[string]any{"type": "disabled"},
		"active_permission_profile": map[string]any{"id": ":danger-full-access"},
		"cwd":                       `C:\workspace`,
		"reasoning_effort":          "xhigh",
		"reasoning_summary":         "detailed",
		"personality":               "friendly",
		"developer_instructions":    "private-thread-setting",
	})
	path := filepath.Join(t.TempDir(), "rollout-test.jsonl")
	content := strings.Join([]string{string(oldContext), decoy, string(latestContext), string(settingsEvent)}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	settings, err := loadLatestResumeSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Model != "provider-model" || settings.ModelProvider != "relay-provider" ||
		settings.ServiceTier != "priority" || settings.Effort != "xhigh" {
		t.Fatalf("latest settings were not selected: %+v", settings)
	}
	if settings.Personality != "friendly" || settings.Summary != "detailed" {
		t.Fatalf("applied thread settings were not selected: %+v", settings)
	}
	if settings.CWD != `C:\workspace` || settings.Permissions != ":danger-full-access" {
		t.Fatalf("execution settings were not preserved: %+v", settings)
	}
	if len(settings.RuntimeWorkspaceRoots) != 2 || settings.RuntimeWorkspaceRoots[1] != `C:\visualization` {
		t.Fatalf("workspace roots were not preserved: %v", settings.RuntimeWorkspaceRoots)
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{
		"private-developer-instructions", "private-thread-setting", "old-secret", "collaboration_mode",
	} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("private non-setting data escaped the parser: %q", private)
		}
	}
}

func TestLoadLatestResumeSettingsDoesNotFallBackPastInvalidLatestContext(t *testing.T) {
	valid := marshalSyntheticTurnContext(t, syntheticSettingsPayload("gpt-5.6-sol", "high", "ignored"))
	invalidPayload := syntheticSettingsPayload("gpt-5.6-sol", "high", "ignored")
	invalidPayload["cwd"] = "relative-path"
	invalid := marshalSyntheticTurnContext(t, invalidPayload)
	path := filepath.Join(t.TempDir(), "rollout-test.jsonl")
	if err := os.WriteFile(path, append(append(valid, '\n'), invalid...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadLatestResumeSettings(path); !errors.Is(err, errResumeSettingsUnavailable) {
		t.Fatalf("invalid latest settings did not fail closed: %v", err)
	}
}

func TestResumePermissionProfileMapping(t *testing.T) {
	tests := []struct {
		sandbox string
		profile string
		want    string
	}{
		{sandbox: "danger-full-access", profile: "disabled", want: ":danger-full-access"},
		{sandbox: "read-only", profile: "managed", want: ":read-only"},
		{sandbox: "workspace-write", profile: "managed", want: ":workspace"},
	}
	for _, test := range tests {
		got, err := resumePermissionProfile(test.sandbox, test.profile)
		if err != nil || got != test.want {
			t.Fatalf("mapping %s/%s: got %q, err=%v", test.sandbox, test.profile, got, err)
		}
	}
	if _, err := resumePermissionProfile("danger-full-access", "managed"); err == nil {
		t.Fatal("inconsistent permission profile was accepted")
	}
}

func TestValidApprovalPolicySupportsGranularPolicy(t *testing.T) {
	valid := json.RawMessage(`{"granular":{"mcp_elicitations":true,"rules":true,"sandbox_approval":false,"request_permissions":true}}`)
	if !validApprovalPolicy(valid) {
		t.Fatal("valid granular approval policy was rejected")
	}
	invalid := json.RawMessage(`{"granular":{"mcp_elicitations":true,"rules":true,"sandbox_approval":false,"unknown":true}}`)
	if validApprovalPolicy(invalid) {
		t.Fatal("unknown granular approval field was accepted")
	}
}

func TestLiveResumeSettingsProbe(t *testing.T) {
	path := os.Getenv("CODEX_AUTO_RETRY_LIVE_ROLLOUT")
	if path == "" {
		t.Skip("set CODEX_AUTO_RETRY_LIVE_ROLLOUT for a privacy-bounded live parser probe")
	}
	settings, err := loadLatestResumeSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateResumeSettings(settings); err != nil {
		t.Fatal(err)
	}
}

func syntheticSettingsPayload(model, effort, private string) map[string]any {
	return map[string]any{
		"turn_id":              "019f9e64-d177-7500-b24c-7011e6465d23",
		"cwd":                  `C:\workspace`,
		"workspace_roots":      []string{`C:\workspace`, `C:\visualization`},
		"approval_policy":      "never",
		"approvals_reviewer":   "user",
		"sandbox_policy":       map[string]any{"type": "danger-full-access"},
		"permission_profile":   map[string]any{"type": "disabled"},
		"model":                model,
		"personality":          "pragmatic",
		"effort":               effort,
		"summary":              "auto",
		"developer_secret":     private,
		"conversation_message": "must-not-be-retained",
	}
}

func marshalSyntheticTurnContext(t *testing.T, payload map[string]any) []byte {
	t.Helper()
	line, err := json.Marshal(syntheticTurnContext{
		Timestamp: "2026-07-27T00:00:00Z",
		Type:      "turn_context",
		Payload:   payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	return line
}

func marshalSyntheticThreadSettings(t *testing.T, settings map[string]any) []byte {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"timestamp": "2026-07-27T00:00:01Z",
		"type":      "event_msg",
		"payload": map[string]any{
			"type":            "thread_settings_applied",
			"thread_settings": settings,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return line
}
