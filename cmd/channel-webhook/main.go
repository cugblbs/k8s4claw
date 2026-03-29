package main

import (
	"context"
	"fmt"
	"net"
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

func newLogger() (logr.Logger, error) {
	zapLog, err := zap.NewProduction()
	if err != nil {
		return logr.Logger{}, fmt.Errorf("failed to initialize logger: %w", err)
	}
	return zapr.NewLogger(zapLog), nil
}

// loadConfig reads CHANNEL_CONFIG from the environment and parses it.
func loadConfig() (*webhookConfig, error) {
	return parseConfig(os.Getenv("CHANNEL_CONFIG"))
}

func main() {
	if err := mainRun(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func mainRun() error {
	logger, err := newLogger()
	if err != nil {
		return err
	}

	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	client, err := channel.Connect(ctx, channel.WithLogger(logger))
	if err != nil {
		return fmt.Errorf("failed to connect to IPC Bus: %w", err)
	}
	defer func() { _ = client.Close() }()

	mode := os.Getenv("CHANNEL_MODE")

	var outCh <-chan *channel.InboundMessage
	if needsOutbound(mode, cfg.TargetURL) {
		outCh, err = client.Receive(ctx)
		if err != nil {
			return fmt.Errorf("failed to start receiving: %w", err)
		}
	}

	return run(ctx, cfg, mode, client, client.BufferedCount, outCh, logger)
}

// needsOutbound returns true if the mode requires outbound posting.
func needsOutbound(mode string, targetURL string) bool {
	return (mode == "outbound" || mode == "bidirectional") && targetURL != ""
}

// runOpts holds optional parameters for run().
type runOpts struct {
	listener net.Listener
}

func run(ctx context.Context, cfg *webhookConfig, mode string, s sender, bufferedCount func() int, outCh <-chan *channel.InboundMessage, logger logr.Logger, opts ...runOpts) error {
	mux := buildMux(cfg, mode, s, bufferedCount)

	if outCh != nil {
		poster := newOutboundPoster(cfg)
		go runOutboundLoop(ctx, outCh, poster, logger)
	}

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

	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	logger.Info("webhook sidecar starting", "addr", ln.Addr().String(), "mode", mode)
	if err := srv.Serve(ln); err != http.ErrServerClosed {
		return fmt.Errorf("HTTP server error: %w", err)
	}
	return nil
}

// buildMux creates the HTTP mux with inbound and health handlers.
func buildMux(cfg *webhookConfig, mode string, s sender, bufferedCount func() int) *http.ServeMux {
	mux := http.NewServeMux()

	if mode == "inbound" || mode == "bidirectional" {
		mux.Handle(cfg.Path, newInboundHandler(s, cfg.Secret))
	}

	mux.Handle("/healthz", newHealthHandler(func() bool {
		return bufferedCount() == 0
	}))

	return mux
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
