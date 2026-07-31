package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestAppServerCallStopsPromptlyWhenRecoveryIsCancelled(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	requestSeen := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		connection, err := upgrader.Upgrade(response, request, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		for {
			var message map[string]json.RawMessage
			if err := connection.ReadJSON(&message); err != nil {
				return
			}
			var method string
			_ = json.Unmarshal(message["method"], &method)
			if method == "initialize" {
				_ = connection.WriteJSON(map[string]any{
					"jsonrpc": "2.0", "id": json.RawMessage(message["id"]), "result": map[string]any{},
				})
				continue
			}
			if method == "hang" {
				close(requestSeen)
				for {
					if _, _, err := connection.ReadMessage(); err != nil {
						return
					}
				}
			}
		}
	}))
	defer server.Close()
	client, err := dialAppServerRPC(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Call(ctx, "hang", map[string]any{}, nil) }()
	select {
	case <-requestSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("hanging app-server request was not received")
	}
	cancelledAt := time.Now()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled call returned %v", err)
		}
		if time.Since(cancelledAt) > time.Second {
			t.Fatal("cancelled app-server call did not stop promptly")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled app-server call remained blocked")
	}
}
