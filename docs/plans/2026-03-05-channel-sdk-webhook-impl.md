# Channel SDK + Webhook Sidecar Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build a Go Channel SDK (`sdk/channel/`) for sidecar↔IPC Bus communication, and a built-in Webhook sidecar (`cmd/channel-webhook/`) that bridges HTTP webhooks with the IPC Bus.

**Architecture:** The SDK reimplements IPC Bus framing (4-byte length-prefix + JSON) to avoid importing `internal/` packages. It handles UDS connection, registration handshake, backpressure, bus-down buffering, heartbeat, and auto-reconnect. The Webhook sidecar imports the SDK and adds HTTP inbound/outbound logic.

**Tech Stack:** Go 1.25, standard library (`net`, `encoding/binary`, `encoding/json`, `net/http`, `crypto/hmac`), `github.com/go-logr/logr`, `github.com/google/uuid`

---

## Task 1: SDK Message Types and Framing

**Files:**
- Create: `sdk/channel/message.go`
- Create: `sdk/channel/message_test.go`

**Step 1: Write the failing test**

```go
package channel

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestWriteReadMessage(t *testing.T) {
	var buf bytes.Buffer
	msg := &message{
		ID:        "test-123",
		Type:      typeMessage,
		Channel:   "slack",
		Timestamp: time.Now().Truncate(time.Millisecond),
		Payload:   json.RawMessage(`{"text":"hello"}`),
	}

	if err := writeMessage(&buf, msg); err != nil {
		t.Fatalf("writeMessage: %v", err)
	}

	got, err := readMessage(&buf)
	if err != nil {
		t.Fatalf("readMessage: %v", err)
	}

	if got.ID != msg.ID {
		t.Errorf("ID = %q, want %q", got.ID, msg.ID)
	}
	if got.Type != typeMessage {
		t.Errorf("Type = %q, want %q", got.Type, typeMessage)
	}
	if got.Channel != "slack" {
		t.Errorf("Channel = %q, want %q", got.Channel, "slack")
	}
}

func TestWriteMessage_ExceedsMaxSize(t *testing.T) {
	var buf bytes.Buffer
	huge := make(json.RawMessage, maxMessageSize+1)
	msg := &message{ID: "big", Type: typeMessage, Payload: huge}

	err := writeMessage(&buf, msg)
	if err == nil {
		t.Fatal("expected error for oversized message")
	}
}

func TestMessageTypes(t *testing.T) {
	tests := []struct {
		mt      messageType
		control bool
	}{
		{typeMessage, false},
		{typeAck, true},
		{typeNack, true},
		{typeSlowDown, true},
		{typeResume, true},
		{typeShutdown, true},
		{typeRegister, true},
		{typeHeartbeat, true},
	}
	for _, tt := range tests {
		msg := &message{Type: tt.mt}
		if msg.isControl() != tt.control {
			t.Errorf("%s.isControl() = %v, want %v", tt.mt, msg.isControl(), tt.control)
		}
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test -race ./sdk/channel/ -run TestWriteRead -v`
Expected: FAIL — package doesn't exist yet

**Step 3: Write minimal implementation**

