package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type cdpRequest struct {
	ID     int            `json:"id"`
	Method string         `json:"method"`
	Params map[string]any `json:"params"`
}

type server struct {
	port            int
	invocationsPath string
	mu              sync.Mutex
	upgrader        websocket.Upgrader
}

func main() {
	portFile := flag.String("port-file", "", "path that receives the selected port")
	invocations := flag.String("invocations", "", "path that receives evaluate requests")
	flag.Parse()
	if *portFile == "" || *invocations == "" {
		os.Exit(2)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(3)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	s := &server{
		port:            port,
		invocationsPath: *invocations,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(*http.Request) bool { return true },
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/json/list", s.targets)
	mux.HandleFunc("/devtools/page/codex", s.websocket)
	httpServer := &http.Server{Handler: mux}
	mux.HandleFunc("/shutdown", func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = httpServer.Shutdown(ctx)
		}()
	})
	portData, _ := json.Marshal(port)
	if err := os.WriteFile(*portFile, portData, 0o600); err != nil {
		os.Exit(4)
	}
	if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		os.Exit(5)
	}
}

func (s *server) targets(response http.ResponseWriter, request *http.Request) {
	_ = json.NewEncoder(response).Encode([]map[string]any{{
		"type":                 "page",
		"title":                "Codex",
		"url":                  "app://-/index.html",
		"webSocketDebuggerUrl": "ws://" + request.Host + "/devtools/page/codex",
	}})
}

func (s *server) websocket(response http.ResponseWriter, httpRequest *http.Request) {
	connection, err := s.upgrader.Upgrade(response, httpRequest, nil)
	if err != nil {
		return
	}
	defer connection.Close()
	for {
		var message cdpRequest
		if err := connection.ReadJSON(&message); err != nil {
			return
		}
		if message.Method == "Runtime.evaluate" {
			expression, _ := message.Params["expression"].(string)
			s.record(expression)
			_ = connection.WriteJSON(map[string]any{
				"id": message.ID,
				"result": map[string]any{
					"result": map[string]any{
						"type": "object",
						"value": map[string]any{
							"outcome": "dispatched",
							"action":  "conversation_continue",
							"reason":  "mock_background_dispatch",
						},
					},
				},
			})
			continue
		}
		_ = connection.WriteJSON(map[string]any{"id": message.ID, "result": map[string]any{}})
	}
}

func (s *server) record(expression string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := os.OpenFile(s.invocationsPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	data, _ := json.Marshal(map[string]string{"expression": expression})
	_, _ = file.Write(append(data, '\n'))
}
