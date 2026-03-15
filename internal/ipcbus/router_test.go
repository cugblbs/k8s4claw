package ipcbus

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
)

// mockBridge implements RuntimeBridge for testing.
type mockBridge struct {
	mu       sync.Mutex
	sent     []*Message
	recvCh   chan *Message
	sendErr  error
	recvErr  error
	closed   bool
}

func newMockBridge() *mockBridge {
	return &mockBridge{
		recvCh: make(chan *Message, 64),
	}
}

func (b *mockBridge) Connect(_ context.Context) error { return nil }

func (b *mockBridge) Send(_ context.Context, msg *Message) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sendErr != nil {
		return b.sendErr
	}
	b.sent = append(b.sent, msg)
	return nil
}

func (b *mockBridge) Receive(_ context.Context) (<-chan *Message, error) {
	if b.recvErr != nil {
		return nil, b.recvErr
	}
	return b.recvCh, nil
}

func (b *mockBridge) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	return nil
}

func (b *mockBridge) getSent() []*Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]*Message(nil), b.sent...)
}

func (b *mockBridge) setSendErr(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sendErr = err
}

// mockSidecarConn creates a SidecarConn backed by a pipe for testing.
func newTestSidecarConn(channel string) (*SidecarConn, *collectWriter) {
	cw := &collectWriter{}
	return &SidecarConn{
		Channel: channel,
		Mode:    "bidirectional",
		conn:    cw,
	}, cw
}

// collectWriter is a net.Conn stub that collects written bytes.
type collectWriter struct {
	mu   sync.Mutex
	data [][]byte
}

func (w *collectWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.data = append(w.data, append([]byte(nil), p...))
	return len(p), nil
}

func (w *collectWriter) Read([]byte) (int, error)        { return 0, fmt.Errorf("not implemented") }
func (w *collectWriter) Close() error                     { return nil }
func (w *collectWriter) LocalAddr() net.Addr              { return nil }
func (w *collectWriter) RemoteAddr() net.Addr             { return nil }
func (w *collectWriter) SetDeadline(time.Time) error      { return nil }
func (w *collectWriter) SetReadDeadline(time.Time) error  { return nil }
func (w *collectWriter) SetWriteDeadline(time.Time) error { return nil }

// Implement net.Conn interface properly.
func (w *collectWriter) getWrites() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.data)
}

