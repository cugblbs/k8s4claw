package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	channel "github.com/Prismer-AI/k8s4claw/sdk/channel"
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

func TestInboundHandler_Success(t *testing.T) {
	sender := &mockSender{}
	h := newInboundHandler(sender, "")

	body := `{"event":"test"}`
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want %d", w.Code, http.StatusAccepted)
	}

	sent := sender.getSent()
	if len(sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(sent))
	}
	if string(sent[0]) != body {
		t.Errorf("payload = %s, want %s", sent[0], body)
	}
}

func TestInboundHandler_HMACVerification(t *testing.T) {
	secret := "test-secret"
	sender := &mockSender{}
	h := newInboundHandler(sender, secret)

	body := `{"event":"signed"}`
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Signature-256", sig)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want %d", w.Code, http.StatusAccepted)
	}
}

func TestInboundHandler_HMACRejection(t *testing.T) {
	secret := "test-secret"
	sender := &mockSender{}
	h := newInboundHandler(sender, secret)

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"bad":"sig"}`))
	req.Header.Set("X-Signature-256", "sha256=invalid")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestInboundHandler_WrongMethod(t *testing.T) {
	sender := &mockSender{}
	h := newInboundHandler(sender, "")

	req := httptest.NewRequest(http.MethodGet, "/webhook", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHealthHandler_Connected(t *testing.T) {
	h := newHealthHandler(func() bool { return true })

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestHealthHandler_Disconnected(t *testing.T) {
	h := newHealthHandler(func() bool { return false })

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}

func TestOutboundPoster(t *testing.T) {
	var received []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Error("missing Authorization header")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	cfg := &webhookConfig{
		TargetURL:     ts.URL,
		Headers:       map[string]string{"Authorization": "Bearer token"},
		RetryAttempts: 3,
	}
	poster := newOutboundPoster(cfg)

	err := poster.post(context.Background(), json.RawMessage(`{"msg":"hello"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	if string(received) != `{"msg":"hello"}` {
		t.Errorf("received = %s, want %s", received, `{"msg":"hello"}`)
	}
}

func TestParseConfig(t *testing.T) {
	raw := `{"listenPort":9090,"path":"/hook","targetURL":"https://example.com","secret":"s","headers":{"X-Key":"val"},"retryAttempts":5}`

	cfg, err := parseConfig(raw)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.ListenPort != 9090 {
		t.Errorf("ListenPort = %d, want 9090", cfg.ListenPort)
	}
	if cfg.Path != "/hook" {
		t.Errorf("Path = %q, want /hook", cfg.Path)
	}
	if cfg.TargetURL != "https://example.com" {
		t.Errorf("TargetURL = %q", cfg.TargetURL)
	}
	if cfg.Secret != "s" {
		t.Errorf("Secret = %q", cfg.Secret)
	}
	if cfg.Headers["X-Key"] != "val" {
		t.Errorf("Headers[X-Key] = %q", cfg.Headers["X-Key"])
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
	if cfg.Path != "/webhook" {
		t.Errorf("Path = %q, want /webhook", cfg.Path)
	}
	if cfg.RetryAttempts != 3 {
		t.Errorf("RetryAttempts = %d, want 3", cfg.RetryAttempts)
	}
}

// --- failingSender always returns an error ---

type failingSender struct{}

func (f *failingSender) Send(_ context.Context, _ json.RawMessage) error {
	return fmt.Errorf("send failed")
}

// --- runOutboundLoop tests ---

func TestRunOutboundLoop(t *testing.T) {
	var received atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	cfg := &webhookConfig{TargetURL: ts.URL, RetryAttempts: 1}
	poster := newOutboundPoster(cfg)

	ch := make(chan *channel.InboundMessage, 3)
	ch <- &channel.InboundMessage{ID: "1", Payload: json.RawMessage(`{"a":1}`)}
	ch <- &channel.InboundMessage{ID: "2", Payload: json.RawMessage(`{"a":2}`)}
	ch <- &channel.InboundMessage{ID: "3", Payload: json.RawMessage(`{"a":3}`)}
	close(ch)

	runOutboundLoop(context.Background(), ch, poster, logr.Discard())

	if received.Load() != 3 {
		t.Errorf("received %d messages, want 3", received.Load())
	}
}

func TestRunOutboundLoop_ContextCancel(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	cfg := &webhookConfig{TargetURL: ts.URL, RetryAttempts: 1}
	poster := newOutboundPoster(cfg)

	ch := make(chan *channel.InboundMessage)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		runOutboundLoop(ctx, ch, poster, logr.Discard())
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runOutboundLoop did not exit after context cancel")
	}
}

