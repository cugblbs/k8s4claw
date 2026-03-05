# Channel SDK + Webhook Sidecar Design

## Context

Phase 2 requires a **Channel SDK** (Go library for sidecars to connect to the IPC Bus) and **built-in sidecar implementations**. This design covers the SDK and the Webhook sidecar. Slack sidecar is deferred.

## Channel SDK (`sdk/channel/`)

### Purpose

A Go library that all channel sidecars (built-in and custom) import to communicate with the IPC Bus over UDS. Handles protocol complexity so sidecar authors only implement platform-specific logic.

### API

```go
package channel

func Connect(ctx context.Context, opts ...Option) (*Client, error)
func (c *Client) Send(ctx context.Context, payload json.RawMessage) error
func (c *Client) Receive(ctx context.Context) (<-chan *InboundMessage, error)
func (c *Client) Close() error
```

### Options

| Option | Default | Description |
|--------|---------|-------------|
| `WithSocketPath(path)` | `$IPC_SOCKET_PATH` or `/var/run/claw/ipc.sock` | UDS path |
| `WithChannelName(name)` | `$CHANNEL_NAME` | Channel name for registration |
| `WithChannelMode(mode)` | `$CHANNEL_MODE` | inbound/outbound/bidirectional |
| `WithBufferSize(n)` | 256 | Bus-down buffer capacity |
| `WithReconnectInterval(d)` | 2s (base, exponential backoff) | Reconnect timing |
| `WithHeartbeatInterval(d)` | 30s | Heartbeat period |
| `WithLogger(logger)` | discard | logr.Logger |

### Behaviors

1. **Registration**: On connect, sends `TypeRegister` message with channel name, waits for ACK.
2. **Bus-down buffering**: When UDS connection is lost, `Send()` queues messages in an in-memory ring buffer. On reconnect, buffered messages replay in FIFO order before new sends.
3. **Backpressure**: Honors `slow_down`/`resume` signals from the IPC Bus. When throttled, `Send()` blocks until resume (or context cancellation).
4. **Heartbeat**: Sends periodic `TypeHeartbeat`, expects ACK. Missed heartbeats trigger reconnection.
5. **Auto-reconnect**: Exponential backoff (2s → 4s → 8s → ... capped at 60s). Re-registers on reconnect.
6. **Graceful shutdown**: On `TypeShutdown` or `Close()`, drains pending buffer, closes UDS connection.

### Wire Protocol

Reuses `internal/ipcbus` framing: 4-byte big-endian length prefix + JSON body. The SDK reimplements the framing (not importing internal packages) to keep `sdk/` dependency-free from operator internals.

## Webhook Sidecar (`cmd/channel-webhook/`)

### Purpose

A built-in sidecar that bridges HTTP webhooks with the IPC Bus. No external dependencies beyond the Channel SDK and standard library.

### Inbound (external → runtime)

- HTTP server on configurable port (default 8080)
- Accepts POST requests at `/webhook`
- Optional HMAC-SHA256 verification via `X-Signature-256` header
- Wraps request body as IPC Message payload, sends via Channel SDK
- Returns 202 Accepted on success, 401/500 on failure

### Outbound (runtime → external)

- Receives messages from Channel SDK via `Receive()`
- POSTs payload to configured target URL
- Configurable headers (e.g., Authorization)
- Retry with exponential backoff (3 attempts)

### Configuration

From `CHANNEL_CONFIG` env var (JSON):

```json
{
  "listenPort": 8080,
  "path": "/webhook",
  "targetURL": "https://example.com/hook",
  "secret": "hmac-secret",
  "headers": {"Authorization": "Bearer token"},
  "retryAttempts": 3
}
```

Credentials can also come via Secret envFrom (preferred for production).

### Health

- `GET /healthz` returns 200 when connected to IPC Bus, 503 otherwise.

## File Layout

| File | Purpose |
|------|---------|
| `sdk/channel/client.go` | Core Client: Connect, Send, Receive, Close |
| `sdk/channel/options.go` | Functional options |
| `sdk/channel/buffer.go` | In-memory ring buffer for bus-down buffering |
| `sdk/channel/message.go` | InboundMessage type + framing (reimplemented) |
| `sdk/channel/client_test.go` | Unit tests with mock UDS server |
| `cmd/channel-webhook/main.go` | Entry point, config parsing, signal handling |
| `cmd/channel-webhook/handler.go` | HTTP inbound handler + outbound poster |
| `cmd/channel-webhook/handler_test.go` | Table-driven tests |

## Testing

- **SDK unit tests**: Mock UDS server testing registration, send/receive, backpressure, reconnection, bus-down buffering.
- **Webhook unit tests**: HTTP handler tests (inbound HMAC verification, outbound posting), config parsing.
- **Integration**: Manual test with `socat` or a test IPC Bus server instance.

## Not In Scope

- Slack sidecar (deferred)
- Telegram, Discord, Matrix, WhatsApp sidecars
- Dockerfile / container builds (future CI task)
