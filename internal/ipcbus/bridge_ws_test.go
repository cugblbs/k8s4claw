package ipcbus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestWebSocketBridge_SendReceive(t *testing.T) {
	// Echo WebSocket server: reads a message and writes it back.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Logf("accept error: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")

		for {
			typ, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			if err := conn.Write(r.Context(), typ, data); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	// Convert http URL to ws URL.
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	bridge := NewWebSocketBridge(wsURL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Connect.
	if err := bridge.Connect(ctx); err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer bridge.Close()

	// Start receiving.
	ch, err := bridge.Receive(ctx)
	if err != nil {
		t.Fatalf("failed to start receive: %v", err)
	}

	// Send a message.
	payload, _ := json.Marshal(map[string]string{"hello": "world"})
	msg := NewMessage(TypeMessage, "test-chan", payload)

	if err := bridge.Send(ctx, msg); err != nil {
		t.Fatalf("failed to send: %v", err)
	}

	// Read the echoed message back.
	select {
	case got := <-ch:
		if got.ID != msg.ID {
			t.Errorf("message ID mismatch: got %s, want %s", got.ID, msg.ID)
		}
		if got.Channel != "test-chan" {
			t.Errorf("channel mismatch: got %s, want test-chan", got.Channel)
		}
		if got.Type != TypeMessage {
			t.Errorf("type mismatch: got %s, want %s", got.Type, TypeMessage)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for echoed message")
	}
}

func TestNewBridge_Factory(t *testing.T) {
	tests := []struct {
		rt      RuntimeType
		wantErr bool
	}{
		{RuntimeOpenClaw, false},
		{RuntimeNanoClaw, false},
		{RuntimeZeroClaw, false},
		{RuntimePicoClaw, false},
		{RuntimeType("unknown"), true},
	}

	for _, tt := range tests {
		t.Run(string(tt.rt), func(t *testing.T) {
			b, err := NewBridge(tt.rt, BridgeConfig{GatewayPort: 9090, SocketPath: "/tmp/rt.sock"})
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if b == nil {
				t.Fatal("expected non-nil bridge")
			}
		})
	}
}
