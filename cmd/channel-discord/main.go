package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"go.uber.org/zap"

	channel "github.com/Prismer-AI/k8s4claw/sdk/channel"
)

const (
	defaultListenPort           = 8080
	defaultDiscordBusSocketPath = "/var/run/claw/bus.sock"
	discordMessageContentIntent = 1 << 15
)

var (
	newLoggerFn  = newLogger
	connectBusFn = func(ctx context.Context, logger logr.Logger) (channelClient, error) {
		socketPath := os.Getenv("IPC_SOCKET_PATH")
		if socketPath == "" {
			socketPath = defaultDiscordBusSocketPath
		}
		return channel.Connect(ctx, channel.WithLogger(logger), channel.WithSocketPath(socketPath))
	}
	newDiscordSessionFn = newDiscordSession
	runFn               = run
)

// channelClient abstracts the channel SDK client for testing.
type channelClient interface {
	Send(ctx context.Context, payload json.RawMessage) error
	Receive(ctx context.Context) (<-chan *channel.InboundMessage, error)
	BufferedCount() int
	Close() error
}

// sender abstracts the channel SDK Send method for testing.
type sender interface {
	Send(ctx context.Context, payload json.RawMessage) error
}

// discordSession abstracts the Discord session for testing.
type discordSession interface {
	AddHandler(handler interface{}) func()
	Open() error
	Close() error
	ChannelMessageSend(channelID, content string, options ...discordgo.RequestOption) (*discordgo.Message, error)
}

type discordConfig struct {
	ChannelID  string `json:"channelId,omitempty"`
	ListenPort int    `json:"listenPort,omitempty"`
}

type inboundRuntimeMessage struct {
	Text   string `json:"text"`
	User   string `json:"user,omitempty"`
	Thread string `json:"thread,omitempty"`
}

type outboundRuntimeMessage struct {
	Channel string `json:"channel,omitempty"`
	Thread  string `json:"thread,omitempty"`
	Text    string `json:"text"`
}

type channelState struct {
	mu        sync.RWMutex
	channelID string
}

func newChannelState(channelID string) *channelState {
	return &channelState{channelID: channelID}
}

func (s *channelState) Set(channelID string) {
	if channelID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.channelID = channelID
}

func (s *channelState) Get() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.channelID
}

func parseConfig(raw string) (*discordConfig, error) {
	cfg := &discordConfig{
		ListenPort: defaultListenPort,
	}
	if raw == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(raw), cfg); err != nil {
		return nil, fmt.Errorf("failed to parse CHANNEL_CONFIG: %w", err)
	}
	if cfg.ListenPort == 0 {
		cfg.ListenPort = defaultListenPort
	}
	return cfg, nil
}

func newLogger() (logr.Logger, error) {
	zapLog, err := zap.NewProduction()
	if err != nil {
		return logr.Logger{}, fmt.Errorf("failed to initialize logger: %w", err)
	}
	return zapr.NewLogger(zapLog), nil
}

func newDiscordSession(token string) (discordSession, error) {
	session, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, fmt.Errorf("failed to create Discord session: %w", err)
	}
	session.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordMessageContentIntent
	return session, nil
}

