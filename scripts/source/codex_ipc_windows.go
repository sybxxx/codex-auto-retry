//go:build windows

package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

const codexIPCPath = `\\.\pipe\codex-ipc`

var (
	errCodexIPCUnavailable = errors.New("Codex official IPC is unavailable")
)

type codexIPCResponse struct {
	Type            string          `json:"type"`
	RequestID       string          `json:"requestId"`
	ResultType      string          `json:"resultType"`
	Method          string          `json:"method"`
	Error           string          `json:"error"`
	HandledByClient string          `json:"handledByClientId"`
	Result          json.RawMessage `json:"result"`
}

type codexIPCClient struct {
	file   *os.File
	mu     sync.Mutex
	client string
}

func newPlatformOfficialDesktopIPC() officialDesktopIPC {
	return &windowsOfficialDesktopIPC{}
}

type windowsOfficialDesktopIPC struct{}

func (windowsOfficialDesktopIPC) Available(ctx context.Context) (bool, error) {
	client, err := dialCodexIPC(ctx)
	if err != nil {
		return false, nil
	}
	defer client.close()
	return true, nil
}

func (windowsOfficialDesktopIPC) StartTurn(ctx context.Context, threadID string, settings ResumeSettings) error {
	threadID = strings.ToLower(strings.TrimSpace(threadID))
	if !threadIDPattern.MatchString(threadID + ".jsonl") {
		return errors.New("invalid thread id")
	}
	client, err := dialCodexIPC(ctx)
	if err != nil {
		return err
	}
	defer client.close()

	owner, err := client.request(ctx, "thread-owner-discovery", 1, map[string]any{
		"hostId": "local", "conversationId": threadID,
	})
	if err != nil {
		return err
	}
	if owner.ResultType != "success" || owner.HandledByClient == "" {
		return errCodexIPCOwnerMissing
	}

	_, err = client.requestTo(ctx, "thread-follower-start-turn", 2, map[string]any{
		"conversationId": threadID,
		"turnStart": map[string]any{
			"request": officialIPCStartRequest(threadID, settings),
			"context": map[string]any{
				"inheritThreadSettings": true,
				// An empty input and empty responseItems are intentional. They
				// continue the exact existing turn without adding a user-visible
				// message or replaying the failed prompt.
				"responseItems": []any{},
			},
		},
	}, owner.HandledByClient)
	return err
}

func (windowsOfficialDesktopIPC) NotifySubagentRecovery(ctx context.Context, parentID, childID, eventID string, settings ResumeSettings) error {
	if !recoveryEventIDPattern.MatchString(eventID) ||
		!threadIDPattern.MatchString(strings.ToLower(strings.TrimSpace(parentID))+".jsonl") ||
		!threadIDPattern.MatchString(strings.ToLower(strings.TrimSpace(childID))+".jsonl") {
		return errors.New("invalid subagent recovery identity")
	}
	parentID = strings.ToLower(strings.TrimSpace(parentID))
	childID = strings.ToLower(strings.TrimSpace(childID))
	client, err := dialCodexIPC(ctx)
	if err != nil {
		return err
	}
	defer client.close()
	owner, err := client.request(ctx, "thread-owner-discovery", 1, map[string]any{
		"hostId": "local", "conversationId": parentID,
	})
	if err != nil || owner.ResultType != "success" || owner.HandledByClient == "" {
		if err != nil {
			return err
		}
		return errCodexIPCOwnerMissing
	}
	var ownerCapabilities struct {
		SupportsUntrustedAppInput bool `json:"supportsUntrustedAppInput"`
	}
	if len(owner.Result) == 0 || json.Unmarshal(owner.Result, &ownerCapabilities) != nil || !ownerCapabilities.SupportsUntrustedAppInput {
		return errors.New("Codex parent owner does not accept recovery events")
	}
	noticePayload, _ := json.Marshal(map[string]any{
		"parent_thread_id": parentID, "child_thread_id": childID,
		"recovery_event_id": eventID, "action": "resume_existing_child",
		"spawn_replacement": false,
		"instruction":       "The watchdog is resuming the exact existing child. Do not resume or spawn any child for this recovery event.",
	})
	notice := recoveryNoticePrefix + string(noticePayload)
	itemID := "msg_codex_auto_retry_" + strings.TrimPrefix(eventID, "car-")
	_, err = client.requestTo(ctx, "thread-follower-start-turn", 2, map[string]any{
		"conversationId": parentID,
		"turnStart": map[string]any{
			"request": officialIPCStartRequest(parentID, settings),
			"context": map[string]any{
				"inheritThreadSettings": true,
				"responseItems": []any{map[string]any{
					"type": "message", "id": itemID, "role": "developer",
					"content": []any{map[string]any{"type": "input_text", "text": notice}},
				}},
			},
		},
	}, owner.HandledByClient)
	return err
}

