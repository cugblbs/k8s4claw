package ipcbus

import (
	"context"
	"fmt"
	"net"
)

// UDSBridge implements [RuntimeBridge] over Unix Domain Sockets with
// length-prefix framing, used by the NanoClaw runtime.
type UDSBridge struct {
	path string
	streamBridge
}

// NewUDSBridge creates a UDS bridge targeting the given socket path.
func NewUDSBridge(path string) *UDSBridge {
	return &UDSBridge{
		path:         path,
		streamBridge: newStreamBridge(),
	}
}

func (b *UDSBridge) Connect(ctx context.Context) error {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", b.path)
	if err != nil {
		return fmt.Errorf("failed to dial unix %s: %w", b.path, err)
	}
	b.conn = conn
	return nil
}
