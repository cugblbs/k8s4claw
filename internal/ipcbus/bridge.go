package ipcbus

import (
	"context"
	"fmt"
)

// RuntimeBridge abstracts the transport layer between the IPC bus and
// a specific claw runtime. Each runtime flavour uses a different
// wire protocol, so concrete bridges translate to/from [Message].
type RuntimeBridge interface {
	Connect(ctx context.Context) error
	Send(ctx context.Context, msg *Message) error
	Receive(ctx context.Context) (<-chan *Message, error)
	Close() error
}

// RuntimeType identifies a claw runtime variant.
type RuntimeType string

const (
	RuntimeOpenClaw RuntimeType = "openclaw"
	RuntimeNanoClaw RuntimeType = "nanoclaw"
	RuntimeZeroClaw RuntimeType = "zeroclaw"
	RuntimePicoClaw RuntimeType = "picoclaw"
)

// NewBridge returns the appropriate [RuntimeBridge] for the given runtime.
func NewBridge(rt RuntimeType, gatewayPort int) (RuntimeBridge, error) {
	switch rt {
	case RuntimeOpenClaw:
		return NewWebSocketBridge(fmt.Sprintf("ws://localhost:%d", gatewayPort)), nil
	case RuntimeNanoClaw:
		return NewUDSBridge(fmt.Sprintf("localhost:%d", gatewayPort)), nil
	case RuntimeZeroClaw:
		return NewSSEBridge(fmt.Sprintf("http://localhost:%d", gatewayPort)), nil
	case RuntimePicoClaw:
		return NewTCPBridge(fmt.Sprintf("localhost:%d", gatewayPort)), nil
	default:
		return nil, fmt.Errorf("unsupported runtime type: %s", rt)
	}
}
