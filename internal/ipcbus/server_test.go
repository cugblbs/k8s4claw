package ipcbus

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-logr/logr"
)

func TestServer_RegisterAndSend(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "test.sock")
	walDir := filepath.Join(tmpDir, "wal")

	wal, err := NewWAL(walDir)
	if err != nil {
		t.Fatalf("failed to create WAL: %v", err)
	}
	defer wal.Close()

	router := NewRouter(RouterConfig{
		WAL:    wal,
		Logger: logr.Discard(),
	})

	srv := NewServer(socketPath, router, logr.Discard())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start(ctx)
	}()

	// Poll until the socket appears and is connectable.
	var conn net.Conn
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.Dial("unix", socketPath)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("failed to connect to UDS server: %v", err)
	}
	defer conn.Close()

	// Send TypeRegister message.
	regMsg := NewMessage(TypeRegister, "test-channel", nil)
	if err := WriteMessage(conn, regMsg); err != nil {
		t.Fatalf("failed to write registration message: %v", err)
	}

	// Read ACK.
	ack, err := ReadMessage(conn)
	if err != nil {
		t.Fatalf("failed to read ACK: %v", err)
	}
	if ack.Type != TypeAck {
		t.Fatalf("expected ACK, got %s", ack.Type)
	}
	if ack.CorrelationID != regMsg.ID {
		t.Fatalf("ACK correlationID mismatch: got %s, want %s", ack.CorrelationID, regMsg.ID)
	}

	// Wait briefly for registration to propagate.
	time.Sleep(50 * time.Millisecond)

	// Verify connected count.
	if got := srv.ConnectedCount(); got != 1 {
		t.Fatalf("expected ConnectedCount=1, got %d", got)
	}

	// Send a data message.
	dataMsg := NewMessage(TypeMessage, "test-channel", nil)
	if err := WriteMessage(conn, dataMsg); err != nil {
		t.Fatalf("failed to write data message: %v", err)
	}

	// Wait briefly for message processing.
	time.Sleep(50 * time.Millisecond)

	// Close client connection so the server's read loop exits, then cancel.
	conn.Close()
	cancel()

	select {
	case srvErr := <-errCh:
		if srvErr != nil {
			t.Fatalf("server returned error: %v", srvErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down in time")
	}
}

func TestServer_SocketPermissions(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "perm.sock")

	router := NewRouter(RouterConfig{
		Logger: logr.Discard(),
	})

	srv := NewServer(socketPath, router, logr.Discard())

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		srv.Start(ctx)
	}()

	// Wait for socket to appear.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	info, err := os.Stat(socketPath)
	if err != nil {
		t.Fatalf("socket file not found: %v", err)
	}

	perm := info.Mode().Perm()
	if perm != 0o777 {
		t.Fatalf("expected socket permissions 0777, got %o", perm)
	}

	cancel()
}

func TestServer_RejectNonRegisterFirst(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "reject.sock")

	router := NewRouter(RouterConfig{
		Logger: logr.Discard(),
	})

	srv := NewServer(socketPath, router, logr.Discard())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		srv.Start(ctx)
	}()

	// Wait for socket.
	var conn net.Conn
	var connErr error
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, connErr = net.Dial("unix", socketPath)
		if connErr == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if conn == nil {
		t.Fatalf("failed to connect to UDS server: %v", connErr)
	}
	defer conn.Close()

	// Send a non-register message first.
	badMsg := NewMessage(TypeMessage, "bad-channel", nil)
	if err := WriteMessage(conn, badMsg); err != nil {
		t.Fatalf("failed to write message: %v", err)
	}

	// Server should close the connection — ReadMessage should fail.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, readErr := ReadMessage(conn)
	if readErr == nil {
		t.Fatal("expected error reading from rejected connection, got nil")
	}

	// Should have 0 connections.
	if got := srv.ConnectedCount(); got != 0 {
		t.Fatalf("expected ConnectedCount=0, got %d", got)
	}
}

func TestServer_HeartbeatACK(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "hb.sock")

	router := NewRouter(RouterConfig{
		Logger: logr.Discard(),
	})

	srv := NewServer(socketPath, router, logr.Discard())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		srv.Start(ctx)
	}()

	// Connect and register.
	var conn net.Conn
	var err error
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.Dial("unix", socketPath)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer conn.Close()

	regMsg := NewMessage(TypeRegister, "hb-channel", nil)
	if err := WriteMessage(conn, regMsg); err != nil {
		t.Fatalf("failed to write register: %v", err)
	}

	// Read registration ACK.
	if _, err := ReadMessage(conn); err != nil {
		t.Fatalf("failed to read register ACK: %v", err)
	}

	// Send heartbeat.
	hbMsg := NewMessage(TypeHeartbeat, "hb-channel", nil)
	if err := WriteMessage(conn, hbMsg); err != nil {
		t.Fatalf("failed to write heartbeat: %v", err)
	}

	// Read heartbeat ACK.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	ack, err := ReadMessage(conn)
	if err != nil {
		t.Fatalf("failed to read heartbeat ACK: %v", err)
	}
	if ack.Type != TypeAck {
		t.Fatalf("expected ACK for heartbeat, got %s", ack.Type)
	}
	if ack.CorrelationID != hbMsg.ID {
		t.Fatalf("heartbeat ACK correlationID mismatch: got %s, want %s", ack.CorrelationID, hbMsg.ID)
	}
}
