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

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	client, err := channel.Connect(ctx, channel.WithLogger(logger))
	if err != nil {
		logger.Error(err, "failed to connect to IPC Bus")
		os.Exit(1)
	}
	defer client.Close()

	mux := http.NewServeMux()

	// Inbound: HTTP -> IPC Bus.
	mode := os.Getenv("CHANNEL_MODE")
	if mode == "inbound" || mode == "bidirectional" {
		mux.Handle(cfg.Path, newInboundHandler(client, cfg.Secret))
	}

	// Health check.
	mux.Handle("/healthz", newHealthHandler(func() bool {
		return client.BufferedCount() == 0
	}))

	// Outbound: IPC Bus -> HTTP.
	if (mode == "outbound" || mode == "bidirectional") && cfg.TargetURL != "" {
		poster := newOutboundPoster(cfg)
		inCh, err := client.Receive(ctx)
		if err != nil {
			logger.Error(err, "failed to start receiving")
			os.Exit(1)
		}
		go runOutboundLoop(ctx, inCh, poster, logger)
	}

	addr := fmt.Sprintf(":%d", cfg.ListenPort)
	srv := &http.Server{Addr: addr, Handler: mux}

	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		srv.Shutdown(shutdownCtx)
	}()

	logger.Info("webhook sidecar starting", "addr", addr, "mode", mode)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		logger.Error(err, "HTTP server error")
		os.Exit(1)
	}
}

func runOutboundLoop(ctx context.Context, ch <-chan *channel.InboundMessage, poster *outboundPoster, logger logr.Logger) {
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
