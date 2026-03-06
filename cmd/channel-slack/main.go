package main

import (
	"context"
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

func main() {
	zapLog, err := zap.NewProduction()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	logger := zapr.NewLogger(zapLog)

	configJSON := os.Getenv("CHANNEL_CONFIG")
	cfg, err := parseConfig(configJSON)
	if err != nil {
		logger.Error(err, "failed to parse config")
		os.Exit(1)
	}

	if cfg.BotToken == "" {
		logger.Error(fmt.Errorf("SLACK_BOT_TOKEN is required"), "missing bot token")
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	client, err := channel.Connect(ctx, channel.WithLogger(logger))
	if err != nil {
		logger.Error(err, "failed to connect to IPC Bus")
		os.Exit(1)
	}
	defer client.Close()

	mode := os.Getenv("CHANNEL_MODE")

	// Socket Mode connection for inbound.
	var smConn socketModeConn
	if mode == "inbound" || mode == "bidirectional" {
		if cfg.AppLevelToken == "" {
			logger.Error(fmt.Errorf("SLACK_APP_TOKEN is required for inbound mode"), "missing app-level token")
			os.Exit(1)
		}
		sm := newSlackSocketMode(cfg.AppLevelToken, cfg.SlackAPIURL)
		if err := sm.Connect(ctx); err != nil {
			logger.Error(err, "failed to connect Socket Mode")
			os.Exit(1)
		}
		defer sm.Close()
		smConn = sm

		go runInboundLoop(ctx, smConn, client, logger)
	}

	// Outbound: IPC Bus -> Slack.
	if mode == "outbound" || mode == "bidirectional" {
		poster := newSlackPoster(cfg)
		inCh, err := client.Receive(ctx)
		if err != nil {
			logger.Error(err, "failed to start receiving")
			os.Exit(1)
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
	srv := &http.Server{Addr: addr, Handler: mux}

	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		srv.Shutdown(shutdownCtx)
	}()

	logger.Info("slack sidecar starting", "addr", addr, "mode", mode)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		logger.Error(err, "HTTP server error")
		os.Exit(1)
	}
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
