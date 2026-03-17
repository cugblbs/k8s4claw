package ipcbus

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"
)

// streamBridge provides Send, Receive, and Close over any net.Conn using
// length-prefix framing. Both UDSBridge and TCPBridge embed this.
type streamBridge struct {
	conn   net.Conn
	mu     sync.Mutex
	closed chan struct{}
}

func newStreamBridge() streamBridge {
	return streamBridge{closed: make(chan struct{})}
}

func (b *streamBridge) Send(ctx context.Context, msg *Message) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.conn == nil {
		return fmt.Errorf("not connected")
	}

	// Respect context deadline for the write.
	if deadline, ok := ctx.Deadline(); ok {
		b.conn.SetWriteDeadline(deadline)
		defer b.conn.SetWriteDeadline(time.Time{})
	}

	if err := WriteMessage(b.conn, msg); err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}
	return nil
}

func (b *streamBridge) Receive(ctx context.Context) (<-chan *Message, error) {
	ch := make(chan *Message, 64)

	go func() {
		defer close(ch)

		// Close conn when context is cancelled so ReadMessage unblocks.
		go func() {
			select {
			case <-ctx.Done():
				b.conn.Close()
			case <-b.closed:
			}
		}()

		for {
			msg, err := ReadMessage(b.conn)
			if err != nil {
				select {
				case <-b.closed:
				default:
				}
				return
			}

			select {
			case ch <- msg:
			case <-ctx.Done():
				return
			case <-b.closed:
				return
			}
		}
	}()

	return ch, nil
}

func (b *streamBridge) Close() error {
	select {
	case <-b.closed:
		return nil // already closed
	default:
		close(b.closed)
	}

	if b.conn != nil {
		return b.conn.Close()
	}
	return nil
}
