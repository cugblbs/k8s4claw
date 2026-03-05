package ipcbus

import (
	"context"
	"fmt"
)

// SSEBridge implements [RuntimeBridge] over Server-Sent Events,
// used by the ZeroClaw runtime. Currently a stub.
type SSEBridge struct {
	url string
}

// NewSSEBridge creates a stub SSE bridge targeting the given URL.
func NewSSEBridge(url string) *SSEBridge {
	return &SSEBridge{url: url}
}

func (b *SSEBridge) Connect(_ context.Context) error {
	return fmt.Errorf("SSE bridge not yet implemented")
}

func (b *SSEBridge) Send(_ context.Context, _ *Message) error {
	return fmt.Errorf("SSE bridge not yet implemented")
}

func (b *SSEBridge) Receive(_ context.Context) (<-chan *Message, error) {
	return nil, fmt.Errorf("SSE bridge not yet implemented")
}

func (b *SSEBridge) Close() error {
	return nil
}