```go
package channel

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
)

const (
	maxMessageSize  = 16 * 1024 * 1024 // 16 MiB
	frameHeaderSize = 4
)

type messageType string

const (
	typeMessage   messageType = "message"
	typeAck       messageType = "ack"
	typeNack      messageType = "nack"
	typeSlowDown  messageType = "slow_down"
	typeResume    messageType = "resume"
	typeShutdown  messageType = "shutdown"
	typeRegister  messageType = "register"
	typeHeartbeat messageType = "heartbeat"
)

type message struct {
	ID            string          `json:"id"`
	Type          messageType     `json:"type"`
	Channel       string          `json:"channel,omitempty"`
	CorrelationID string          `json:"correlationId,omitempty"`
	ReplyTo       string          `json:"replyTo,omitempty"`
	Timestamp     time.Time       `json:"timestamp"`
	Payload       json.RawMessage `json:"payload,omitempty"`
}

func newMessage(mt messageType, channel string, payload json.RawMessage) *message {
	return &message{
		ID:        uuid.Must(uuid.NewV7()).String(),
		Type:      mt,
		Channel:   channel,
		Timestamp: time.Now(),
		Payload:   payload,
	}
}

func newAck(id string) *message {
	ref, _ := json.Marshal(map[string]string{"ref": id})
	return &message{
		ID:            uuid.Must(uuid.NewV7()).String(),
		Type:          typeAck,
		CorrelationID: id,
		Timestamp:     time.Now(),
		Payload:       json.RawMessage(ref),
	}
}

func (m *message) isControl() bool {
	switch m.Type {
	case typeAck, typeNack, typeSlowDown, typeResume, typeShutdown, typeRegister, typeHeartbeat:
		return true
	}
	return false
}

func writeMessage(w io.Writer, msg *message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}
	if len(data) > maxMessageSize {
		return fmt.Errorf("message size %d exceeds maximum %d", len(data), maxMessageSize)
	}
	frame := make([]byte, frameHeaderSize+len(data))
	binary.BigEndian.PutUint32(frame, uint32(len(data)))
	copy(frame[frameHeaderSize:], data)
	if _, err := w.Write(frame); err != nil {
		return fmt.Errorf("failed to write frame: %w", err)
	}
	return nil
}

func readMessage(r io.Reader) (*message, error) {
	header := make([]byte, frameHeaderSize)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(header)
	if length > maxMessageSize {
		return nil, fmt.Errorf("frame size %d exceeds maximum %d", length, maxMessageSize)
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, fmt.Errorf("failed to read frame body: %w", err)
	}
	var msg message
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal message: %w", err)
	}
	return &msg, nil
}
```

**Step 4: Run test to verify it passes**

Run: `go test -race ./sdk/channel/ -v`
Expected: PASS

**Step 5: Commit**

```bash
git add sdk/channel/message.go sdk/channel/message_test.go
git commit -m "feat(sdk/channel): add message types and length-prefix framing"
```

---

## Task 2: SDK Bus-Down Buffer

**Files:**
- Create: `sdk/channel/buffer.go`
- Create: `sdk/channel/buffer_test.go`

**Step 1: Write the failing test**

```go
package channel

import (
	"encoding/json"
	"testing"
)

func TestBuffer_PushPop(t *testing.T) {
	b := newBuffer(4)
	msg := newMessage(typeMessage, "test", json.RawMessage(`{}`))

	if !b.push(msg) {
		t.Fatal("push to empty buffer should succeed")
	}
	if b.len() != 1 {
		t.Fatalf("len = %d, want 1", b.len())
	}

	got := b.pop()
	if got == nil {
		t.Fatal("pop should return message")
	}
	if got.ID != msg.ID {
		t.Errorf("ID = %q, want %q", got.ID, msg.ID)
	}
	if b.len() != 0 {
		t.Fatalf("len = %d, want 0", b.len())
	}
}

func TestBuffer_Full(t *testing.T) {
	b := newBuffer(2)
	b.push(newMessage(typeMessage, "a", nil))
	b.push(newMessage(typeMessage, "b", nil))

	if b.push(newMessage(typeMessage, "c", nil)) {
		t.Fatal("push to full buffer should fail")
	}
}

func TestBuffer_PopEmpty(t *testing.T) {
	b := newBuffer(4)
	if got := b.pop(); got != nil {
		t.Fatalf("pop on empty buffer returned %v", got)
	}
}

func TestBuffer_FIFO(t *testing.T) {
	b := newBuffer(4)
	m1 := newMessage(typeMessage, "ch", json.RawMessage(`"first"`))
	m2 := newMessage(typeMessage, "ch", json.RawMessage(`"second"`))
	b.push(m1)
	b.push(m2)

	got1 := b.pop()
	got2 := b.pop()
	if got1.ID != m1.ID {
		t.Errorf("first pop ID = %q, want %q", got1.ID, m1.ID)
	}
	if got2.ID != m2.ID {
		t.Errorf("second pop ID = %q, want %q", got2.ID, m2.ID)
	}
}

func TestBuffer_DrainAll(t *testing.T) {
	b := newBuffer(4)
	b.push(newMessage(typeMessage, "ch", nil))
	b.push(newMessage(typeMessage, "ch", nil))
	b.push(newMessage(typeMessage, "ch", nil))

	msgs := b.drainAll()
	if len(msgs) != 3 {
		t.Fatalf("drainAll returned %d, want 3", len(msgs))
	}
	if b.len() != 0 {
		t.Fatalf("len after drainAll = %d, want 0", b.len())
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test -race ./sdk/channel/ -run TestBuffer -v`
Expected: FAIL — buffer type not defined

