package ipcbus

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/coder/websocket"
)

// WebSocketBridge implements [RuntimeBridge] over WebSocket, used by
// the OpenClaw runtime.
type WebSocketBridge struct {
	url  string
	conn *websocket.Conn

	mu     sync.Mutex
	closed chan struct{}
}

// NewWebSocketBridge creates a bridge targeting the given WebSocket URL.
func NewWebSocketBridge(url string) *WebSocketBridge {
	return &WebSocketBridge{
		url:    url,
		closed: make(chan struct{}),
	}
}

func (b *WebSocketBridge) Connect(ctx context.Context) error {
	conn, _, err := websocket.Dial(ctx, b.url, nil)
	if err != nil {
		return fmt.Errorf("failed to dial websocket %s: %w", b.url, err)
	}
	b.conn = conn
	return nil
}

func (b *WebSocketBridge) Send(ctx context.Context, msg *Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if err := b.conn.Write(ctx, websocket.MessageText, data); err != nil {
		return fmt.Errorf("failed to write websocket message: %w", err)
	}
	return nil
}

func (b *WebSocketBridge) Receive(ctx context.Context) (<-chan *Message, error) {
	ch := make(chan *Message, 64)

	go func() {
		defer close(ch)
		for {
			_, data, err := b.conn.Read(ctx)
			if err != nil {
				// Context cancelled or connection closed — stop reading.
				select {
				case <-b.closed:
				default:
				}
				return
			}

			var msg Message
			if err := json.Unmarshal(data, &msg); err != nil {
				continue // skip malformed messages
			}

			select {
			case ch <- &msg:
			case <-ctx.Done():
				return
			case <-b.closed:
				return
			}
		}
	}()

	return ch, nil
}

func (b *WebSocketBridge) Close() error {
	select {
	case <-b.closed:
		return nil // already closed
	default:
		close(b.closed)
	}

	if b.conn != nil {
		return b.conn.Close(websocket.StatusNormalClosure, "shutdown")
	}
	return nil
}
