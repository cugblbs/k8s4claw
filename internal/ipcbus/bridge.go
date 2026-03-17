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

// BridgeConfig holds parameters for constructing a RuntimeBridge.
type BridgeConfig struct {
	GatewayPort int    // used by OpenClaw (WS), PicoClaw (TCP), ZeroClaw (SSE)
	SocketPath  string // used by NanoClaw (UDS)
}

// NewBridge returns the appropriate [RuntimeBridge] for the given runtime.
func NewBridge(rt RuntimeType, cfg BridgeConfig) (RuntimeBridge, error) {
	switch rt {
	case RuntimeOpenClaw:
		return NewWebSocketBridge(fmt.Sprintf("ws://localhost:%d", cfg.GatewayPort)), nil
	case RuntimeNanoClaw:
		return NewUDSBridge(cfg.SocketPath), nil
	case RuntimeZeroClaw:
		return NewSSEBridge(fmt.Sprintf("http://localhost:%d", cfg.GatewayPort)), nil
	case RuntimePicoClaw:
		return NewTCPBridge(fmt.Sprintf("localhost:%d", cfg.GatewayPort)), nil
	default:
		return nil, fmt.Errorf("unsupported runtime type: %s", rt)
	}
}
