package ipcbus

import (
	"context"
	"fmt"
)

// UDSBridge implements [RuntimeBridge] over Unix Domain Sockets,
// used by the NanoClaw runtime. Currently a stub.
type UDSBridge struct {
	addr string
}

// NewUDSBridge creates a stub UDS bridge targeting the given address.
func NewUDSBridge(addr string) *UDSBridge {
	return &UDSBridge{addr: addr}
}

func (b *UDSBridge) Connect(_ context.Context) error {
	return fmt.Errorf("UDS bridge not yet implemented")
}

func (b *UDSBridge) Send(_ context.Context, _ *Message) error {
	return fmt.Errorf("UDS bridge not yet implemented")
}

func (b *UDSBridge) Receive(_ context.Context) (<-chan *Message, error) {
	return nil, fmt.Errorf("UDS bridge not yet implemented")
}

func (b *UDSBridge) Close() error {
	return nil
}
