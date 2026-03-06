package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
)

// mockSender captures payloads sent via the channel SDK.
type mockSender struct {
	mu   sync.Mutex
	sent []json.RawMessage
}

func (m *mockSender) Send(_ context.Context, payload json.RawMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, payload)
	return nil
}

func (m *mockSender) getSent() []json.RawMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]json.RawMessage(nil), m.sent...)
}

// mockSocketModeConn is a mock Socket Mode connection for testing.
type mockSocketModeConn struct {
	mu         sync.Mutex
	events     []mockEvent
	acked      []string
	connected  bool
	connectErr error
	readIdx    int
}

type mockEvent struct {
	payload    json.RawMessage
	envelopeID string
	err        error
}

func newMockSocketModeConn(events []mockEvent) *mockSocketModeConn {
	return &mockSocketModeConn{
		events:    events,
		connected: true,
	}
}

func (m *mockSocketModeConn) Connect(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.connectErr != nil {
		return m.connectErr
	}
	m.connected = true
	return nil
}

func (m *mockSocketModeConn) ReadEvent(ctx context.Context) (json.RawMessage, string, error) {
	m.mu.Lock()
	idx := m.readIdx
	if idx >= len(m.events) {
		m.mu.Unlock()
		// Block until context is cancelled to simulate waiting.
		<-ctx.Done()
		return nil, "", ctx.Err()
	}
	ev := m.events[idx]
	m.readIdx++
	m.mu.Unlock()
	return ev.payload, ev.envelopeID, ev.err
}

func (m *mockSocketModeConn) Acknowledge(_ context.Context, envelopeID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.acked = append(m.acked, envelopeID)
	return nil
}

func (m *mockSocketModeConn) Connected() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.connected
}

func (m *mockSocketModeConn) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connected = false
	return nil
}

func (m *mockSocketModeConn) getAcked() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.acked...)
}

func TestParseConfig(t *testing.T) {
	raw := `{"appLevelToken":"xapp-test","botToken":"xoxb-test","defaultChannel":"C123","listenPort":9090,"retryAttempts":5}`

	cfg, err := parseConfig(raw)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.AppLevelToken != "xapp-test" {
		t.Errorf("AppLevelToken = %q, want xapp-test", cfg.AppLevelToken)
	}
	if cfg.BotToken != "xoxb-test" {
		t.Errorf("BotToken = %q, want xoxb-test", cfg.BotToken)
	}
	if cfg.DefaultChannel != "C123" {
		t.Errorf("DefaultChannel = %q, want C123", cfg.DefaultChannel)
	}
	if cfg.ListenPort != 9090 {
		t.Errorf("ListenPort = %d, want 9090", cfg.ListenPort)
	}
	if cfg.RetryAttempts != 5 {
		t.Errorf("RetryAttempts = %d, want 5", cfg.RetryAttempts)
	}
}

func TestParseConfig_Defaults(t *testing.T) {
	cfg, err := parseConfig("")
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.ListenPort != 8080 {
		t.Errorf("ListenPort = %d, want 8080", cfg.ListenPort)
	}
	if cfg.RetryAttempts != 3 {
		t.Errorf("RetryAttempts = %d, want 3", cfg.RetryAttempts)
	}
	if cfg.SlackAPIURL != "https://slack.com/api" {
		t.Errorf("SlackAPIURL = %q, want https://slack.com/api", cfg.SlackAPIURL)
	}
}

func TestParseConfig_EnvOverride(t *testing.T) {
	t.Setenv("SLACK_APP_TOKEN", "xapp-env")
	t.Setenv("SLACK_BOT_TOKEN", "xoxb-env")

	raw := `{"appLevelToken":"xapp-json","botToken":"xoxb-json"}`
	cfg, err := parseConfig(raw)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	// Env vars should take precedence.
	if cfg.AppLevelToken != "xapp-env" {
		t.Errorf("AppLevelToken = %q, want xapp-env (env override)", cfg.AppLevelToken)
	}
	if cfg.BotToken != "xoxb-env" {
		t.Errorf("BotToken = %q, want xoxb-env (env override)", cfg.BotToken)
	}
}

