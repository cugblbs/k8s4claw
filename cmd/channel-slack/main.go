package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"go.uber.org/zap"

	channel "github.com/Prismer-AI/k8s4claw/sdk/channel"
)

// channelClient abstracts the channel SDK client for testing.
type channelClient interface {
	Send(ctx context.Context, payload json.RawMessage) error
	Receive(ctx context.Context) (<-chan *channel.InboundMessage, error)
	BufferedCount() int
	Close() error
}

func main() {
	if err := mainRun(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func mainRun() error {
	zapLog, err := zap.NewProduction()
	if err != nil {
		return fmt.Errorf("failed to initialize logger: %v", err)
	}
	logger := zapr.NewLogger(zapLog)

	configJSON := os.Getenv("CHANNEL_CONFIG")
	cfg, err := parseConfig(configJSON)
	if err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	if cfg.BotToken == "" {
		return fmt.Errorf("SLACK_BOT_TOKEN is required")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	client, err := channel.Connect(ctx, channel.WithLogger(logger))
	if err != nil {
		return fmt.Errorf("failed to connect to IPC Bus: %w", err)
	}
	defer func() { _ = client.Close() }()

	mode := os.Getenv("CHANNEL_MODE")

	return run(ctx, cfg, client, mode, logger)
}

func run(ctx context.Context, cfg *slackConfig, client channelClient, mode string, logger logr.Logger) error {
	// Socket Mode connection for inbound.
	var smConn socketModeConn
	if mode == "inbound" || mode == "bidirectional" {
		if cfg.AppLevelToken == "" {
			return fmt.Errorf("SLACK_APP_TOKEN is required for inbound mode")
		}
		sm := newSlackSocketMode(cfg.AppLevelToken, cfg.SlackAPIURL)
		if err := sm.Connect(ctx); err != nil {
			return fmt.Errorf("failed to connect Socket Mode: %w", err)
		}
		defer func() { _ = sm.Close() }()
		smConn = sm

		go runInboundLoop(ctx, smConn, client, logger)
	}

	// Outbound: IPC Bus -> Slack.
	if mode == "outbound" || mode == "bidirectional" {
		poster := newSlackPoster(cfg)
		inCh, err := client.Receive(ctx)
		if err != nil {
			return fmt.Errorf("failed to start receiving: %w", err)
		}
		go runOutboundLoop(ctx, inCh, poster, logger)
	}

	// Health check.
	mux := http.NewServeMux()
	mux.Handle("/healthz", newHealthHandler(func() bool {
		if smConn != nil && !smConn.Connected() {
			return false
		}
		return client.BufferedCount() == 0
	}))

	addr := fmt.Sprintf(":%d", cfg.ListenPort)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	logger.Info("slack sidecar starting", "addr", addr, "mode", mode)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return fmt.Errorf("HTTP server error: %w", err)
	}
	return nil
}

func runOutboundLoop(ctx context.Context, ch <-chan *channel.InboundMessage, poster *slackPoster, logger logr.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if err := poster.post(ctx, msg.Payload); err != nil {
				logger.Error(err, "outbound post failed", "msgID", msg.ID)
			}
		}
	}
}