func officialIPCStartRequest(threadID string, settings ResumeSettings) map[string]any {
	request := map[string]any{
		"threadId":              threadID,
		"clientUserMessageId":   newCodexIPCRequestID(),
		"input":                 []any{},
		"cwd":                   settings.CWD,
		"approvalPolicy":        settings.ApprovalPolicy,
		"approvalsReviewer":     settings.ApprovalsReviewer,
		"sandboxPolicy":         officialIPCSandboxPolicy(settings),
		"permissions":           settings.Permissions,
		"runtimeWorkspaceRoots": settings.RuntimeWorkspaceRoots,
		"model":                 settings.Model,
		"serviceTier":           settings.ServiceTier,
		"effort":                settings.Effort,
		"multiAgentMode":        "explicitRequestOnly",
		"summary":               settings.Summary,
		"personality":           settings.Personality,
		"collaborationMode": map[string]any{
			"mode": "default",
			"settings": map[string]any{
				"model":                  settings.Model,
				"reasoning_effort":       settings.Effort,
				"developer_instructions": nil,
			},
		},
		"outputSchema": nil,
	}
	return request
}

func officialIPCSandboxPolicy(settings ResumeSettings) map[string]any {
	switch settings.Permissions {
	case ":danger-full-access":
		return map[string]any{"type": "dangerFullAccess"}
	case ":read-only":
		return map[string]any{"type": "readOnly"}
	case ":workspace":
		return map[string]any{"type": "workspaceWrite", "writableRoots": settings.RuntimeWorkspaceRoots}
	default:
		return map[string]any{"type": "workspaceWrite", "writableRoots": settings.RuntimeWorkspaceRoots}
	}
}

func dialCodexIPC(ctx context.Context) (*codexIPCClient, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	open := make(chan *os.File, 1)
	errCh := make(chan error, 1)
	go func() {
		file, err := os.OpenFile(codexIPCPath, os.O_RDWR, 0)
		if err != nil {
			errCh <- err
			return
		}
		select {
		case open <- file:
		case <-ctx.Done():
			_ = file.Close()
		}
	}()
	select {
	case file := <-open:
		client := &codexIPCClient{file: file, client: "initializing-client"}
		initialize, err := client.request(ctx, "initialize", 0, map[string]any{"clientType": "codex-auto-retry"})
		if err != nil {
			client.close()
			return nil, err
		}
		if initialize.ResultType != "success" || len(initialize.Result) == 0 {
			client.close()
			return nil, errCodexIPCUnavailable
		}
		var result struct {
			ClientID string `json:"clientId"`
		}
		if err := json.Unmarshal(initialize.Result, &result); err != nil || result.ClientID == "" {
			client.close()
			return nil, errCodexIPCUnavailable
		}
		client.client = result.ClientID
		return client, nil
	case err := <-errCh:
		return nil, fmt.Errorf("open Codex IPC: %w", err)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *codexIPCClient) request(ctx context.Context, method string, version int, params any) (codexIPCResponse, error) {
	return c.requestTo(ctx, method, version, params, "")
}

func (c *codexIPCClient) requestTo(ctx context.Context, method string, version int, params any, target string) (codexIPCResponse, error) {
	if c == nil || c.file == nil {
		return codexIPCResponse{}, errCodexIPCUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	requestID := newCodexIPCRequestID()
	message := map[string]any{
		"type": "request", "requestId": requestID, "sourceClientId": c.client,
		"version": version, "method": method, "params": params,
	}
	if target != "" {
		message["targetClientId"] = target
	}
	if err := c.writeFrame(ctx, message); err != nil {
		return codexIPCResponse{}, err
	}
	for {
		var response codexIPCResponse
		if err := c.readFrame(ctx, &response); err != nil {
			return codexIPCResponse{}, err
		}
		if response.Type == "broadcast" {
			continue
		}
		if response.Type == "client-discovery-request" {
			_ = c.writeFrame(ctx, map[string]any{
				"type": "client-discovery-response", "requestId": response.RequestID,
				"response": map[string]any{"canHandle": false},
			})
			continue
		}
		if response.RequestID != requestID {
			continue
		}
		if response.ResultType == "error" {
			if response.Error == "no-client-found" {
				return response, errCodexIPCOwnerMissing
			}
			return response, &codexIPCRequestError{Method: method, Message: response.Error}
		}
		return response, nil
	}
}

type codexIPCRequestError struct {
	Method  string
	Message string
}

func (e *codexIPCRequestError) Error() string {
	return "Codex official IPC request failed"
}

func (e *codexIPCRequestError) Unwrap() error { return errAppServerRequest }

func (c *codexIPCClient) writeFrame(ctx context.Context, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	frame := make([]byte, 4+len(data))
	binary.LittleEndian.PutUint32(frame, uint32(len(data)))
	copy(frame[4:], data)
	return c.writeWithContext(ctx, frame)
}

func (c *codexIPCClient) readFrame(ctx context.Context, destination any) error {
	header := make([]byte, 4)
	if err := c.readWithContext(ctx, header); err != nil {
		return err
	}
	length := binary.LittleEndian.Uint32(header)
	if length == 0 || length > 256*1024*1024 {
		return errors.New("invalid Codex IPC frame length")
	}
	payload := make([]byte, length)
	if err := c.readWithContext(ctx, payload); err != nil {
		return err
	}
	return json.Unmarshal(payload, destination)
}

func (c *codexIPCClient) writeWithContext(ctx context.Context, data []byte) error {
	done := make(chan error, 1)
	go func() {
		_, err := c.file.Write(data)
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *codexIPCClient) readWithContext(ctx context.Context, data []byte) error {
	done := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(c.file, data)
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *codexIPCClient) close() {
	if c != nil && c.file != nil {
		_ = c.file.Close()
	}
}

func newCodexIPCRequestID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("car-ipc-%d", time.Now().UnixNano())
	}
	encoded := hex.EncodeToString(raw[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}
