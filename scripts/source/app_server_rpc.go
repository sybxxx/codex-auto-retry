package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

var errAppServerRequest = errors.New("Codex app-server request failed")

type appServerRequestError struct {
	Code    int
	Message string
}

func (e *appServerRequestError) Error() string {
	return errAppServerRequest.Error()
}

func (e *appServerRequestError) Unwrap() error {
	return errAppServerRequest
}

type appServerRPCClient struct {
	connection *websocket.Conn
	nextID     atomic.Int64
}

type appServerWireMessage struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func dialAppServerRPC(ctx context.Context, endpoint string) (*appServerRPCClient, error) {
	// The endpoint is always loopback. Never send local recovery traffic through
	// a user or corporate HTTP proxy, even when NO_PROXY is misconfigured.
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	connection, _, err := dialer.DialContext(ctx, endpoint, nil)
	if err != nil {
		return nil, err
	}
	client := &appServerRPCClient{connection: connection}
	client.nextID.Store(1)
	initialize := map[string]any{
		"clientInfo":   map[string]any{"name": "codex_auto_retry", "version": appVersion},
		"capabilities": map[string]any{"experimentalApi": true},
	}
	if err := client.Call(ctx, "initialize", initialize, nil); err != nil {
		_ = client.Close()
		return nil, err
	}
	if err := client.Notify(ctx, "initialized", map[string]any{}); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

func (c *appServerRPCClient) Call(ctx context.Context, method string, params any, destination any) error {
	if c == nil || c.connection == nil {
		return errors.New("app-server connection is unavailable")
	}
	id := c.nextID.Add(1)
	deadline := time.Now().Add(20 * time.Second)
	if value, ok := ctx.Deadline(); ok && value.Before(deadline) {
		deadline = value
	}
	stopCancellation := context.AfterFunc(ctx, func() {
		_ = c.connection.SetReadDeadline(time.Now())
		_ = c.connection.SetWriteDeadline(time.Now())
	})
	defer stopCancellation()
	_ = c.connection.SetWriteDeadline(deadline)
	request := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	if err := c.connection.WriteJSON(request); err != nil {
		return err
	}
	_ = c.connection.SetReadDeadline(deadline)
	for {
		_, data, err := c.connection.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		var message appServerWireMessage
		if json.Unmarshal(data, &message) != nil {
			continue
		}
		if message.Method != "" && len(message.ID) > 0 && string(message.ID) != "null" {
			c.rejectServerRequest(message.ID)
			continue
		}
		var responseID int64
		if len(message.ID) == 0 || json.Unmarshal(message.ID, &responseID) != nil || responseID != id {
			continue
		}
		if message.Error != nil {
			return &appServerRequestError{Code: message.Error.Code, Message: message.Error.Message}
		}
		if destination == nil || len(message.Result) == 0 || string(message.Result) == "null" {
			return nil
		}
		if err := json.Unmarshal(message.Result, destination); err != nil {
			return fmt.Errorf("decode app-server result: %w", err)
		}
		return nil
	}
}

func (c *appServerRPCClient) Notify(ctx context.Context, method string, params any) error {
	deadline := time.Now().Add(5 * time.Second)
	if value, ok := ctx.Deadline(); ok && value.Before(deadline) {
		deadline = value
	}
	stopCancellation := context.AfterFunc(ctx, func() {
		_ = c.connection.SetWriteDeadline(time.Now())
	})
	defer stopCancellation()
	_ = c.connection.SetWriteDeadline(deadline)
	err := c.connection.WriteJSON(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func (c *appServerRPCClient) rejectServerRequest(id json.RawMessage) {
	_ = c.connection.WriteJSON(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    -32601,
			"message": "Interactive request unavailable in background recovery",
		},
	})
}

func (c *appServerRPCClient) Close() error {
	if c == nil || c.connection == nil {
		return nil
	}
	return c.connection.Close()
}
