package channel

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// mockIPCServer simulates the IPC Bus server for testing.
type mockIPCServer struct {
	socketPath string
	listener   net.Listener
	mu         sync.Mutex
	received   []*message
	sendOnRecv []*message // messages to send back after first data message
	conns      []net.Conn
}

func newMockIPCServer(t *testing.T) *mockIPCServer {
	t.Helper()
	dir := t.TempDir()
	return &mockIPCServer{
		socketPath: filepath.Join(dir, "ipc.sock"),
	}
}

func (s *mockIPCServer) start(t *testing.T) {
	t.Helper()
	ln, err := net.Listen("unix", s.socketPath)
	if err != nil {
		t.Fatalf("mock server listen: %v", err)
	}
	s.listener = ln

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns = append(s.conns, conn)
			s.mu.Unlock()
			go s.handleConn(conn)
		}
	}()
}

func (s *mockIPCServer) handleConn(conn net.Conn) {
	defer conn.Close()

	// Read registration.
	reg, err := readMessage(conn)
	if err != nil {
		return
	}
	if reg.Type != typeRegister {
		return
	}

	// Send ACK.
	ack := newAck(reg.ID)
	if err := writeMessage(conn, ack); err != nil {
		return
	}

	// Read loop.
	for {
		msg, err := readMessage(conn)
		if err != nil {
			return
		}

		if msg.Type == typeHeartbeat {
			hbAck := newAck(msg.ID)
			_ = writeMessage(conn, hbAck)
			continue
		}

		s.mu.Lock()
		s.received = append(s.received, msg)
		toSend := append([]*message(nil), s.sendOnRecv...)
		s.mu.Unlock()

		for _, m := range toSend {
			_ = writeMessage(conn, m)
		}
	}
}

func (s *mockIPCServer) close() {
	if s.listener != nil {
		s.listener.Close()
	}
	s.mu.Lock()
	for _, conn := range s.conns {
		conn.Close()
	}
	s.conns = nil
	s.mu.Unlock()
}

func (s *mockIPCServer) getReceived() []*message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*message(nil), s.received...)
}

func (s *mockIPCServer) queueOutbound(msgs ...*message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sendOnRecv = append(s.sendOnRecv, msgs...)
}

func TestClient_ConnectAndSend(t *testing.T) {
	srv := newMockIPCServer(t)
	srv.start(t)
	defer srv.close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := Connect(ctx,
		WithSocketPath(srv.socketPath),
		WithChannelName("test-chan"),
		WithHeartbeatInterval(time.Hour), // disable heartbeat for this test
	)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Close()

	payload := json.RawMessage(`{"text":"hello"}`)
	if err := c.Send(ctx, payload); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Give the server time to receive.
	time.Sleep(50 * time.Millisecond)

	msgs := srv.getReceived()
	if len(msgs) != 1 {
		t.Fatalf("server received %d messages, want 1", len(msgs))
	}
	if string(msgs[0].Payload) != `{"text":"hello"}` {
		t.Errorf("payload = %s, want %s", msgs[0].Payload, `{"text":"hello"}`)
	}
}

func TestClient_Receive(t *testing.T) {
	srv := newMockIPCServer(t)

	// Queue an outbound message to send after first data message.
	outMsg := newMessage(typeMessage, "test-chan", json.RawMessage(`{"reply":"world"}`))
	srv.queueOutbound(outMsg)

	srv.start(t)
	defer srv.close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := Connect(ctx,
		WithSocketPath(srv.socketPath),
		WithChannelName("test-chan"),
		WithHeartbeatInterval(time.Hour),
	)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Close()

	ch, err := c.Receive(ctx)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}

	// Trigger the server to send the outbound message.
	_ = c.Send(ctx, json.RawMessage(`{"ping":true}`))

	select {
	case msg := <-ch:
		if string(msg.Payload) != `{"reply":"world"}` {
			t.Errorf("received payload = %s, want %s", msg.Payload, `{"reply":"world"}`)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for inbound message")
	}
}

func TestClient_ConnectFailsWithoutServer(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "nonexistent.sock")

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, err := Connect(ctx,
		WithSocketPath(sock),
		WithChannelName("test"),
		WithReconnectInterval(50*time.Millisecond),
	)
	if err == nil {
		t.Fatal("expected error when server is not running")
	}
}

func TestClient_MissingChannelName(t *testing.T) {
	os.Setenv("CHANNEL_NAME", "")
	defer os.Unsetenv("CHANNEL_NAME")

	ctx := context.Background()
	_, err := Connect(ctx, WithChannelName(""))
	if err == nil {
		t.Fatal("expected error for missing channel name")
	}
}

func TestClient_BusDownBuffering(t *testing.T) {
	srv := newMockIPCServer(t)
	srv.start(t)
	defer srv.close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := Connect(ctx,
		WithSocketPath(srv.socketPath),
		WithChannelName("buf-chan"),
		WithBufferSize(16),
		WithHeartbeatInterval(time.Hour),
	)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Verify client is connected.
	if err := c.Send(ctx, json.RawMessage(`{"before":"disconnect"}`)); err != nil {
		t.Fatalf("Send before disconnect: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// Forcibly close the server to simulate bus down.
	srv.close()

	// Wait for the client's readLoop to detect the disconnect (EOF).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		connected := c.connected
		c.mu.Unlock()
		if !connected {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Send while disconnected — should buffer.
	if err := c.Send(ctx, json.RawMessage(`{"during":"disconnect"}`)); err != nil {
		t.Fatalf("Send during disconnect should buffer, got: %v", err)
	}

	if c.BufferedCount() != 1 {
		t.Fatalf("BufferedCount = %d, want 1", c.BufferedCount())
	}

	c.Close()
}
