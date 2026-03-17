package ipcbus

import (
	"context"
	"fmt"
	"net"
)

// TCPBridge implements [RuntimeBridge] over raw TCP with length-prefix
// framing, used by the PicoClaw runtime.
type TCPBridge struct {
	addr string
	streamBridge
}

// NewTCPBridge creates a TCP bridge targeting the given host:port address.
func NewTCPBridge(addr string) *TCPBridge {
	return &TCPBridge{
		addr:         addr,
		streamBridge: newStreamBridge(),
	}
}

func (b *TCPBridge) Connect(ctx context.Context) error {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", b.addr)
	if err != nil {
		return fmt.Errorf("failed to dial tcp %s: %w", b.addr, err)
	}
	b.conn = conn
	return nil
}
