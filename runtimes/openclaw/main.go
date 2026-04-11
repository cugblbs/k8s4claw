package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	port := envOr("CLAW_GATEWAY_PORT", "18900")
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	baseURL := os.Getenv("ANTHROPIC_BASE_URL")
	model := envOr("OPENCLAW_MODEL", "claude-sonnet-4-20250514")
	systemPrompt := envOr("OPENCLAW_SYSTEM_PROMPT", "You are a helpful team assistant.")
	mock := envOr("OPENCLAW_MODE", "") == "mock" || apiKey == ""

	var handler *handler
	if mock {
		slog.Info("starting in mock mode (no Claude API calls)")
		handler = newMockHandler(model, systemPrompt)
	} else {
		handler = newHandler(apiKey, model, systemPrompt, baseURL)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ready")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "websocket" {
			http.NotFound(w, r)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			slog.Error("websocket accept failed", "error", err)
			return
		}
		slog.Info("IPC bus bridge connected")
		handler.serve(r.Context(), conn)
	})

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		<-sigCh
		slog.Info("shutting down")
		shutdownCtx, done := context.WithTimeout(ctx, 10*time.Second)
		defer done()
		srv.Shutdown(shutdownCtx)
	}()

	slog.Info("openclaw runtime starting", "port", port, "model", model, "mock", mock)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