**Step 3: Write minimal implementation**

```go
package channel

import "sync"

// buffer is a thread-safe FIFO queue for bus-down message buffering.
type buffer struct {
	mu    sync.Mutex
	items []*message
	cap   int
}

func newBuffer(capacity int) *buffer {
	if capacity <= 0 {
		capacity = 256
	}
	return &buffer{
		items: make([]*message, 0, capacity),
		cap:   capacity,
	}
}

func (b *buffer) push(msg *message) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.items) >= b.cap {
		return false
	}
	b.items = append(b.items, msg)
	return true
}

func (b *buffer) pop() *message {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.items) == 0 {
		return nil
	}
	msg := b.items[0]
	b.items = b.items[1:]
	return msg
}

func (b *buffer) len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.items)
}

func (b *buffer) drainAll() []*message {
	b.mu.Lock()
	defer b.mu.Unlock()
	msgs := b.items
	b.items = make([]*message, 0, b.cap)
	return msgs
}
```

**Step 4: Run test to verify it passes**

Run: `go test -race ./sdk/channel/ -run TestBuffer -v`
Expected: PASS

**Step 5: Commit**

```bash
git add sdk/channel/buffer.go sdk/channel/buffer_test.go
git commit -m "feat(sdk/channel): add bus-down message buffer"
```

---

## Task 3: SDK Options

**Files:**
- Create: `sdk/channel/options.go`

**Step 1: Write implementation** (no separate test needed — options are tested via Client)

```go
package channel

import (
	"os"
	"time"

	"github.com/go-logr/logr"
)

const (
	defaultSocketPath        = "/var/run/claw/ipc.sock"
	defaultBufferSize        = 256
	defaultReconnectInterval = 2 * time.Second
	defaultHeartbeatInterval = 30 * time.Second
	maxReconnectInterval     = 60 * time.Second
)

type clientConfig struct {
	socketPath        string
	channelName       string
	channelMode       string
	bufferSize        int
	reconnectInterval time.Duration
	heartbeatInterval time.Duration
	logger            logr.Logger
}

func defaultConfig() *clientConfig {
	socketPath := os.Getenv("IPC_SOCKET_PATH")
	if socketPath == "" {
		socketPath = defaultSocketPath
	}
	return &clientConfig{
		socketPath:        socketPath,
		channelName:       os.Getenv("CHANNEL_NAME"),
		channelMode:       os.Getenv("CHANNEL_MODE"),
		bufferSize:        defaultBufferSize,
		reconnectInterval: defaultReconnectInterval,
		heartbeatInterval: defaultHeartbeatInterval,
		logger:            logr.Discard(),
	}
}

// Option configures a channel Client.
type Option func(*clientConfig)

// WithSocketPath sets the IPC Bus UDS path.
func WithSocketPath(path string) Option {
	return func(c *clientConfig) { c.socketPath = path }
}

// WithChannelName sets the channel name for registration.
func WithChannelName(name string) Option {
	return func(c *clientConfig) { c.channelName = name }
}

// WithChannelMode sets the channel mode (inbound/outbound/bidirectional).
func WithChannelMode(mode string) Option {
	return func(c *clientConfig) { c.channelMode = mode }
}

// WithBufferSize sets the bus-down buffer capacity.
func WithBufferSize(size int) Option {
	return func(c *clientConfig) { c.bufferSize = size }
}

// WithReconnectInterval sets the base reconnect interval.
func WithReconnectInterval(d time.Duration) Option {
	return func(c *clientConfig) { c.reconnectInterval = d }
}

// WithHeartbeatInterval sets the heartbeat period.
func WithHeartbeatInterval(d time.Duration) Option {
	return func(c *clientConfig) { c.heartbeatInterval = d }
}

// WithLogger sets the logger.
func WithLogger(l logr.Logger) Option {
	return func(c *clientConfig) { c.logger = l }
}
```

**Step 2: Commit**

```bash
git add sdk/channel/options.go
git commit -m "feat(sdk/channel): add functional options"
```

---

## Task 4: SDK Client — Connect, Send, Receive, Close

This is the core task. The Client manages a UDS connection with registration, send/receive loops, backpressure, bus-down buffering, heartbeat, and auto-reconnect.

