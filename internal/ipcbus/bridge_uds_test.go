package ipcbus

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestUDSBridge_SendReceive(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "rt.sock")

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	// Echo server.
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		for {
			msg, err := ReadMessage(conn)
			if err != nil {
				return
			}
			if err := WriteMessage(conn, msg); err != nil {
				return
			}
		}
	}()

	bridge := NewUDSBridge(sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := bridge.Connect(ctx); err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer func() { _ = bridge.Close() }()

	ch, err := bridge.Receive(ctx)
	if err != nil {
		t.Fatalf("failed to start receive: %v", err)
	}

	payload, _ := json.Marshal(map[string]string{"hello": "uds"})
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
	case <-ctx.Done():
		t.Fatal("timed out waiting for echoed message")
	}
}

func TestUDSBridge_ConnectFailure(t *testing.T) {
	bridge := NewUDSBridge("/nonexistent/path/rt.sock")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := bridge.Connect(ctx); err == nil {
		_ = bridge.Close()
		t.Fatal("expected error connecting to nonexistent socket")
	}
}
