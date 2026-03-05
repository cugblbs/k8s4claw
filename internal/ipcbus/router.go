package ipcbus

import (
	"context"
	"fmt"
	"sync"

	"github.com/go-logr/logr"
)

const (
	defaultBufferSize    = 1024
	defaultHighWatermark = 0.8
	defaultLowWatermark  = 0.3
	maxRetryAttempts     = 5
)

// RouterConfig holds configuration for a Router.
type RouterConfig struct {
	Bridge        RuntimeBridge
	WAL           *WAL
	DLQ           *DLQ
	Logger        logr.Logger
	BufferSize    int
	HighWatermark float64
	LowWatermark  float64
}

// Router handles message routing between sidecars and the runtime bridge.
type Router struct {
	mu            sync.RWMutex
	sidecars      map[string]*SidecarConn
	bridge        RuntimeBridge
	wal           *WAL
	dlq           *DLQ
	buffers       map[string]*RingBuffer
	logger        logr.Logger
	bufferSize    int
	highWatermark float64
	lowWatermark  float64
}

// NewRouter creates a Router with the given configuration. Zero/invalid
// watermark or buffer size values are replaced with defaults.
func NewRouter(cfg RouterConfig) *Router {
	bs := cfg.BufferSize
	if bs <= 0 {
		bs = defaultBufferSize
	}
	hw := cfg.HighWatermark
	if hw <= 0 || hw > 1.0 {
		hw = defaultHighWatermark
	}
	lw := cfg.LowWatermark
	if lw <= 0 || lw >= hw {
		lw = defaultLowWatermark
	}

	return &Router{
		sidecars:      make(map[string]*SidecarConn),
		bridge:        cfg.Bridge,
		wal:           cfg.WAL,
		dlq:           cfg.DLQ,
		buffers:       make(map[string]*RingBuffer),
		logger:        cfg.Logger,
		bufferSize:    bs,
		highWatermark: hw,
		lowWatermark:  lw,
	}
}

// Register adds a sidecar connection and creates a per-channel ring buffer.
func (r *Router) Register(sc *SidecarConn) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.sidecars[sc.Channel] = sc
	if _, ok := r.buffers[sc.Channel]; !ok {
		r.buffers[sc.Channel] = NewRingBuffer(r.bufferSize, r.highWatermark, r.lowWatermark)
	}
	r.logger.Info("sidecar registered", "channel", sc.Channel, "mode", sc.Mode)
}

// Unregister removes a sidecar connection.
func (r *Router) Unregister(sc *SidecarConn) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.sidecars, sc.Channel)
	r.logger.Info("sidecar unregistered", "channel", sc.Channel)
}

// ConnectedCount returns the number of currently registered sidecars.
func (r *Router) ConnectedCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.sidecars)
}

// HandleInbound processes a message received from a sidecar. Control messages
// (heartbeat) are handled directly; data messages are WAL-appended, buffered,
// and forwarded to the runtime bridge.
func (r *Router) HandleInbound(ctx context.Context, from *SidecarConn, msg *Message) {
	if msg.IsControl() {
		r.handleControl(from, msg)
		return
	}

	// WAL append for durability.
	if r.wal != nil {
		if err := r.wal.Append(msg); err != nil {
			r.logger.Error(err, "failed to append to WAL", "msgID", msg.ID)
		}
	}

	// Push to per-channel ring buffer.
	r.mu.RLock()
	buf := r.buffers[from.Channel]
	r.mu.RUnlock()

	if buf != nil {
		ok, stateChanged := buf.Push(msg)
		if !ok {
			r.logger.Info("ring buffer full, spilling message", "channel", from.Channel, "msgID", msg.ID)
		}
		if stateChanged {
			r.logger.Info("backpressure: sending slow_down", "channel", from.Channel)
			slowDown := NewMessage(TypeSlowDown, from.Channel, nil)
			if err := from.Send(slowDown); err != nil {
				r.logger.Error(err, "failed to send slow_down", "channel", from.Channel)
			}
		}
	}

	// Forward to bridge.
	if r.bridge != nil {
		if err := r.bridge.Send(ctx, msg); err != nil {
			r.logger.Error(err, "bridge send failed, scheduling retry", "msgID", msg.ID)
			r.scheduleRetry(ctx, msg)
			return
		}
	}

	// On success: WAL complete + pop buffer.
	if r.wal != nil {
		if err := r.wal.Complete(msg.ID); err != nil {
			r.logger.Error(err, "failed to complete WAL entry", "msgID", msg.ID)
		}
	}

	if buf != nil {
		_, stateChanged := buf.Pop()
		if stateChanged {
			r.logger.Info("backpressure: sending resume", "channel", from.Channel)
			resume := NewMessage(TypeResume, from.Channel, nil)
			if err := from.Send(resume); err != nil {
				r.logger.Error(err, "failed to send resume", "channel", from.Channel)
			}
		}
	}
}

