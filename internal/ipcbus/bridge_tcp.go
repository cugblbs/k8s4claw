package ipcbus

import (
	"context"
	"fmt"
)

// TCPBridge implements [RuntimeBridge] over raw TCP,
// used by the PicoClaw runtime. Currently a stub.
type TCPBridge struct {
	addr string
}

// NewTCPBridge creates a stub TCP bridge targeting the given address.
func NewTCPBridge(addr string) *TCPBridge {
	return &TCPBridge{addr: addr}
}

func (b *TCPBridge) Connect(_ context.Context) error {
	return fmt.Errorf("TCP bridge not yet implemented")
}

func (b *TCPBridge) Send(_ context.Context, _ *Message) error {
	return fmt.Errorf("TCP bridge not yet implemented")
}

func (b *TCPBridge) Receive(_ context.Context) (<-chan *Message, error) {
	return nil, fmt.Errorf("TCP bridge not yet implemented")
}

func (b *TCPBridge) Close() error {
	return nil
}