func TestOutboundPost_Success(t *testing.T) {
	var received []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		if r.Header.Get("Authorization") != "Bearer xoxb-test" {
			t.Error("missing or wrong Authorization header")
		}
		if r.Header.Get("Content-Type") != "application/json; charset=utf-8" {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	cfg := &slackConfig{
		BotToken:       "xoxb-test",
		DefaultChannel: "C999",
		RetryAttempts:  3,
		SlackAPIURL:    ts.URL,
	}
	poster := newSlackPoster(cfg)

	payload := json.RawMessage(`{"text":"Hello from the agent!"}`)
	err := poster.post(context.Background(), payload)
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	// Verify the request body sent to Slack API.
	var apiReq map[string]any
	if err := json.Unmarshal(received, &apiReq); err != nil {
		t.Fatalf("unmarshal received: %v", err)
	}
	if apiReq["channel"] != "C999" {
		t.Errorf("channel = %v, want C999", apiReq["channel"])
	}
	if apiReq["text"] != "Hello from the agent!" {
		t.Errorf("text = %v", apiReq["text"])
	}
}

func TestOutboundPost_WithExplicitChannel(t *testing.T) {
	var received []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	cfg := &slackConfig{
		BotToken:       "xoxb-test",
		DefaultChannel: "C999",
		RetryAttempts:  3,
		SlackAPIURL:    ts.URL,
	}
	poster := newSlackPoster(cfg)

	payload := json.RawMessage(`{"channel":"C1234567890","text":"specific channel"}`)
	err := poster.post(context.Background(), payload)
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	var apiReq map[string]any
	if err := json.Unmarshal(received, &apiReq); err != nil {
		t.Fatalf("unmarshal received: %v", err)
	}
	if apiReq["channel"] != "C1234567890" {
		t.Errorf("channel = %v, want C1234567890", apiReq["channel"])
	}
}

func TestOutboundPost_WithBlocks(t *testing.T) {
	var received []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	cfg := &slackConfig{
		BotToken:       "xoxb-test",
		DefaultChannel: "C999",
		RetryAttempts:  3,
		SlackAPIURL:    ts.URL,
	}
	poster := newSlackPoster(cfg)

	payload := json.RawMessage(`{"text":"fallback","blocks":[{"type":"section","text":{"type":"mrkdwn","text":"*Bold*"}}]}`)
	err := poster.post(context.Background(), payload)
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	var apiReq map[string]any
	if err := json.Unmarshal(received, &apiReq); err != nil {
		t.Fatalf("unmarshal received: %v", err)
	}
	if apiReq["blocks"] == nil {
		t.Error("expected blocks in API request")
	}
}

func TestOutboundPost_RateLimited(t *testing.T) {
	var attempts atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	cfg := &slackConfig{
		BotToken:       "xoxb-test",
		DefaultChannel: "C999",
		RetryAttempts:  3,
		SlackAPIURL:    ts.URL,
	}
	poster := newSlackPoster(cfg)

	payload := json.RawMessage(`{"text":"retry me"}`)
	err := poster.post(context.Background(), payload)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if got := attempts.Load(); got != 2 {
		t.Errorf("attempts = %d, want 2", got)
	}
}

func TestOutboundPost_SlackError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":false,"error":"channel_not_found"}`))
	}))
	defer ts.Close()

	cfg := &slackConfig{
		BotToken:       "xoxb-test",
		DefaultChannel: "C999",
		RetryAttempts:  1,
		SlackAPIURL:    ts.URL,
	}
	poster := newSlackPoster(cfg)

	payload := json.RawMessage(`{"text":"bad"}`)
	err := poster.post(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for Slack API error response")
	}
}

func TestOutboundPost_NoChannel(t *testing.T) {
	cfg := &slackConfig{
		BotToken:      "xoxb-test",
		RetryAttempts: 1,
		SlackAPIURL:   "http://unused",
	}
	poster := newSlackPoster(cfg)

	payload := json.RawMessage(`{"text":"no channel"}`)
	err := poster.post(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error when no channel specified")
	}
}

func TestInboundHandler(t *testing.T) {
	eventPayload := json.RawMessage(`{"envelope_id":"env1","type":"events_api","payload":{"event":{"type":"message","text":"hello"}}}`)

	mockConn := newMockSocketModeConn([]mockEvent{
		{payload: eventPayload, envelopeID: "env1"},
	})

	s := &mockSender{}
	logger := logr.Discard()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		runInboundLoop(ctx, mockConn, s, logger)
		close(done)
	}()

	// Wait for the event to be processed.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for inbound message")
		default:
		}
		sent := s.getSent()
		if len(sent) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	<-done

	sent := s.getSent()
	if len(sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(sent))
	}
	if string(sent[0]) != string(eventPayload) {
		t.Errorf("payload = %s, want %s", sent[0], eventPayload)
	}

	acked := mockConn.getAcked()
	if len(acked) != 1 || acked[0] != "env1" {
		t.Errorf("acked = %v, want [env1]", acked)
	}
}

func TestInboundHandler_NoEnvelopeID(t *testing.T) {
	// Events without envelope_id (e.g., hello message) should still forward.
	eventPayload := json.RawMessage(`{"type":"hello"}`)

	mockConn := newMockSocketModeConn([]mockEvent{
		{payload: eventPayload, envelopeID: ""},
	})

	s := &mockSender{}
	logger := logr.Discard()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		runInboundLoop(ctx, mockConn, s, logger)
		close(done)
	}()

	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for inbound message")
		default:
		}
		sent := s.getSent()
		if len(sent) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	<-done

	acked := mockConn.getAcked()
	if len(acked) != 0 {
		t.Errorf("acked = %v, want empty (no envelope_id)", acked)
	}
}

func TestInboundHandler_ReadError_Reconnects(t *testing.T) {
	eventPayload := json.RawMessage(`{"type":"events_api","payload":{}}`)

	mockConn := newMockSocketModeConn([]mockEvent{
		{err: fmt.Errorf("connection lost")},
		{payload: eventPayload, envelopeID: "env2"},
	})

	s := &mockSender{}
	logger := logr.Discard()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		runInboundLoop(ctx, mockConn, s, logger)
		close(done)
	}()

	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for reconnect and message")
		default:
		}
		sent := s.getSent()
		if len(sent) >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	<-done

	sent := s.getSent()
	if len(sent) != 1 {
		t.Fatalf("sent %d messages, want 1 (after reconnect)", len(sent))
	}
}

func TestHealthHandler_Healthy(t *testing.T) {
	h := newHealthHandler(func() bool { return true })

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if w.Body.String() != "ok" {
		t.Errorf("body = %q, want ok", w.Body.String())
	}
}

func TestHealthHandler_Unhealthy(t *testing.T) {
	h := newHealthHandler(func() bool { return false })

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
	if w.Body.String() != "unhealthy" {
		t.Errorf("body = %q, want unhealthy", w.Body.String())
	}
}
