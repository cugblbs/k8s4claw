package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
)

type slackConfig struct {
	// AppLevelToken is used for Socket Mode connection (xapp-...).
	// Can also be set via SLACK_APP_TOKEN env var.
	AppLevelToken string `json:"appLevelToken,omitempty"`

	// BotToken is used for Web API calls (xoxb-...).
	// Can also be set via SLACK_BOT_TOKEN env var.
	BotToken string `json:"botToken,omitempty"`

	// DefaultChannel is the Slack channel ID for outbound messages.
	DefaultChannel string `json:"defaultChannel,omitempty"`

	// ListenPort is the HTTP port for health checks.
	ListenPort int `json:"listenPort"`

	// RetryAttempts is the number of retry attempts for outbound posts.
	RetryAttempts int `json:"retryAttempts"`

	// SlackAPIURL overrides the Slack API base URL (for testing).
	SlackAPIURL string `json:"slackAPIURL,omitempty"`
}

func parseConfig(raw string) (*slackConfig, error) {
	cfg := &slackConfig{
		ListenPort:    8080,
		RetryAttempts: 3,
		SlackAPIURL:   "https://slack.com/api",
	}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), cfg); err != nil {
			return nil, fmt.Errorf("failed to parse CHANNEL_CONFIG: %w", err)
		}
	}
	if cfg.ListenPort == 0 {
		cfg.ListenPort = 8080
	}
	if cfg.RetryAttempts == 0 {
		cfg.RetryAttempts = 3
	}
	if cfg.SlackAPIURL == "" {
		cfg.SlackAPIURL = "https://slack.com/api"
	}

	// Env vars take precedence over JSON config.
	if v := os.Getenv("SLACK_APP_TOKEN"); v != "" {
		cfg.AppLevelToken = v
	}
	if v := os.Getenv("SLACK_BOT_TOKEN"); v != "" {
		cfg.BotToken = v
	}

	return cfg, nil
}

// sender abstracts the channel SDK Send method for testing.
type sender interface {
	Send(ctx context.Context, payload json.RawMessage) error
}

// socketModeConn abstracts the Socket Mode WebSocket connection for testing.
type socketModeConn interface {
	// Connect opens the Socket Mode connection.
	Connect(ctx context.Context) error
	// ReadEvent blocks until an event is received or ctx is cancelled.
	ReadEvent(ctx context.Context) (json.RawMessage, string, error)
	// Acknowledge sends an acknowledgement for an envelope.
	Acknowledge(ctx context.Context, envelopeID string) error
	// Connected reports whether the WebSocket is currently connected.
	Connected() bool
	// Close closes the connection.
	Close() error
}

// slackSocketMode implements socketModeConn using raw HTTP and WebSocket.
type slackSocketMode struct {
	appToken  string
	apiURL    string
	wsConn    wsConn
	connected atomic.Bool
}

// wsConn abstracts a WebSocket connection for testing/pluggability.
type wsConn interface {
	ReadMessage(ctx context.Context) ([]byte, error)
	WriteMessage(ctx context.Context, data []byte) error
	Close() error
}

func newSlackSocketMode(appToken, apiURL string) *slackSocketMode {
	return &slackSocketMode{
		appToken: appToken,
		apiURL:   apiURL,
	}
}

