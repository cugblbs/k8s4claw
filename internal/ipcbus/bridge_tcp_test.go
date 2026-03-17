package ipcbus

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"
)

func TestTCPBridge_SendReceive(t *testing.T) {
	// Echo TCP server: reads a message and writes it back.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

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

	bridge := NewTCPBridge(ln.Addr().String())

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

	payload, _ := json.Marshal(map[string]string{"hello": "tcp"})
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

func TestTCPBridge_ConnectFailure(t *testing.T) {
	bridge := NewTCPBridge("127.0.0.1:1") // port 1 should be unreachable

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := bridge.Connect(ctx); err == nil {
		bridge.Close()
		t.Fatal("expected error connecting to unreachable address")
	}
}

func TestTCPBridge_CloseIdempotent(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer ln.Close()

	go func() {
		conn, _ := ln.Accept()
		if conn != nil {
			conn.Close()
		}
	}()

	bridge := NewTCPBridge(ln.Addr().String())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := bridge.Connect(ctx); err != nil {
		t.Fatalf("failed to connect: %v", err)
	}

	// Close twice should not panic.
	if err := bridge.Close(); err != nil {
		t.Errorf("first close error: %v", err)
	}
	if err := bridge.Close(); err != nil {
		t.Errorf("second close error: %v", err)
	}
}
