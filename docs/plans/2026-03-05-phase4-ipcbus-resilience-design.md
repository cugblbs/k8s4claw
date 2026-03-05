# Phase 4: IPC Bus + Resilience Design

**Date:** 2026-03-05
**Status:** Approved
**Depends on:** Phase 1 (Foundation), Phase 2 (Channel sidecar injection), Phase 3 (Persistence + Security)

## 1. Overview

Phase 4 implements the IPC Bus — the central message router that connects channel sidecars to AI agent runtimes — and builds resilience features on top: backpressure, WAL, DLQ, sidecar reconnection, and graceful shutdown.

### Goals

1. Enable actual message flow between channel sidecars and runtimes
2. Provide at-least-once delivery with WAL-backed durability
3. Protect runtimes from message overload via 4-layer backpressure
4. Handle sidecar/bus failures gracefully with reconnection and local buffering
5. Implement ordered graceful shutdown with message drain

### Non-Goals

- Multi-pod message routing (single-replica only in v1alpha1)
- Persistent WAL across pod restarts (emptyDir is sufficient)
- Custom channel SDK (deferred to Phase 5)

## 2. IPC Bus Architecture

### 2.1 Deployment

The IPC Bus runs as a **native sidecar** container (init container with `restartPolicy: Always`), consistent with the channel sidecar and archiver sidecar patterns already in the codebase.

- **Image:** `ghcr.io/prismer-ai/claw-ipcbus:latest`
- **Binary:** `cmd/ipcbus/main.go`
- **Core logic:** `internal/ipcbus/`
- **Socket:** `/var/run/claw/bus.sock` (existing `ipc-socket` emptyDir volume)
- **WAL/DLQ storage:** `/var/run/claw/wal/` (existing `wal-data` emptyDir volume, 512Mi)
- **Resources:** 100m/128Mi request, 500m/512Mi limit

### 2.2 Message Protocol

**Wire format:** 4-byte big-endian length prefix + JSON body over Unix Domain Socket.

```
┌──────────┬──────────────────────────┐
│ len (4B) │ JSON payload (len bytes) │
└──────────┴──────────────────────────┘
```

**Message envelope:**

```json
{
  "id": "uuid-v7",
  "type": "message|ack|nack|slow_down|resume|shutdown|register|heartbeat",
  "channel": "slack-team",
  "correlationId": "uuid-of-original",
  "replyTo": "channel-name",
  "timestamp": "2026-03-05T10:00:00Z",
  "payload": { ... }
}
```

- `id`: UUIDv7 (time-ordered) for WAL ordering
- `type`: control plane (`register`, `ack`, `nack`, `slow_down`, `resume`, `shutdown`, `heartbeat`) vs data plane (`message`)
- `correlationId` + `replyTo`: request-response pairing for streaming
- `payload`: opaque JSON, passed through to runtime/sidecar

### 2.3 Connection Lifecycle

```
Sidecar starts → connect to bus.sock → send "register" {channel: "slack-team", mode: "bidirectional"}
                                      → Bus ACKs registration
                                      → bidirectional message flow
                                      → Bus sends "shutdown" on termination
                                      → Sidecar drains → disconnects
```

## 3. Message Flow

```
                    ┌─────────────────────────────────┐
                    │           IPC Bus                │
                    │                                  │
Slack Sidecar ─UDS─→│  Router ──→ RuntimeBridge ──→ Runtime (OpenClaw:18900)
                    │    ↕                             │
Webhook Sidecar─UDS─→│  Ring Buffer                    │
                    │    ↕                             │
                    │  WAL (append-only JSON files)    │
                    │  DLQ (BoltDB)                    │
                    └─────────────────────────────────┘
```

**Inbound (sidecar → runtime):**
1. Sidecar sends message to Bus via UDS
2. Bus writes to WAL (pre-commit)
3. Bus enqueues in channel's ring buffer
4. Bus forwards to runtime via RuntimeBridge
5. Runtime ACKs → Bus marks WAL entry complete

**Outbound (runtime → sidecar):**
1. Runtime sends message via RuntimeBridge
2. Bus routes to target sidecar by channel name
3. Sidecar ACKs → complete

## 4. RuntimeBridge Interface

Each runtime has a different gateway protocol. The `RuntimeBridge` interface adapts the Bus to each runtime:

```go
type RuntimeBridge interface {
    // Connect establishes a connection to the runtime gateway.
    Connect(ctx context.Context) error
    // Send delivers a message to the runtime.
    Send(ctx context.Context, msg *Message) error
    // Receive returns a channel of outbound messages from the runtime.
    Receive(ctx context.Context) (<-chan *Message, error)
    // Close gracefully disconnects.
    Close() error
}
```

