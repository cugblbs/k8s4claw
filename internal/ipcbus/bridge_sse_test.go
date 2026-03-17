package ipcbus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// sseEchoServer creates an httptest server that echoes POST /messages back
// through the GET /events SSE stream. The ready channel is closed once a
// listener has connected to the SSE stream.
func sseEchoServer(t *testing.T) (*httptest.Server, <-chan struct{}) {
	t.Helper()

	var mu sync.Mutex
	var listeners []chan []byte
	ready := make(chan struct{}, 1)

	mux := http.NewServeMux()

	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		flusher.Flush()

		ch := make(chan []byte, 64)
		mu.Lock()
		listeners = append(listeners, ch)
		mu.Unlock()

		// Signal that a listener is ready.
		select {
		case ready <- struct{}{}:
		default:
		}

		for {
			select {
			case data := <-ch:
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			case <-r.Context().Done():
				return
			}
		}
	})

	mux.HandleFunc("/messages", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}

		mu.Lock()
		for _, ch := range listeners {
			select {
			case ch <- body:
			default:
			}
		}
		mu.Unlock()

		w.WriteHeader(http.StatusAccepted)
	})

	return httptest.NewServer(mux), ready
}

func TestSSEBridge_SendReceive(t *testing.T) {
	srv, ready := sseEchoServer(t)
	defer srv.Close()

	bridge := NewSSEBridge(srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := bridge.Connect(ctx); err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer bridge.Close()

	ch, err := bridge.Receive(ctx)
	if err != nil {
		t.Fatalf("failed to start receive: %v", err)
	}

	// Wait for SSE stream to be established.
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("timed out waiting for SSE listener to connect")
	}

	payload, _ := json.Marshal(map[string]string{"hello": "sse"})
	msg := NewMessage(TypeMessage, "test-chan", payload)

	if err := bridge.Send(ctx, msg); err != nil {
		t.Fatalf("failed to send: %v", err)
	}

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

func TestSSEBridge_ConnectFailure(t *testing.T) {
	bridge := NewSSEBridge("http://127.0.0.1:1")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := bridge.Connect(ctx); err == nil {
		bridge.Close()
		t.Fatal("expected error connecting to unreachable server")
	}
}

func TestSSEBridge_CloseIdempotent(t *testing.T) {
	srv, _ := sseEchoServer(t)
	defer srv.Close()

	bridge := NewSSEBridge(srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := bridge.Connect(ctx); err != nil {
		t.Fatalf("failed to connect: %v", err)
	}

	if err := bridge.Close(); err != nil {
		t.Errorf("first close error: %v", err)
	}
	if err := bridge.Close(); err != nil {
		t.Errorf("second close error: %v", err)
	}
}