**Files:**
- Create: `sdk/channel/client.go`
- Create: `sdk/channel/client_test.go`

**Step 1: Write the failing tests**

```go
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
	// Unset env to ensure no fallback.
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
	time.Sleep(100 * time.Millisecond)

	// Send while disconnected — should buffer.
	if err := c.Send(ctx, json.RawMessage(`{"during":"disconnect"}`)); err != nil {
		t.Fatalf("Send during disconnect should buffer, got: %v", err)
	}

	if c.BufferedCount() != 1 {
		t.Fatalf("BufferedCount = %d, want 1", c.BufferedCount())
	}

	c.Close()
}
```

**Step 2: Run test to verify it fails**

Run: `go test -race ./sdk/channel/ -run TestClient -v`
Expected: FAIL — Client type not defined

**Step 3: Write implementation**

```go
package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// InboundMessage is a message received from the IPC Bus (runtime → sidecar).
type InboundMessage struct {
	ID      string          `json:"id"`
	Channel string          `json:"channel"`
	Payload json.RawMessage `json:"payload"`
}

// Client connects to the IPC Bus and provides send/receive for channel sidecars.
type Client struct {
	cfg       *clientConfig
	conn      net.Conn
	mu        sync.Mutex // guards conn, connected
	connected bool
	buf       *buffer
	throttled bool     // set by slow_down, cleared by resume
	throttleMu sync.Mutex
	throttleCh chan struct{} // closed when resume received
	inbound   chan *InboundMessage
	done      chan struct{}
	closeOnce sync.Once
}

// Connect establishes a UDS connection to the IPC Bus and registers.
// It retries with exponential backoff until ctx is cancelled.
func Connect(ctx context.Context, opts ...Option) (*Client, error) {
	cfg := defaultConfig()
	for _, o := range opts {
		o(cfg)
	}

	if cfg.channelName == "" {
		return nil, fmt.Errorf("channel name is required (set CHANNEL_NAME or use WithChannelName)")
	}

	c := &Client{
		cfg:        cfg,
		buf:        newBuffer(cfg.bufferSize),
		throttleCh: make(chan struct{}),
		inbound:    make(chan *InboundMessage, 64),
		done:       make(chan struct{}),
	}

	if err := c.connectWithRetry(ctx); err != nil {
		return nil, err
	}

	go c.readLoop()
	go c.heartbeatLoop()

	return c, nil
}

func (c *Client) connectWithRetry(ctx context.Context) error {
	interval := c.cfg.reconnectInterval
	for {
		err := c.dial(ctx)
		if err == nil {
			return nil
		}

		c.cfg.logger.Info("connection failed, retrying", "err", err, "interval", interval)

		select {
		case <-ctx.Done():
			return fmt.Errorf("failed to connect to IPC Bus at %s: %w", c.cfg.socketPath, ctx.Err())
		case <-time.After(interval):
		}

		interval = interval * 2
		if interval > maxReconnectInterval {
			interval = maxReconnectInterval
		}
	}
}

func (c *Client) dial(ctx context.Context) error {
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", c.cfg.socketPath)
	if err != nil {
		return fmt.Errorf("failed to dial UDS: %w", err)
	}

	// Send registration.
	reg := newMessage(typeRegister, c.cfg.channelName, nil)
	if err := writeMessage(conn, reg); err != nil {
		conn.Close()
		return fmt.Errorf("failed to send registration: %w", err)
	}

	// Wait for ACK with timeout.
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	ack, err := readMessage(conn)
	if err != nil {
		conn.Close()
		return fmt.Errorf("failed to read registration ACK: %w", err)
	}
	conn.SetReadDeadline(time.Time{})

	if ack.Type != typeAck || ack.CorrelationID != reg.ID {
		conn.Close()
		return fmt.Errorf("unexpected registration response: type=%s correlationID=%s", ack.Type, ack.CorrelationID)
	}

	c.mu.Lock()
	c.conn = conn
	c.connected = true
	c.mu.Unlock()

	c.cfg.logger.Info("connected to IPC Bus", "channel", c.cfg.channelName)

	// Replay buffered messages.
	c.replayBuffer()

	return nil
}

func (c *Client) replayBuffer() {
	msgs := c.buf.drainAll()
	for _, msg := range msgs {
		c.mu.Lock()
		conn := c.conn
		c.mu.Unlock()
		if conn == nil {
			// Re-buffer if connection lost during replay.
			c.buf.push(msg)
			return
		}
		if err := writeMessage(conn, msg); err != nil {
			c.cfg.logger.Error(err, "failed to replay buffered message", "msgID", msg.ID)
			c.buf.push(msg)
			return
		}
	}
	if len(msgs) > 0 {
		c.cfg.logger.Info("replayed buffered messages", "count", len(msgs))
	}
}

// Send sends a message to the IPC Bus. If disconnected, the message is
// buffered locally. If backpressure is active, Send blocks until resumed
// or ctx is cancelled.
func (c *Client) Send(ctx context.Context, payload json.RawMessage) error {
	// Wait for backpressure to clear.
	c.throttleMu.Lock()
	throttled := c.throttled
	ch := c.throttleCh
	c.throttleMu.Unlock()

	if throttled {
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		case <-c.done:
			return fmt.Errorf("client closed")
		}
	}

	msg := newMessage(typeMessage, c.cfg.channelName, payload)

	c.mu.Lock()
	conn := c.conn
	connected := c.connected
	c.mu.Unlock()

	if !connected || conn == nil {
		if !c.buf.push(msg) {
			return fmt.Errorf("bus-down buffer full (%d messages)", c.cfg.bufferSize)
		}
		return nil
	}

	if err := writeMessage(conn, msg); err != nil {
		c.cfg.logger.Info("send failed, buffering message", "err", err)
		c.markDisconnected()
		c.buf.push(msg)
		go c.reconnectLoop()
		return nil
	}

	return nil
}

// Receive returns a channel that delivers inbound messages from the IPC Bus.
func (c *Client) Receive(_ context.Context) (<-chan *InboundMessage, error) {
	return c.inbound, nil
}

// BufferedCount returns the number of messages in the bus-down buffer.
func (c *Client) BufferedCount() int {
	return c.buf.len()
}

// Close gracefully shuts down the client.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		close(c.done)
		c.mu.Lock()
		if c.conn != nil {
			c.conn.Close()
		}
		c.mu.Unlock()
		close(c.inbound)
	})
	return nil
}

func (c *Client) readLoop() {
	for {
		select {
		case <-c.done:
			return
		default:
		}

		c.mu.Lock()
		conn := c.conn
		c.mu.Unlock()
		if conn == nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		msg, err := readMessage(conn)
		if err != nil {
			select {
			case <-c.done:
				return
			default:
			}
			if err == io.EOF {
				c.cfg.logger.Info("IPC Bus disconnected")
			} else {
				c.cfg.logger.Error(err, "read error from IPC Bus")
			}
			c.markDisconnected()
			go c.reconnectLoop()
			return
		}

		c.handleMessage(msg)
	}
}

func (c *Client) handleMessage(msg *message) {
	switch msg.Type {
	case typeSlowDown:
		c.throttleMu.Lock()
		c.throttled = true
		c.throttleCh = make(chan struct{})
		c.throttleMu.Unlock()
		c.cfg.logger.Info("backpressure: slow_down received")

	case typeResume:
		c.throttleMu.Lock()
		c.throttled = false
		close(c.throttleCh)
		c.throttleMu.Unlock()
		c.cfg.logger.Info("backpressure: resume received")

	case typeShutdown:
		c.cfg.logger.Info("shutdown signal received from IPC Bus")
		c.Close()

	case typeAck, typeNack, typeHeartbeat:
		// Control messages — no action needed in read loop.

	case typeMessage:
		inMsg := &InboundMessage{
			ID:      msg.ID,
			Channel: msg.Channel,
			Payload: msg.Payload,
		}
		select {
		case c.inbound <- inMsg:
		case <-c.done:
		default:
			c.cfg.logger.Info("inbound channel full, dropping message", "msgID", msg.ID)
		}
	}
}

func (c *Client) heartbeatLoop() {
	ticker := time.NewTicker(c.cfg.heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			c.mu.Lock()
			conn := c.conn
			connected := c.connected
			c.mu.Unlock()

			if !connected || conn == nil {
				continue
			}

			hb := newMessage(typeHeartbeat, c.cfg.channelName, nil)
			if err := writeMessage(conn, hb); err != nil {
				c.cfg.logger.Info("heartbeat send failed", "err", err)
				c.markDisconnected()
				go c.reconnectLoop()
			}
		}
	}
}

func (c *Client) markDisconnected() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connected = false
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
}

func (c *Client) reconnectLoop() {
	interval := c.cfg.reconnectInterval
	for {
		select {
		case <-c.done:
			return
		case <-time.After(interval):
		}

		c.cfg.logger.Info("attempting reconnection", "interval", interval)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := c.dial(ctx)
		cancel()

		if err == nil {
			c.cfg.logger.Info("reconnected to IPC Bus")
			go c.readLoop()
			return
		}

		c.cfg.logger.Info("reconnection failed", "err", err)
		interval = interval * 2
		if interval > maxReconnectInterval {
			interval = maxReconnectInterval
		}
	}
}
```