| Runtime | Bridge | Protocol | Port |
|---------|--------|----------|------|
| OpenClaw | `WebSocketBridge` | WebSocket | 18900 |
| NanoClaw | `UDSBridge` | Unix Domain Socket | 19000 |
| ZeroClaw | `SSEBridge` | Server-Sent Events + HTTP POST | 3000 |
| PicoClaw | `TCPBridge` | Raw TCP with length-prefix framing | 8080 |

Bridge selection is determined by the `CLAW_RUNTIME` environment variable injected by the operator.

## 5. Backpressure (4-Layer)

### Layer 1: Ring Buffer

Per-channel in-memory ring buffer with configurable capacity (default: 1024 messages).

```go
type RingBuffer struct {
    buf      []*Message
    size     int
    head     int
    tail     int
    count    int
    highMark float64  // default: 0.8
    lowMark  float64  // default: 0.3
}
```

### Layer 2: Flow Control Signals

When buffer fill ratio crosses `highWatermark`:
- Bus sends `slow_down` message to the producing sidecar
- Sidecar reduces sending rate (implementation-specific)

When fill ratio drops below `lowWatermark`:
- Bus sends `resume` message
- Sidecar resumes normal rate

### Layer 3: Spill-to-Disk

When ring buffer is 100% full and new messages arrive:
- Overflow messages are appended to WAL spill file
- Spill file is drained back into ring buffer as space opens
- Prometheus metric `claw_ipcbus_spill_total` incremented

### Layer 4: Delta Merge (Degraded Mode)

Under sustained overload (spill file > 10,000 entries):
- Merge consecutive messages of same type from same channel
- Keep only latest state (e.g., latest status update replaces older ones)
- Emit `BackpressureCritical` K8s Event

### Configuration (from CRD)

Uses existing `BackpressureSpec` in `common_types.go`:

```go
type BackpressureSpec struct {
    BufferSize    int    `json:"bufferSize,omitempty"`    // default: 1024
    HighWatermark string `json:"highWatermark,omitempty"` // default: "0.8"
    LowWatermark  string `json:"lowWatermark,omitempty"`  // default: "0.3"
}
```

## 6. WAL (Write-Ahead Log)

### Storage

Append-only JSON files on `wal-data` emptyDir (`/var/run/claw/wal/`).

### Lifecycle

```
Message arrives → WAL append (state: pending) → deliver → ACK → WAL mark complete
                                                       → NACK/timeout → retry (up to 5x)
                                                       → exhausted → move to DLQ
```

### WAL Entry Format

```json
{"id":"uuid","channel":"slack","state":"pending","attempts":0,"ts":"...","msg":{...}}
```

States: `pending` → `complete` | `dlq`

### Compaction

- Triggered every 60 seconds or when file exceeds 10MB
- Removes `complete` entries, rewrites active entries to new file
- Old file deleted after successful rewrite

### Recovery

On IPC Bus startup:
1. Scan WAL files for `pending` entries
2. Re-enqueue into ring buffers
3. Resume delivery (attempt count preserved)

## 7. DLQ (Dead Letter Queue)

### Storage

BoltDB database at `/var/run/claw/wal/dlq.db`.

### Buckets

- `messages`: keyed by message ID, value is full message + metadata
- `index`: keyed by channel name, value is list of message IDs (for per-channel queries)

### Retry Policy

- Max attempts: 5
- Backoff: exponential 1s, 5s, 25s, 2m5s, 5m (capped)
- After exhaustion → move to DLQ

### Limits

- Max entries: 10,000
- TTL: 24 hours
- Eviction: oldest-first when limit reached
- On DLQ insert: emit K8s Event `DeadLetterQueued`, increment `claw_ipcbus_dlq_total` Prometheus metric

### DLQ Drain

- Expose HTTP endpoint `GET /dlq/messages` and `POST /dlq/retry/{id}` on localhost for debugging
- Optional: periodic retry of DLQ messages (configurable, default disabled)

## 8. Sidecar Reconnection

When the IPC Bus is temporarily unavailable (restart, crash):

### Reconnection Strategy

- Exponential backoff: 100ms → 200ms → 400ms → ... → 30s (cap)
- Jitter: +/- 10% randomization to avoid thundering herd
- Reconnect attempts are unlimited (sidecar keeps trying)

### Local Buffering (Bus-Down Mode)