// handleControl processes control messages from sidecars.
func (r *Router) handleControl(from *SidecarConn, msg *Message) {
	switch msg.Type {
	case TypeHeartbeat:
		ack := NewAck(msg.ID)
		if err := from.Send(ack); err != nil {
			r.logger.Error(err, "failed to send heartbeat ACK", "channel", from.Channel)
		}
	default:
		r.logger.Info("unhandled control message", "type", msg.Type, "channel", from.Channel)
	}
}

// HandleOutbound routes a message from the bridge to the appropriate sidecar.
func (r *Router) HandleOutbound(ctx context.Context, msg *Message) {
	r.mu.RLock()
	sc, ok := r.sidecars[msg.Channel]
	r.mu.RUnlock()

	if !ok {
		r.logger.Info("no sidecar for outbound message", "channel", msg.Channel, "msgID", msg.ID)
		return
	}

	if err := sc.Send(msg); err != nil {
		r.logger.Error(err, "failed to send outbound message", "channel", msg.Channel, "msgID", msg.ID)
	}
}

// StartOutboundLoop reads messages from the bridge and routes them to sidecars.
// It blocks until ctx is cancelled or the bridge channel is closed.
func (r *Router) StartOutboundLoop(ctx context.Context) {
	if r.bridge == nil {
		return
	}

	ch, err := r.bridge.Receive(ctx)
	if err != nil {
		r.logger.Error(err, "failed to start bridge receive")
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			r.HandleOutbound(ctx, msg)
		}
	}
}

// ReplayWAL re-enqueues pending WAL entries by forwarding them to the bridge.
func (r *Router) ReplayWAL(ctx context.Context) {
	if r.wal == nil {
		return
	}

	entries := r.wal.PendingEntries()
	r.logger.Info("replaying WAL", "pendingCount", len(entries))

	for _, entry := range entries {
		if entry.Msg == nil {
			continue
		}
		if r.bridge != nil {
			if err := r.bridge.Send(ctx, entry.Msg); err != nil {
				r.logger.Error(err, "WAL replay: bridge send failed", "msgID", entry.ID)
				continue
			}
		}
		if err := r.wal.Complete(entry.ID); err != nil {
			r.logger.Error(err, "WAL replay: failed to complete entry", "msgID", entry.ID)
		}
	}
}

// SendShutdown sends a shutdown message to all connected sidecars.
func (r *Router) SendShutdown() {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for channel, sc := range r.sidecars {
		msg := NewMessage(TypeShutdown, channel, nil)
		if err := sc.Send(msg); err != nil {
			r.logger.Error(err, "failed to send shutdown", "channel", channel)
		}
	}
}

// scheduleRetry increments WAL attempts and moves to DLQ after max retries.
func (r *Router) scheduleRetry(_ context.Context, msg *Message) {
	if r.wal == nil {
		return
	}

	attempts, err := r.wal.IncrementAttempts(msg.ID)
	if err != nil {
		r.logger.Error(err, "failed to increment WAL attempts", "msgID", msg.ID)
		return
	}

	if attempts >= maxRetryAttempts {
		r.logger.Info("max retries reached, moving to DLQ", "msgID", msg.ID, "attempts", attempts)
		if err := r.wal.MarkDLQ(msg.ID); err != nil {
			r.logger.Error(err, "failed to mark WAL entry as DLQ", "msgID", msg.ID)
		}
		if r.dlq != nil {
			if err := r.dlq.Put(msg, attempts); err != nil {
				r.logger.Error(err, "failed to put message in DLQ", "msgID", msg.ID)
			}
		}
		return
	}

	r.logger.Info("retry scheduled", "msgID", msg.ID, "attempt", attempts,
		"remaining", fmt.Sprintf("%d/%d", attempts, maxRetryAttempts))
}