func main() {
	if err := mainRun(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func mainRun() error {
	logger, err := newLoggerFn()
	if err != nil {
		return err
	}

	cfg, err := parseConfig(os.Getenv("CHANNEL_CONFIG"))
	if err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	token := os.Getenv("DISCORD_TOKEN")
	if token == "" {
		return fmt.Errorf("DISCORD_TOKEN is required")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	client, err := connectBusFn(ctx, logger)
	if err != nil {
		return fmt.Errorf("failed to connect to IPC Bus: %w", err)
	}
	defer func() { _ = client.Close() }()

	session, err := newDiscordSessionFn(token)
	if err != nil {
		return err
	}

	return runFn(ctx, cfg, client, session, os.Getenv("CHANNEL_MODE"), logger)
}

type runOpts struct {
	listener net.Listener
}

func run(ctx context.Context, cfg *discordConfig, client channelClient, session discordSession, mode string, logger logr.Logger, opts ...runOpts) error {
	defer func() { _ = session.Close() }()

	tracker := newChannelState(cfg.ChannelID)
	var gatewayConnected atomic.Bool

	if needsGateway(mode) {
		removeHandler := session.AddHandler(newDiscordMessageHandler(ctx, client, cfg, tracker, logger))
		defer func() {
			if removeHandler != nil {
				removeHandler()
			}
		}()

		if err := session.Open(); err != nil {
			return fmt.Errorf("failed to open Discord session: %w", err)
		}
		gatewayConnected.Store(true)
	}

	if needsOutbound(mode) {
		inCh, err := client.Receive(ctx)
		if err != nil {
			return fmt.Errorf("failed to start receiving: %w", err)
		}
		poster := newDiscordPoster(session, cfg, tracker)
		go runOutboundLoop(ctx, inCh, poster, logger)
	}

	mux := http.NewServeMux()
	mux.Handle("/healthz", newHealthHandler(func() bool {
		if client.BufferedCount() != 0 {
			return false
		}
		if needsGateway(mode) {
			return gatewayConnected.Load()
		}
		return true
	}))

	var ln net.Listener
	if len(opts) > 0 && opts[0].listener != nil {
		ln = opts[0].listener
	} else {
		var err error
		ln, err = net.Listen("tcp", fmt.Sprintf(":%d", cfg.ListenPort))
		if err != nil {
			return fmt.Errorf("failed to listen: %w", err)
		}
	}

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	go func() { //nolint:gosec // G118: graceful shutdown intentionally uses a background context
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	logger.Info("discord sidecar starting", "addr", ln.Addr().String(), "mode", mode)
	if err := srv.Serve(ln); err != http.ErrServerClosed {
		return fmt.Errorf("HTTP server error: %w", err)
	}
	return nil
}

func needsGateway(mode string) bool {
	return mode == "inbound" || mode == "bidirectional"
}

func needsOutbound(mode string) bool {
	return mode == "outbound" || mode == "bidirectional"
}

func newDiscordMessageHandler(ctx context.Context, s sender, cfg *discordConfig, tracker *channelState, logger logr.Logger) func(*discordgo.Session, *discordgo.MessageCreate) {
	return func(_ *discordgo.Session, msg *discordgo.MessageCreate) {
		if msg == nil || msg.Author == nil {
			return
		}
		if msg.Author.Bot {
			return
		}
		if cfg.ChannelID != "" && msg.ChannelID != cfg.ChannelID {
			return
		}

		text := strings.TrimSpace(msg.Content)
		if text == "" {
			return
		}

		tracker.Set(msg.ChannelID)

		payload, err := json.Marshal(inboundRuntimeMessage{
			Text:   text,
			User:   discordDisplayName(msg),
			Thread: msg.ChannelID,
		})
		if err != nil {
			logger.Error(err, "failed to marshal Discord message", "messageID", msg.ID)
			return
		}

		if err := s.Send(ctx, json.RawMessage(payload)); err != nil {
			logger.Error(err, "failed to forward Discord message", "messageID", msg.ID, "channelID", msg.ChannelID)
		}
	}
}

func discordDisplayName(msg *discordgo.MessageCreate) string {
	if msg == nil {
		return ""
	}
	if msg.Member != nil && msg.Member.Nick != "" {
		return msg.Member.Nick
	}
	if msg.Author != nil {
		return msg.Author.Username
	}
	return ""
}

type discordPoster struct {
	session discordSession
	cfg     *discordConfig
	tracker *channelState
}

func newDiscordPoster(session discordSession, cfg *discordConfig, tracker *channelState) *discordPoster {
	return &discordPoster{
		session: session,
		cfg:     cfg,
		tracker: tracker,
	}
}

func (p *discordPoster) post(_ context.Context, payload json.RawMessage) error {
	var msg outboundRuntimeMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		return fmt.Errorf("failed to parse outbound message: %w", err)
	}
	if strings.TrimSpace(msg.Text) == "" {
		return fmt.Errorf("empty outbound text")
	}

	channelID := firstNonEmpty(msg.Thread, msg.Channel, p.cfg.ChannelID, p.tracker.Get())
	if channelID == "" {
		return fmt.Errorf("no Discord channel configured and no inbound channel observed")
	}

	if _, err := p.session.ChannelMessageSend(channelID, msg.Text); err != nil {
		return fmt.Errorf("failed to send Discord message: %w", err)
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func runOutboundLoop(ctx context.Context, ch <-chan *channel.InboundMessage, poster *discordPoster, logger logr.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if err := poster.post(ctx, msg.Payload); err != nil {
				logger.Error(err, "outbound Discord post failed", "msgID", msg.ID)
			}
		}
	}
}

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
		return
	}

	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte("unhealthy"))
}
