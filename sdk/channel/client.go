package channel

import (
	"context"
	"encoding/json"
	"errors"
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
	cfg          *clientConfig
	conn         net.Conn
	mu           sync.Mutex // guards conn, connected, reconnecting
	connected    bool
	reconnecting bool
	buf          *buffer
	throttled    bool
	throttleMu   sync.Mutex
	throttleCh   chan struct{} // closed when resume received
	inbound      chan *InboundMessage
	done         chan struct{}
	closeOnce    sync.Once
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

		interval = min(interval*2, maxReconnectInterval)
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
		c.tryReconnect()
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
			if errors.Is(err, io.EOF) {
				c.cfg.logger.Info("IPC Bus disconnected")
			} else {
				c.cfg.logger.Error(err, "read error from IPC Bus")
			}
			c.markDisconnected()
			c.tryReconnect()
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
				c.tryReconnect()
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

func (c *Client) tryReconnect() {
	c.mu.Lock()
	if c.reconnecting {
		c.mu.Unlock()
		return
	}
	c.reconnecting = true
	c.mu.Unlock()
	go c.reconnectLoop()
}

func (c *Client) reconnectLoop() {
	defer func() {
		c.mu.Lock()
		c.reconnecting = false
		c.mu.Unlock()
	}()

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
		interval = min(interval*2, maxReconnectInterval)
	}
}