func TestRouter_StartOutboundLoop(t *testing.T) {
	bridge := newMockBridge()
	router := NewRouter(RouterConfig{
		Bridge: bridge,
		Logger: logr.Discard(),
	})

	// Register a sidecar.
	sc, cw := newTestSidecarConn("test-chan")
	router.Register(sc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go router.StartOutboundLoop(ctx)

	// Send a message through the bridge channel.
	outMsg := NewMessage(TypeMessage, "test-chan", json.RawMessage(`{"hello":"world"}`))
	bridge.recvCh <- outMsg

	// Wait a bit for the message to be routed.
	time.Sleep(50 * time.Millisecond)

	// The sidecar should have received a write.
	if cw.getWrites() == 0 {
		t.Error("expected sidecar to receive outbound message")
	}

	// Cancel context to stop the loop.
	cancel()
	time.Sleep(50 * time.Millisecond)
}

func TestRouter_StartOutboundLoop_NilBridge(t *testing.T) {
	router := NewRouter(RouterConfig{
		Logger: logr.Discard(),
	})

	// Should return immediately without panic.
	router.StartOutboundLoop(context.Background())
}

func TestRouter_StartOutboundLoop_ReceiveError(t *testing.T) {
	bridge := newMockBridge()
	bridge.recvErr = fmt.Errorf("receive error")

	router := NewRouter(RouterConfig{
		Bridge: bridge,
		Logger: logr.Discard(),
	})

	// Should return without panic due to error.
	router.StartOutboundLoop(context.Background())
}

func TestRouter_StartOutboundLoop_ChannelClosed(t *testing.T) {
	bridge := newMockBridge()
	router := NewRouter(RouterConfig{
		Bridge: bridge,
		Logger: logr.Discard(),
	})

	// Close the channel immediately.
	close(bridge.recvCh)

	// Should return when channel is closed.
	done := make(chan struct{})
	go func() {
		router.StartOutboundLoop(context.Background())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("StartOutboundLoop did not return after channel close")
	}
}

func TestRouter_ReplayWAL(t *testing.T) {
	dir := t.TempDir()
	wal, err := NewWAL(dir)
	if err != nil {
		t.Fatalf("NewWAL: %v", err)
	}
	defer wal.Close()

	bridge := newMockBridge()
	router := NewRouter(RouterConfig{
		Bridge: bridge,
		WAL:    wal,
		Logger: logr.Discard(),
	})

	// Append some pending entries.
	msg1 := NewMessage(TypeMessage, "ch1", json.RawMessage(`{"k":"v1"}`))
	msg2 := NewMessage(TypeMessage, "ch2", json.RawMessage(`{"k":"v2"}`))
	if err := wal.Append(msg1); err != nil {
		t.Fatalf("Append msg1: %v", err)
	}
	if err := wal.Append(msg2); err != nil {
		t.Fatalf("Append msg2: %v", err)
	}

	router.ReplayWAL(context.Background())

	sent := bridge.getSent()
	if len(sent) != 2 {
		t.Fatalf("expected 2 messages sent to bridge, got %d", len(sent))
	}

	// Both entries should be completed in the WAL.
	pending := wal.PendingEntries()
	if len(pending) != 0 {
		t.Errorf("expected 0 pending after replay, got %d", len(pending))
	}
}

func TestRouter_ReplayWAL_NilWAL(t *testing.T) {
	router := NewRouter(RouterConfig{
		Logger: logr.Discard(),
	})

	// Should not panic.
	router.ReplayWAL(context.Background())
}

func TestRouter_ReplayWAL_BridgeSendFails(t *testing.T) {
	dir := t.TempDir()
	wal, err := NewWAL(dir)
	if err != nil {
		t.Fatalf("NewWAL: %v", err)
	}
	defer wal.Close()

	bridge := newMockBridge()
	bridge.setSendErr(fmt.Errorf("bridge down"))

	router := NewRouter(RouterConfig{
		Bridge: bridge,
		WAL:    wal,
		Logger: logr.Discard(),
	})

	msg := NewMessage(TypeMessage, "ch1", json.RawMessage(`{"k":"v"}`))
	if err := wal.Append(msg); err != nil {
		t.Fatalf("Append: %v", err)
	}

	router.ReplayWAL(context.Background())

	// Entry should remain pending since send failed.
	pending := wal.PendingEntries()
	if len(pending) != 1 {
		t.Errorf("expected 1 pending after failed replay, got %d", len(pending))
	}
}

func TestRouter_SendShutdown(t *testing.T) {
	router := NewRouter(RouterConfig{
		Logger: logr.Discard(),
	})

	sc1, cw1 := newTestSidecarConn("chan-a")
	sc2, cw2 := newTestSidecarConn("chan-b")
	router.Register(sc1)
	router.Register(sc2)

	router.SendShutdown()

	// Both sidecars should have received a write (the shutdown message).
	if cw1.getWrites() == 0 {
		t.Error("sidecar chan-a did not receive shutdown message")
	}
	if cw2.getWrites() == 0 {
		t.Error("sidecar chan-b did not receive shutdown message")
	}
}

func TestRouter_SendShutdown_NoSidecars(t *testing.T) {
	router := NewRouter(RouterConfig{
		Logger: logr.Discard(),
	})

	// Should not panic with no sidecars.
	router.SendShutdown()
}

func TestRouter_scheduleRetry_PromoteToDLQ(t *testing.T) {
	dir := t.TempDir()
	wal, err := NewWAL(dir)
	if err != nil {
		t.Fatalf("NewWAL: %v", err)
	}
	defer wal.Close()

	dlqPath := filepath.Join(t.TempDir(), "dlq.db")
	dlq, err := NewDLQ(dlqPath, 100, 24*time.Hour)
	if err != nil {
		t.Fatalf("NewDLQ: %v", err)
	}
	defer dlq.Close()

	bridge := newMockBridge()
	bridge.setSendErr(fmt.Errorf("bridge always fails"))

	router := NewRouter(RouterConfig{
		Bridge: bridge,
		WAL:    wal,
		DLQ:    dlq,
		Logger: logr.Discard(),
	})

	// Create a message and append to WAL.
	msg := NewMessage(TypeMessage, "ch1", json.RawMessage(`{"key":"value"}`))
	if err := wal.Append(msg); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Simulate retries up to maxRetryAttempts (5).
	// WAL starts at attempts=1. scheduleRetry increments, so we need 4 calls
	// to reach 5 (1 + 4 increments = 5).
	ctx := context.Background()
	for i := 0; i < maxRetryAttempts-1; i++ {
		router.scheduleRetry(ctx, msg)
	}

	// After maxRetryAttempts, the message should be in the DLQ.
	entry, err := dlq.Get(msg.ID)
	if err != nil {
		t.Fatalf("DLQ Get: %v", err)
	}
	if entry == nil {
		t.Fatal("expected message to be in DLQ after max retries")
	}
	if entry.Attempts != maxRetryAttempts {
		t.Errorf("DLQ entry attempts = %d, want %d", entry.Attempts, maxRetryAttempts)
	}

	// The WAL entry should be marked as DLQ (removed from pending).
	pending := wal.PendingEntries()
	if len(pending) != 0 {
		t.Errorf("expected 0 pending after DLQ promotion, got %d", len(pending))
	}
}

func TestRouter_scheduleRetry_NilWAL(t *testing.T) {
	router := NewRouter(RouterConfig{
		Logger: logr.Discard(),
	})

	// Should not panic.
	msg := NewMessage(TypeMessage, "ch1", nil)
	router.scheduleRetry(context.Background(), msg)
}

func TestRouter_scheduleRetry_BelowMax(t *testing.T) {
	dir := t.TempDir()
	wal, err := NewWAL(dir)
	if err != nil {
		t.Fatalf("NewWAL: %v", err)
	}
	defer wal.Close()

	router := NewRouter(RouterConfig{
		WAL:    wal,
		Logger: logr.Discard(),
	})

	msg := NewMessage(TypeMessage, "ch1", json.RawMessage(`{"key":"value"}`))
	if err := wal.Append(msg); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// One retry, should still be pending.
	router.scheduleRetry(context.Background(), msg)

	pending := wal.PendingEntries()
	if len(pending) != 1 {
		t.Errorf("expected 1 pending after single retry, got %d", len(pending))
	}
	if pending[0].Attempts != 2 {
		t.Errorf("expected 2 attempts after single retry, got %d", pending[0].Attempts)
	}
}

func TestRouter_HandleOutbound_NoSidecar(t *testing.T) {
	router := NewRouter(RouterConfig{
		Logger: logr.Discard(),
	})

	msg := NewMessage(TypeMessage, "nonexistent-chan", nil)

	// Should not panic when no sidecar registered for channel.
	router.HandleOutbound(context.Background(), msg)
}

func TestRouter_HandleInbound_ControlMessage(t *testing.T) {
	router := NewRouter(RouterConfig{
		Logger: logr.Discard(),
	})

	sc, cw := newTestSidecarConn("test-chan")
	router.Register(sc)

	// Send a heartbeat control message.
	hb := NewMessage(TypeHeartbeat, "test-chan", nil)
	router.HandleInbound(context.Background(), sc, hb)

	// Should have sent an ACK back.
	if cw.getWrites() == 0 {
		t.Error("expected ACK response for heartbeat")
	}
}

func TestRouter_RegisterUnregister(t *testing.T) {
	router := NewRouter(RouterConfig{
		Logger: logr.Discard(),
	})

	sc, _ := newTestSidecarConn("test-chan")
	router.Register(sc)

	if router.ConnectedCount() != 1 {
		t.Errorf("expected 1 connected, got %d", router.ConnectedCount())
	}

	router.Unregister(sc)
	if router.ConnectedCount() != 0 {
		t.Errorf("expected 0 connected after unregister, got %d", router.ConnectedCount())
	}
}

func TestRouter_NewRouter_Defaults(t *testing.T) {
	router := NewRouter(RouterConfig{Logger: logr.Discard()})

	if router.bufferSize != defaultBufferSize {
		t.Errorf("bufferSize = %d, want %d", router.bufferSize, defaultBufferSize)
	}
	if router.highWatermark != defaultHighWatermark {
		t.Errorf("highWatermark = %f, want %f", router.highWatermark, defaultHighWatermark)
	}
	if router.lowWatermark != defaultLowWatermark {
		t.Errorf("lowWatermark = %f, want %f", router.lowWatermark, defaultLowWatermark)
	}
}

func TestRouter_HandleInbound_DataMessage(t *testing.T) {
	dir := t.TempDir()
	wal, err := NewWAL(dir)
	if err != nil {
		t.Fatalf("NewWAL: %v", err)
	}
	defer wal.Close()

	bridge := newMockBridge()
	router := NewRouter(RouterConfig{
		Bridge: bridge,
		WAL:    wal,
		Logger: logr.Discard(),
	})

	sc, _ := newTestSidecarConn("test-chan")
	router.Register(sc)

	msg := NewMessage(TypeMessage, "test-chan", json.RawMessage(`{"data":"hello"}`))
	router.HandleInbound(context.Background(), sc, msg)

	// Bridge should have received the message.
	sent := bridge.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 message sent to bridge, got %d", len(sent))
	}
	if sent[0].ID != msg.ID {
		t.Errorf("bridge got ID %s, want %s", sent[0].ID, msg.ID)
	}

	// WAL entry should be completed.
	pending := wal.PendingEntries()
	if len(pending) != 0 {
		t.Errorf("expected 0 pending after successful inbound, got %d", len(pending))
	}
}