**Step 4: Run all tests**

Run: `go test -race ./sdk/channel/ -v`
Expected: PASS

**Step 5: Commit**

```bash
git add sdk/channel/client.go sdk/channel/client_test.go
git commit -m "feat(sdk/channel): add Client with connect, send, receive, backpressure, and bus-down buffering"
```

---

## Task 5: Webhook Sidecar — Config and Handler

**Files:**
- Create: `cmd/channel-webhook/main.go`
- Create: `cmd/channel-webhook/handler.go`
- Create: `cmd/channel-webhook/handler_test.go`

**Step 1: Write the failing tests**

```go
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	channel "github.com/Prismer-AI/k8s4claw/sdk/channel"
)

// mockSender captures payloads sent via the channel SDK.
type mockSender struct {
	mu   sync.Mutex
	sent []json.RawMessage
}

func (m *mockSender) Send(_ context.Context, payload json.RawMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, payload)
	return nil
}

func (m *mockSender) getSent() []json.RawMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]json.RawMessage(nil), m.sent...)
}

func TestInboundHandler_Success(t *testing.T) {
	sender := &mockSender{}
	h := newInboundHandler(sender, "", "/webhook")

	body := `{"event":"test"}`
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want %d", w.Code, http.StatusAccepted)
	}

	sent := sender.getSent()
	if len(sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(sent))
	}
	if string(sent[0]) != body {
		t.Errorf("payload = %s, want %s", sent[0], body)
	}
}

func TestInboundHandler_HMACVerification(t *testing.T) {
	secret := "test-secret"
	sender := &mockSender{}
	h := newInboundHandler(sender, secret, "/webhook")

	body := `{"event":"signed"}`
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Signature-256", sig)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want %d", w.Code, http.StatusAccepted)
	}
}

func TestInboundHandler_HMACRejection(t *testing.T) {
	secret := "test-secret"
	sender := &mockSender{}
	h := newInboundHandler(sender, secret, "/webhook")

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"bad":"sig"}`))
	req.Header.Set("X-Signature-256", "sha256=invalid")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestInboundHandler_WrongMethod(t *testing.T) {
	sender := &mockSender{}
	h := newInboundHandler(sender, "", "/webhook")

	req := httptest.NewRequest(http.MethodGet, "/webhook", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHealthHandler_Connected(t *testing.T) {
	h := newHealthHandler(func() bool { return true })

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestHealthHandler_Disconnected(t *testing.T) {
	h := newHealthHandler(func() bool { return false })

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}

func TestOutboundPoster(t *testing.T) {
	var received []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Error("missing Authorization header")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	cfg := &webhookConfig{
		TargetURL: ts.URL,
		Headers:   map[string]string{"Authorization": "Bearer token"},
	}
	poster := newOutboundPoster(cfg)

	err := poster.post(context.Background(), json.RawMessage(`{"msg":"hello"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	if string(received) != `{"msg":"hello"}` {
		t.Errorf("received = %s, want %s", received, `{"msg":"hello"}`)
	}
}

func TestParseConfig(t *testing.T) {
	raw := `{"listenPort":9090,"path":"/hook","targetURL":"https://example.com","secret":"s","headers":{"X-Key":"val"},"retryAttempts":5}`

	cfg, err := parseConfig(raw)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.ListenPort != 9090 {
		t.Errorf("ListenPort = %d, want 9090", cfg.ListenPort)
	}
	if cfg.Path != "/hook" {
		t.Errorf("Path = %q, want /hook", cfg.Path)
	}
	if cfg.TargetURL != "https://example.com" {
		t.Errorf("TargetURL = %q", cfg.TargetURL)
	}
	if cfg.Secret != "s" {
		t.Errorf("Secret = %q", cfg.Secret)
	}
	if cfg.Headers["X-Key"] != "val" {
		t.Errorf("Headers[X-Key] = %q", cfg.Headers["X-Key"])
	}
	if cfg.RetryAttempts != 5 {
		t.Errorf("RetryAttempts = %d, want 5", cfg.RetryAttempts)
	}
}

func TestParseConfig_Defaults(t *testing.T) {
	cfg, err := parseConfig("")
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.ListenPort != 8080 {
		t.Errorf("ListenPort = %d, want 8080", cfg.ListenPort)
	}
	if cfg.Path != "/webhook" {
		t.Errorf("Path = %q, want /webhook", cfg.Path)
	}
	if cfg.RetryAttempts != 3 {
		t.Errorf("RetryAttempts = %d, want 3", cfg.RetryAttempts)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test -race ./cmd/channel-webhook/ -v`
Expected: FAIL — types not defined

**Step 3: Write implementation**

`cmd/channel-webhook/handler.go`:

```go
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-logr/logr"
)

type webhookConfig struct {
	ListenPort    int               `json:"listenPort"`
	Path          string            `json:"path"`
	TargetURL     string            `json:"targetURL"`
	Secret        string            `json:"secret"`
	Headers       map[string]string `json:"headers"`
	RetryAttempts int               `json:"retryAttempts"`
}

func parseConfig(raw string) (*webhookConfig, error) {
	cfg := &webhookConfig{
		ListenPort:    8080,
		Path:          "/webhook",
		RetryAttempts: 3,
	}
	if raw == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(raw), cfg); err != nil {
		return nil, fmt.Errorf("failed to parse CHANNEL_CONFIG: %w", err)
	}
	if cfg.ListenPort == 0 {
		cfg.ListenPort = 8080
	}
	if cfg.Path == "" {
		cfg.Path = "/webhook"
	}
	if cfg.RetryAttempts == 0 {
		cfg.RetryAttempts = 3
	}
	return cfg, nil
}

// sender abstracts the channel SDK Send method for testing.
type sender interface {
	Send(ctx context.Context, payload json.RawMessage) error
}

// inboundHandler handles incoming HTTP webhook requests.
type inboundHandler struct {
	sender sender
	secret string
	path   string
}

func newInboundHandler(s sender, secret, path string) *inboundHandler {
	return &inboundHandler{sender: s, secret: secret, path: path}
}

func (h *inboundHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	if h.secret != "" {
		sig := r.Header.Get("X-Signature-256")
		if !verifyHMAC(body, sig, h.secret) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
	}

	if err := h.sender.Send(r.Context(), json.RawMessage(body)); err != nil {
		http.Error(w, "failed to forward message", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

func verifyHMAC(body []byte, signature, secret string) bool {
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	sigBytes, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), sigBytes)
}

// healthHandler returns 200 if connected, 503 otherwise.
type healthHandler struct {
	isConnected func() bool
}

func newHealthHandler(isConnected func() bool) *healthHandler {
	return &healthHandler{isConnected: isConnected}
}

func (h *healthHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	if h.isConnected() {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("disconnected"))
	}
}

// outboundPoster posts messages to a target URL.
type outboundPoster struct {
	cfg    *webhookConfig
	client *http.Client
}

func newOutboundPoster(cfg *webhookConfig) *outboundPoster {
	return &outboundPoster{
		cfg:    cfg,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (p *outboundPoster) post(ctx context.Context, payload json.RawMessage) error {
	var lastErr error
	for attempt := 0; attempt < p.cfg.RetryAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.TargetURL, bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range p.cfg.Headers {
			req.Header.Set(k, v)
		}

		resp, err := p.client.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		}
		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		lastErr = fmt.Errorf("target returned status %d", resp.StatusCode)
		time.Sleep(time.Duration(attempt+1) * time.Second)
	}
	return fmt.Errorf("outbound post failed after %d attempts: %w", p.cfg.RetryAttempts, lastErr)
}

// startOutboundLoop reads from the channel SDK and posts to the target URL.
func startOutboundLoop(ctx context.Context, ch <-chan *inboundMessage, poster *outboundPoster, logger logr.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if err := poster.post(ctx, msg.Payload); err != nil {
				logger.Error(err, "outbound post failed", "msgID", msg.ID)
			}
		}
	}
}

// inboundMessage mirrors channel.InboundMessage to avoid import cycle in test.
type inboundMessage struct {
	ID      string          `json:"id"`
	Channel string          `json:"channel"`
	Payload json.RawMessage `json:"payload"`
}
```

`cmd/channel-webhook/main.go`:

```go
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"go.uber.org/zap"

	channel "github.com/Prismer-AI/k8s4claw/sdk/channel"
)

func main() {
	zapLog, _ := zap.NewProduction()
	logger := zapr.NewLogger(zapLog)

	configJSON := os.Getenv("CHANNEL_CONFIG")
	cfg, err := parseConfig(configJSON)
	if err != nil {
		logger.Error(err, "failed to parse config")
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	client, err := channel.Connect(ctx, channel.WithLogger(logger))
	if err != nil {
		logger.Error(err, "failed to connect to IPC Bus")
		os.Exit(1)
	}
	defer client.Close()

	mux := http.NewServeMux()

	// Inbound: HTTP → IPC Bus.
	mode := os.Getenv("CHANNEL_MODE")
	if mode == "inbound" || mode == "bidirectional" {
		mux.Handle(cfg.Path, newInboundHandler(client, cfg.Secret, cfg.Path))
	}

	// Health check.
	mux.Handle("/healthz", newHealthHandler(func() bool {
		return client.BufferedCount() == 0 // rough proxy for connectivity
	}))

	// Outbound: IPC Bus → HTTP.
	if (mode == "outbound" || mode == "bidirectional") && cfg.TargetURL != "" {
		poster := newOutboundPoster(cfg)
		inCh, err := client.Receive(ctx)
		if err != nil {
			logger.Error(err, "failed to start receiving")
			os.Exit(1)
		}
		go runOutboundLoop(ctx, inCh, poster, logger)
	}

	addr := fmt.Sprintf(":%d", cfg.ListenPort)
	srv := &http.Server{Addr: addr, Handler: mux}

	go func() {
		<-ctx.Done()
		srv.Close()
	}()

	logger.Info("webhook sidecar starting", "addr", addr, "mode", mode)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		logger.Error(err, "HTTP server error")
		os.Exit(1)
	}
}

func runOutboundLoop(ctx context.Context, ch <-chan *channel.InboundMessage, poster *outboundPoster, logger logr.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if err := poster.post(ctx, msg.Payload); err != nil {
				logger.Error(err, "outbound post failed", "msgID", msg.ID)
			}
		}
	}
}
```

**Step 4: Run all tests**

Run: `go test -race ./sdk/channel/ ./cmd/channel-webhook/ -v`
Expected: PASS

**Step 5: Commit**

```bash
git add cmd/channel-webhook/ sdk/channel/
git commit -m "feat: add webhook channel sidecar with inbound/outbound HTTP bridging"
```

---

## Task 6: Build Verification and Cleanup

**Step 1: Build everything**

Run: `go build ./...`
Expected: PASS (clean build)

**Step 2: Run full test suite**

Run: `go test -race -cover ./sdk/channel/ ./cmd/channel-webhook/`
Expected: PASS with ≥80% coverage on both packages

**Step 3: Run go vet**

Run: `go vet ./sdk/channel/ ./cmd/channel-webhook/`
Expected: No issues

**Step 4: Final commit if any cleanup was needed**

```bash
git add -A
git commit -m "chore: cleanup and verify channel SDK + webhook sidecar"
```

---

## Task Summary

| Task | Description | Files |
|------|-------------|-------|
| 1 | SDK message types + framing | `sdk/channel/message.go`, `sdk/channel/message_test.go` |
| 2 | SDK bus-down buffer | `sdk/channel/buffer.go`, `sdk/channel/buffer_test.go` |
| 3 | SDK options | `sdk/channel/options.go` |
| 4 | SDK Client (connect/send/receive/close) | `sdk/channel/client.go`, `sdk/channel/client_test.go` |
| 5 | Webhook sidecar (handler + main) | `cmd/channel-webhook/main.go`, `cmd/channel-webhook/handler.go`, `cmd/channel-webhook/handler_test.go` |
| 6 | Build verification + cleanup | — |