func TestRunOutboundLoop_ChannelClosed(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	cfg := &webhookConfig{TargetURL: ts.URL, RetryAttempts: 1}
	poster := newOutboundPoster(cfg)

	ch := make(chan *channel.InboundMessage)
	close(ch)

	done := make(chan struct{})
	go func() {
		runOutboundLoop(context.Background(), ch, poster, logr.Discard())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runOutboundLoop did not exit after channel close")
	}
}

// --- retrySleep tests ---

func TestRetrySleep_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := retrySleep(ctx, 0)
	if err == nil {
		t.Fatal("expected error from retrySleep with cancelled context")
	}
	if err != context.Canceled {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// --- outboundPoster retry tests ---

func TestOutboundPoster_Retry_TransientError(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	cfg := &webhookConfig{TargetURL: ts.URL, RetryAttempts: 3}
	poster := newOutboundPoster(cfg)

	err := poster.post(context.Background(), json.RawMessage(`{"retry":"test"}`))
	if err != nil {
		t.Fatalf("expected success after retry, got: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2", calls.Load())
	}
}

func TestOutboundPoster_AllRetriesExhausted(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer ts.Close()

	cfg := &webhookConfig{TargetURL: ts.URL, RetryAttempts: 2}
	poster := newOutboundPoster(cfg)

	err := poster.post(context.Background(), json.RawMessage(`{"fail":"always"}`))
	if err == nil {
		t.Fatal("expected error when all retries exhausted")
	}
	if !strings.Contains(err.Error(), "after 2 attempts") {
		t.Errorf("error = %q, want 'after 2 attempts'", err.Error())
	}
}

func TestOutboundPoster_ContextCancel(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer ts.Close()

	cfg := &webhookConfig{TargetURL: ts.URL, RetryAttempts: 5}
	poster := newOutboundPoster(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel right away so retry sleep is interrupted
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	err := poster.post(ctx, json.RawMessage(`{"cancel":"test"}`))
	if err == nil {
		t.Fatal("expected error when context cancelled")
	}
	if !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("error = %q, want to contain 'cancelled'", err.Error())
	}
}

// --- inboundHandler edge cases ---

func TestInboundHandler_SenderError(t *testing.T) {
	h := newInboundHandler(&failingSender{}, "")

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg":"test"}`))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}

func TestInboundHandler_BodyTooLarge(t *testing.T) {
	s := &mockSender{}
	h := newInboundHandler(s, "")

	// Create a body larger than maxRequestBody (16 MiB)
	largeBody := make([]byte, maxRequestBody+1)
	for i := range largeBody {
		largeBody[i] = 'a'
	}

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(largeBody))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d", w.Code, http.StatusRequestEntityTooLarge)
	}
}

// --- verifyHMAC edge cases ---

func TestVerifyHMAC_NoPrefix(t *testing.T) {
	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write([]byte("body"))
	sig := hex.EncodeToString(mac.Sum(nil)) // missing "sha256=" prefix

	if verifyHMAC([]byte("body"), sig, "secret") {
		t.Error("expected verifyHMAC to return false without sha256= prefix")
	}
}

func TestVerifyHMAC_InvalidHex(t *testing.T) {
	if verifyHMAC([]byte("body"), "sha256=zzzzzz", "secret") {
		t.Error("expected verifyHMAC to return false with invalid hex")
	}
}

func TestVerifyHMAC_EmptySignature(t *testing.T) {
	if verifyHMAC([]byte("body"), "", "secret") {
		t.Error("expected verifyHMAC to return false with empty signature")
	}
}

// --- parseConfig edge cases ---

func TestParseConfig_InvalidJSON(t *testing.T) {
	_, err := parseConfig("{invalid json")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "failed to parse CHANNEL_CONFIG") {
		t.Errorf("error = %q, want to contain 'failed to parse CHANNEL_CONFIG'", err.Error())
	}
}

// --- buildMux tests ---

