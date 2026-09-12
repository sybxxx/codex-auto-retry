//go:build windows

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestOfficialIPCStartRequestIsSilentAndKeepsThreadSettings(t *testing.T) {
	settings := testResumeSettings()
	request := officialIPCStartRequest("019fa94e-0103-7183-b405-36bd307b6dbd", settings)
	if request["threadId"] != "019fa94e-0103-7183-b405-36bd307b6dbd" {
		t.Fatalf("thread id was not preserved: %+v", request)
	}
	input, ok := request["input"].([]any)
	if !ok || len(input) != 0 {
		t.Fatalf("IPC recovery must not add visible input: %#v", request["input"])
	}
	if request["cwd"] != settings.CWD || request["model"] != settings.Model || request["effort"] != settings.Effort {
		t.Fatalf("thread settings were not preserved: %+v", request)
	}
	mode, ok := request["collaborationMode"].(map[string]any)
	if !ok || mode["mode"] != "default" {
		t.Fatalf("collaboration mode was not preserved: %#v", request["collaborationMode"])
	}
	modeSettings, ok := mode["settings"].(map[string]any)
	if !ok || modeSettings["model"] != settings.Model || modeSettings["reasoning_effort"] != settings.Effort {
		t.Fatalf("collaboration mode settings were incomplete: %#v", mode["settings"])
	}
	sandbox, ok := request["sandboxPolicy"].(map[string]any)
	if !ok || sandbox["type"] != "dangerFullAccess" {
		t.Fatalf("sandbox policy was not mapped safely: %#v", request["sandboxPolicy"])
	}
	encoded, err := json.Marshal(map[string]any{
		"conversationId": "019fa94e-0103-7183-b405-36bd307b6dbd",
		"turnStart":      map[string]any{"request": request, "context": map[string]any{"responseItems": []any{}}},
	})
	if err != nil || strings.Contains(string(encoded), "继续") {
		t.Fatalf("silent IPC request unexpectedly included retry prompt or failed to encode: %v", err)
	}
}

func TestCodexIPCRequestIDsAreDistinct(t *testing.T) {
	first := newCodexIPCRequestID()
	second := newCodexIPCRequestID()
	if first == second || !strings.Contains(first, "-") || !strings.Contains(second, "-") {
		t.Fatalf("IPC request ids are not unique UUID-like values: %q %q", first, second)
	}
}

func TestOfficialIPCProbeAgainstLiveRouterWhenAvailable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ready, err := (&windowsOfficialDesktopIPC{}).Available(ctx)
	if err != nil || !ready {
		t.Skipf("Codex official IPC router is not running: %v", err)
	}
}