func (s *slackSocketMode) Connect(ctx context.Context) error {
	// Step 1: Call apps.connections.open to get a WebSocket URL.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.apiURL+"/apps.connections.open", nil)
	if err != nil {
		return fmt.Errorf("failed to create connections.open request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.appToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Do(req) //nolint:gosec // URL is constructed from trusted API config
	if err != nil {
		return fmt.Errorf("failed to call apps.connections.open: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("failed to read connections.open response: %w", err)
	}

	var connResp struct {
		OK    bool   `json:"ok"`
		URL   string `json:"url"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &connResp); err != nil {
		return fmt.Errorf("failed to parse connections.open response: %w", err)
	}
	if !connResp.OK {
		return fmt.Errorf("apps.connections.open failed: %s", connResp.Error)
	}

	// Step 2: Connect to the WebSocket URL using coder/websocket.
	ws, err := dialWebSocket(ctx, connResp.URL)
	if err != nil {
		return fmt.Errorf("failed to connect WebSocket: %w", err)
	}
	s.wsConn = ws
	s.connected.Store(true)
	return nil
}

func (s *slackSocketMode) ReadEvent(ctx context.Context) (json.RawMessage, string, error) {
	if s.wsConn == nil {
		return nil, "", fmt.Errorf("not connected")
	}
	data, err := s.wsConn.ReadMessage(ctx)
	if err != nil {
		s.connected.Store(false)
		return nil, "", fmt.Errorf("failed to read WebSocket message: %w", err)
	}

	// Parse the Socket Mode envelope.
	var envelope struct {
		EnvelopeID string          `json:"envelope_id"`
		Type       string          `json:"type"`
		Payload    json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, "", fmt.Errorf("failed to parse Socket Mode envelope: %w", err)
	}

	return data, envelope.EnvelopeID, nil
}

func (s *slackSocketMode) Acknowledge(ctx context.Context, envelopeID string) error {
	if s.wsConn == nil {
		return fmt.Errorf("not connected")
	}
	ack, err := json.Marshal(map[string]string{"envelope_id": envelopeID})
	if err != nil {
		return fmt.Errorf("failed to marshal acknowledge: %w", err)
	}
	if err := s.wsConn.WriteMessage(ctx, ack); err != nil {
		return fmt.Errorf("failed to send acknowledge: %w", err)
	}
	return nil
}

func (s *slackSocketMode) Connected() bool {
	return s.connected.Load()
}

func (s *slackSocketMode) Close() error {
	s.connected.Store(false)
	if s.wsConn != nil {
		return s.wsConn.Close()
	}
	return nil
}

// runInboundLoop reads events from Socket Mode and forwards them to the IPC Bus.
func runInboundLoop(ctx context.Context, conn socketModeConn, s sender, logger logr.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		payload, envelopeID, err := conn.ReadEvent(ctx)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			logger.Error(err, "failed to read Socket Mode event")
			// Try to reconnect.
			for {
				select {
				case <-ctx.Done():
					return
				case <-time.After(2 * time.Second):
				}
				if err := conn.Connect(ctx); err != nil {
					logger.Error(err, "Socket Mode reconnect failed")
					continue
				}
				logger.Info("Socket Mode reconnected")
				break
			}
			continue
		}

		// Acknowledge the envelope if it has an ID (events_api type).
		if envelopeID != "" {
			if err := conn.Acknowledge(ctx, envelopeID); err != nil {
				logger.Error(err, "failed to acknowledge envelope", "envelopeID", envelopeID)
			}
		}

		// Forward the raw envelope payload to the IPC Bus.
		if err := s.Send(ctx, payload); err != nil {
			logger.Error(err, "failed to send to IPC Bus")
		}
	}
}

// outboundMessage is the expected format from the runtime.
type outboundMessage struct {
	Channel string          `json:"channel,omitempty"`
	Text    string          `json:"text"`
	Blocks  json.RawMessage `json:"blocks,omitempty"`
}

// slackPoster posts messages to Slack via the Web API.
type slackPoster struct {
	cfg    *slackConfig
	client *http.Client
}

func newSlackPoster(cfg *slackConfig) *slackPoster {
	return &slackPoster{
		cfg:    cfg,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (p *slackPoster) post(ctx context.Context, payload json.RawMessage) error {
	var msg outboundMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		return fmt.Errorf("failed to parse outbound message: %w", err)
	}

	channel := msg.Channel
	if channel == "" {
		channel = p.cfg.DefaultChannel
	}
	if channel == "" {
		return fmt.Errorf("no channel specified and no default channel configured")
	}

	// Build the chat.postMessage payload.
	apiPayload := map[string]any{
		"channel": channel,
		"text":    msg.Text,
	}
	if len(msg.Blocks) > 0 {
		apiPayload["blocks"] = json.RawMessage(msg.Blocks)
	}

	body, err := json.Marshal(apiPayload)
	if err != nil {
		return fmt.Errorf("failed to marshal Slack API payload: %w", err)
	}

	var lastErr error
	for attempt := range p.cfg.RetryAttempts {
		err := p.doPost(ctx, body)
		if err == nil {
			return nil
		}
		lastErr = err

		// Non-transient Slack API errors should not be retried.
		var apiErr *slackAPIError
		if errors.As(err, &apiErr) {
			return err
		}

		// Check for rate limit error with retry-after.
		var rlErr *rateLimitError
		if errors.As(err, &rlErr) {
			select {
			case <-time.After(rlErr.retryAfter):
			case <-ctx.Done():
				return fmt.Errorf("outbound post cancelled: %w", ctx.Err())
			}
			continue
		}

		if err := retrySleep(ctx, attempt); err != nil {
			return fmt.Errorf("outbound post cancelled: %w", err)
		}
	}
	return fmt.Errorf("outbound post failed after %d attempts: %w", p.cfg.RetryAttempts, lastErr)
}

type rateLimitError struct {
	retryAfter time.Duration
}

func (e *rateLimitError) Error() string {
	return fmt.Sprintf("rate limited, retry after %v", e.retryAfter)
}

// slackAPIError represents a non-transient Slack API error (e.g. channel_not_found).
type slackAPIError struct {
	code string
}

func (e *slackAPIError) Error() string {
	return fmt.Sprintf("chat.postMessage failed: %s", e.code)
}

func (p *slackPoster) doPost(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.SlackAPIURL+"/chat.postMessage", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+p.cfg.BotToken)

	resp, err := p.client.Do(req) //nolint:gosec // URL is constructed from trusted API config
	if err != nil {
		return fmt.Errorf("failed to call chat.postMessage: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := 1 * time.Second
		if v := resp.Header.Get("Retry-After"); v != "" {
			if secs, err := strconv.Atoi(v); err == nil {
				retryAfter = time.Duration(secs) * time.Second
			}
		}
		return &rateLimitError{retryAfter: retryAfter}
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("failed to read chat.postMessage response: %w", err)
	}

	var slackResp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(respBody, &slackResp); err != nil {
		return fmt.Errorf("failed to parse chat.postMessage response: %w", err)
	}
	if !slackResp.OK {
		return &slackAPIError{code: slackResp.Error}
	}

	return nil
}

func retrySleep(ctx context.Context, attempt int) error {
	select {
	case <-time.After(time.Duration(attempt+1) * time.Second):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// healthHandler returns 200 if connected, 503 otherwise.
type healthHandler struct {
	isHealthy func() bool
}

func newHealthHandler(isHealthy func() bool) *healthHandler {
	return &healthHandler{isHealthy: isHealthy}
}

func (h *healthHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	if h.isHealthy() {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("unhealthy"))
	}
}