- Each sidecar maintains an in-memory ring buffer (256 messages)
- When Bus connection is lost, incoming messages are buffered locally
- When Bus reconnects, sidecar flushes buffered messages in order
- If local buffer overflows, oldest messages are dropped with warning log
- Sidecar reports `bus_down` status via its health endpoint

## 9. Graceful Shutdown

### preStop Hook Sequence

Added to the IPC Bus sidecar container:

```yaml
lifecycle:
  preStop:
    exec:
      command: ["/claw-ipcbus", "shutdown"]
```

The `shutdown` subcommand:
1. Send `shutdown` message to all connected sidecars via UDS
2. Wait up to 5s for sidecars to drain in-flight messages
3. Flush WAL to disk (fsync)
4. Close UDS listener
5. Exit 0

### Runtime preStop Hook

Added to the runtime container:

```yaml
lifecycle:
  preStop:
    exec:
      command: ["sh", "-c", "sleep 2"]
```

The 2-second sleep allows the IPC Bus to complete its shutdown first (native sidecars terminate in reverse init order).

### Termination Grace Period

`terminationGracePeriodSeconds = runtime.GracefulShutdownSeconds() + 15`

The extra 15s (up from current 10s) accounts for IPC Bus drain + WAL flush.

## 10. Operator-Side Changes

### Pod Builder (`internal/runtime/pod_builder.go`)

- Inject IPC Bus native sidecar container (before channel sidecars in init order)
- Add `CLAW_RUNTIME` and `CLAW_GATEWAY_PORT` env vars to IPC Bus container
- Add preStop lifecycle hooks to IPC Bus and runtime containers
- Increase `terminationGracePeriodSeconds` by 5s

### IPC Bus Sidecar Injection (`internal/controller/claw_ipcbus.go`)

New file following `claw_archiver.go` pattern:
- `injectIPCBusSidecar(claw, podTemplate)` — builds and injects the sidecar
- `shouldInjectIPCBus(claw)` — true when any channel is configured
- Passes backpressure config via env vars

### IPC Bus Binary (`cmd/ipcbus/`)

- `main.go`: CLI entrypoint with `serve` (default) and `shutdown` subcommands
- Flags: `--socket-path`, `--wal-dir`, `--runtime`, `--gateway-port`

### IPC Bus Core (`internal/ipcbus/`)

```
internal/ipcbus/
├── server.go          # UDS listener, connection management
├── router.go          # Message routing (sidecar ↔ runtime)
├── message.go         # Message types, envelope, serialization
├── framing.go         # Length-prefix read/write helpers
├── bridge.go          # RuntimeBridge interface
├── bridge_ws.go       # WebSocket bridge (OpenClaw)
├── bridge_uds.go      # UDS bridge (NanoClaw)
├── bridge_sse.go      # SSE bridge (ZeroClaw)
├── bridge_tcp.go      # TCP bridge (PicoClaw)
├── ringbuffer.go      # Per-channel ring buffer
├── backpressure.go    # Flow control logic
├── wal.go             # Write-ahead log
├── dlq.go             # Dead letter queue (BoltDB)
├── shutdown.go        # Graceful shutdown orchestration
└── metrics.go         # Prometheus metrics
```

## 11. Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `claw_ipcbus_messages_total` | Counter | Messages routed (by channel, direction) |
| `claw_ipcbus_messages_inflight` | Gauge | Currently unACKed messages |
| `claw_ipcbus_buffer_usage_ratio` | Gauge | Ring buffer fill ratio (by channel) |
| `claw_ipcbus_spill_total` | Counter | Messages spilled to disk |
| `claw_ipcbus_dlq_total` | Counter | Messages sent to DLQ |
| `claw_ipcbus_dlq_size` | Gauge | Current DLQ entry count |
| `claw_ipcbus_retry_total` | Counter | Delivery retry attempts |
| `claw_ipcbus_bridge_connected` | Gauge | RuntimeBridge connection status (0/1) |
| `claw_ipcbus_sidecar_connections` | Gauge | Connected sidecar count |
| `claw_ipcbus_wal_entries` | Gauge | Pending WAL entries |

## 12. Testing Strategy

- **Unit tests:** Ring buffer, WAL compaction, DLQ CRUD, message framing, backpressure state machine
- **Integration tests:** Full Bus with mock sidecars and mock runtime bridge over real UDS
- **Operator tests:** IPC Bus sidecar injection, env var wiring, preStop hooks
- **Failure tests:** Sidecar disconnect/reconnect, Bus crash recovery from WAL, DLQ overflow eviction
