package ipcbus

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// SSEBridge implements [RuntimeBridge] over Server-Sent Events (inbound)
// and HTTP POST (outbound), used by the ZeroClaw runtime.
//
// Send posts JSON to {baseURL}/messages.
// Receive reads an SSE stream from {baseURL}/events.
type SSEBridge struct {
	baseURL   string
	client    *http.Client // for Send (POST) and Connect (HEAD), with timeout
	sseClient *http.Client // for Receive (GET /events), no timeout

	mu        sync.Mutex
	sseCancel context.CancelFunc
	closed    chan struct{}
}

// NewSSEBridge creates an SSE bridge targeting the given base URL.
func NewSSEBridge(url string) *SSEBridge {
	return &SSEBridge{
		baseURL: url,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		sseClient: &http.Client{},
		closed:    make(chan struct{}),
	}
}

func (b *SSEBridge) Connect(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, b.baseURL+"/events", nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := b.client.Do(req) //nolint:gosec // URL is constructed from trusted baseURL config
	if err != nil {
		return fmt.Errorf("failed to reach SSE endpoint %s/events: %w", b.baseURL, err)
	}
	_ = resp.Body.Close()
	return nil
}

func (b *SSEBridge) Send(ctx context.Context, msg *Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+"/messages", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("failed to create POST request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(req) //nolint:gosec // URL is constructed from trusted baseURL config
	if err != nil {
		return fmt.Errorf("failed to POST message: %w", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("POST /messages returned status %d", resp.StatusCode)
	}
	return nil
}

func (b *SSEBridge) Receive(ctx context.Context) (<-chan *Message, error) {
	ch := make(chan *Message, 64)

	sseCtx, cancel := context.WithCancel(ctx)

	b.mu.Lock()
	b.sseCancel = cancel
	b.mu.Unlock()

	go b.readSSELoop(sseCtx, ch)

	return ch, nil
}

func (b *SSEBridge) readSSELoop(ctx context.Context, ch chan<- *Message) {
	defer close(ch)

	backoff := time.Second

	for {
		select {
		case <-b.closed:
			return
		case <-ctx.Done():
			return
		default:
		}

		err := b.readSSEStream(ctx, ch)
		if err == nil {
			return // clean shutdown
		}

		select {
		case <-b.closed:
			return
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

func (b *SSEBridge) readSSEStream(ctx context.Context, ch chan<- *Message) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.baseURL+"/events", nil)
	if err != nil {
		return fmt.Errorf("failed to create SSE request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := b.sseClient.Do(req) //nolint:gosec // URL is constructed from trusted baseURL config
	if err != nil {
		return fmt.Errorf("SSE connection failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	scanner := bufio.NewScanner(resp.Body)
	// Allow up to 1MB per SSE line to handle large messages.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var dataLines []string

	for scanner.Scan() {
		line := scanner.Text()

		switch {
		case strings.HasPrefix(line, "data:"):
			// Per SSE spec: strip "data:" prefix, then optional single leading space.
			value, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				value, _ = strings.CutPrefix(line, "data:")
			}
			dataLines = append(dataLines, value)
		case line == "":
			if len(dataLines) > 0 {
				payload := strings.Join(dataLines, "\n")
				dataLines = nil

				var msg Message
				if err := json.Unmarshal([]byte(payload), &msg); err != nil {
					continue // skip malformed events
				}

				select {
				case ch <- &msg:
				case <-ctx.Done():
					return nil
				case <-b.closed:
					return nil
				}
			}
		default:
			// SSE comment or unknown line, ignore.
		}
	}

	return scanner.Err()
}

func (b *SSEBridge) Close() error {
	select {
	case <-b.closed:
		return nil // already closed
	default:
		close(b.closed)
	}

	b.mu.Lock()
	cancel := b.sseCancel
	b.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	return nil
}