func TestRouter_HandleInbound_BridgeFails(t *testing.T) {
	dir := t.TempDir()
	wal, err := NewWAL(dir)
	if err != nil {
		t.Fatalf("NewWAL: %v", err)
	}
	defer wal.Close()

	bridge := newMockBridge()
	bridge.setSendErr(fmt.Errorf("bridge down"))

	router := NewRouter(RouterConfig{
		Bridge: bridge,
		WAL:    wal,
		Logger: logr.Discard(),
	})

	sc, _ := newTestSidecarConn("test-chan")
	router.Register(sc)

	msg := NewMessage(TypeMessage, "test-chan", json.RawMessage(`{"data":"hello"}`))
	router.HandleInbound(context.Background(), sc, msg)

	// WAL entry should still be pending (retry was scheduled).
	pending := wal.PendingEntries()
	if len(pending) != 1 {
		t.Errorf("expected 1 pending after bridge failure, got %d", len(pending))
	}
}

func TestRouter_HandleInbound_NilBridge(t *testing.T) {
	dir := t.TempDir()
	wal, err := NewWAL(dir)
	if err != nil {
		t.Fatalf("NewWAL: %v", err)
	}
	defer wal.Close()

	router := NewRouter(RouterConfig{
		WAL:    wal,
		Logger: logr.Discard(),
	})

	sc, _ := newTestSidecarConn("test-chan")
	router.Register(sc)

	msg := NewMessage(TypeMessage, "test-chan", json.RawMessage(`{"data":"hello"}`))
	router.HandleInbound(context.Background(), sc, msg)

	// With nil bridge, code skips Send and falls through to Complete.
	// WAL entry should be completed.
	pending := wal.PendingEntries()
	if len(pending) != 0 {
		t.Errorf("expected 0 pending with nil bridge (falls through to complete), got %d", len(pending))
	}
}

func TestRouter_NewRouter_CustomValues(t *testing.T) {
	router := NewRouter(RouterConfig{
		Logger:        logr.Discard(),
		BufferSize:    512,
		HighWatermark: 0.9,
		LowWatermark:  0.2,
	})

	if router.bufferSize != 512 {
		t.Errorf("bufferSize = %d, want 512", router.bufferSize)
	}
	if router.highWatermark != 0.9 {
		t.Errorf("highWatermark = %f, want 0.9", router.highWatermark)
	}
	if router.lowWatermark != 0.2 {
		t.Errorf("lowWatermark = %f, want 0.2", router.lowWatermark)
	}
}