func TestBuildMux_InboundMode(t *testing.T) {
	s := &mockSender{}
	cfg := &webhookConfig{Path: "/webhook", Secret: ""}
	mux := buildMux(cfg, "inbound", s, func() int { return 0 })

	// Verify inbound handler is registered
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"test":true}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Errorf("inbound handler status = %d, want %d", w.Code, http.StatusAccepted)
	}

	// Verify health handler
	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("health status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestBuildMux_BidirectionalMode(t *testing.T) {
	s := &mockSender{}
	cfg := &webhookConfig{Path: "/hook", Secret: "s3cret"}
	mux := buildMux(cfg, "bidirectional", s, func() int { return 0 })

	// Inbound handler should be registered at /hook
	req := httptest.NewRequest(http.MethodPost, "/hook", bytes.NewBufferString(`{"test":true}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	// Without HMAC it should be unauthorized
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestBuildMux_OutboundMode_NoInbound(t *testing.T) {
	s := &mockSender{}
	cfg := &webhookConfig{Path: "/webhook", Secret: ""}
	mux := buildMux(cfg, "outbound", s, func() int { return 0 })

	// Inbound handler should NOT be registered in outbound-only mode
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"test":true}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	// Should get 404 since inbound handler not registered
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d (no inbound handler in outbound mode)", w.Code, http.StatusNotFound)
	}
}

func TestBuildMux_HealthDisconnected(t *testing.T) {
	s := &mockSender{}
	cfg := &webhookConfig{Path: "/webhook"}
	mux := buildMux(cfg, "outbound", s, func() int { return 5 })

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("health status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}

func TestLoadConfig(t *testing.T) {
	t.Setenv("CHANNEL_CONFIG", `{"listenPort":9090}`)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.ListenPort != 9090 {
		t.Errorf("ListenPort = %d, want 9090", cfg.ListenPort)
	}
}

func TestNewLogger(t *testing.T) {
	logger, err := newLogger()
	if err != nil {
		t.Fatalf("newLogger: %v", err)
	}
	if logger.GetSink() == nil {
		t.Error("expected non-nil logger sink")
	}
}

func TestNeedsOutbound(t *testing.T) {
	tests := []struct {
		mode      string
		targetURL string
		want      bool
	}{
		{"outbound", "http://example.com", true},
		{"bidirectional", "http://example.com", true},
		{"inbound", "http://example.com", false},
		{"outbound", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		got := needsOutbound(tt.mode, tt.targetURL)
		if got != tt.want {
			t.Errorf("needsOutbound(%q, %q) = %v, want %v", tt.mode, tt.targetURL, got, tt.want)
		}
	}
}

func TestParseConfig_DefaultsAppliedAfterParse(t *testing.T) {
	// JSON with zero-value fields should get defaults applied
	raw := `{"targetURL":"https://example.com","listenPort":0,"path":"","retryAttempts":0}`
	cfg, err := parseConfig(raw)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.ListenPort != 8080 {
		t.Errorf("ListenPort = %d, want 8080", cfg.ListenPort)
	}
	if cfg.Path != "/webhook" {
		t.Errorf("Path = %q, want /webhook", cfg.Path)
	}
	if cfg.RetryAttempts != 3 {
		t.Errorf("RetryAttempts = %d, want 3", cfg.RetryAttempts)
	}
}

func TestOutboundPoster_Retry_HTTPClientError(t *testing.T) {
	// Use a URL that will cause an HTTP client error (connection refused)
	cfg := &webhookConfig{TargetURL: "http://127.0.0.1:1", RetryAttempts: 2}
	poster := newOutboundPoster(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel quickly so we don't wait through full retry sleeps
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	err := poster.post(ctx, json.RawMessage(`{"err":"test"}`))
	if err == nil {
		t.Fatal("expected error for connection refused")
	}
}

func TestOutboundPoster_Retry_HTTPClientErrorAllExhausted(t *testing.T) {
	// Target a port that refuses connections. With RetryAttempts=1, it will:
	// attempt 0: client.Do fails -> retrySleep(ctx, 0) -> sleep 1s -> continue -> loop ends
	// Then fall through to "failed after N attempts" error.
	cfg := &webhookConfig{TargetURL: "http://127.0.0.1:1", RetryAttempts: 1}
	poster := newOutboundPoster(cfg)

	err := poster.post(context.Background(), json.RawMessage(`{"exhaust":"client-err"}`))
	if err == nil {
		t.Fatal("expected error when all retries exhausted with client errors")
	}
	if !strings.Contains(err.Error(), "after 1 attempts") {
		t.Errorf("error = %q, want to contain 'after 1 attempts'", err.Error())
	}
}

func TestRunOutboundLoop_PostError(t *testing.T) {
	// Target that always returns 500, with 1 retry attempt so it fails fast
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	cfg := &webhookConfig{TargetURL: ts.URL, RetryAttempts: 1}
	poster := newOutboundPoster(cfg)

	ch := make(chan *channel.InboundMessage, 1)
	ch <- &channel.InboundMessage{ID: "err-1", Payload: json.RawMessage(`{"fail":"yes"}`)}
	close(ch)

	// Should log the error but not panic/hang
	runOutboundLoop(context.Background(), ch, poster, logr.Discard())
}

// --- run() tests ---

func TestRun_InboundMode(t *testing.T) {
	s := &mockSender{}
	cfg := &webhookConfig{
		ListenPort:    0, // let OS pick
		Path:          "/webhook",
		RetryAttempts: 1,
	}

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, cfg, "inbound", s, func() int { return 0 }, nil, logr.Discard())
	}()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not exit after context cancel")
	}
}

func TestRun_OutboundMode(t *testing.T) {
	var received atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	cfg := &webhookConfig{
		ListenPort:    0,
		Path:          "/webhook",
		TargetURL:     ts.URL,
		RetryAttempts: 1,
	}

	outCh := make(chan *channel.InboundMessage, 1)
	outCh <- &channel.InboundMessage{ID: "1", Payload: json.RawMessage(`{"out":true}`)}

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, cfg, "outbound", &mockSender{}, func() int { return 0 }, outCh, logr.Discard())
	}()

	// Wait for outbound message to be delivered
	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not exit after context cancel")
	}

	if received.Load() != 1 {
		t.Errorf("received %d messages, want 1", received.Load())
	}
}

func TestOutboundPoster_InvalidURL(t *testing.T) {
	// URL with control character causes NewRequestWithContext to fail
	cfg := &webhookConfig{TargetURL: "http://invalid\x00url", RetryAttempts: 1}
	poster := newOutboundPoster(cfg)

	err := poster.post(context.Background(), json.RawMessage(`{"test":true}`))
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
	if !strings.Contains(err.Error(), "failed to create request") {
		t.Errorf("error = %q, want to contain 'failed to create request'", err.Error())
	}
}

func TestRun_ListenError(t *testing.T) {
	cfg := &webhookConfig{
		ListenPort:    -1, // invalid port
		Path:          "/webhook",
		RetryAttempts: 1,
	}

	err := run(context.Background(), cfg, "inbound", &mockSender{}, func() int { return 0 }, nil, logr.Discard())
	if err == nil {
		t.Fatal("expected error for invalid listen port")
	}
	if !strings.Contains(err.Error(), "failed to listen") {
		t.Errorf("error = %q, want to contain 'failed to listen'", err.Error())
	}
}

func TestRun_ServeError(t *testing.T) {
	cfg := &webhookConfig{
		Path:          "/webhook",
		RetryAttempts: 1,
	}

	// Create and close a listener to trigger a Serve error
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	_ = ln.Close() // closed listener will cause Serve to fail

	runErr := run(context.Background(), cfg, "inbound", &mockSender{}, func() int { return 0 }, nil, logr.Discard(), runOpts{listener: ln})
	if runErr == nil {
		t.Fatal("expected error from Serve with closed listener")
	}
	if !strings.Contains(runErr.Error(), "HTTP server error") {
		t.Errorf("error = %q, want to contain 'HTTP server error'", runErr.Error())
	}
}

func TestRun_WithInjectedListener(t *testing.T) {
	s := &mockSender{}
	cfg := &webhookConfig{
		Path:          "/webhook",
		RetryAttempts: 1,
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, cfg, "inbound", s, func() int { return 0 }, nil, logr.Discard(), runOpts{listener: ln})
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case runErr := <-errCh:
		if runErr != nil {
			t.Fatalf("run returned error: %v", runErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not exit after context cancel")
	}
}

func TestRun_NilOutboundChannel(t *testing.T) {
	cfg := &webhookConfig{
		ListenPort:    0,
		Path:          "/webhook",
		TargetURL:     "http://example.com",
		RetryAttempts: 1,
	}

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		// nil outCh means no outbound loop started
		errCh <- run(ctx, cfg, "inbound", &mockSender{}, func() int { return 0 }, nil, logr.Discard())
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not exit after context cancel")
	}
}
