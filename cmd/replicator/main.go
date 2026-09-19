package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/stonkagents/agent/internal/replicator"
)

func newReplicatorLogger() *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(strings.TrimSpace(os.Getenv("REPLICATOR_LOG_LEVEL"))) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	switch strings.ToLower(strings.TrimSpace(os.Getenv("REPLICATOR_LOG_FORMAT"))) {
	case "text", "console":
		h = slog.NewTextHandler(os.Stdout, opts)
	default:
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(h)
}

func main() {
	cfg := replicator.LoadConfigFromEnv()
	logger := newReplicatorLogger()
	service, err := replicator.NewService(cfg, logger)
	if err != nil {
		log.Fatalf("replicator init failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	if err := service.Run(ctx); err != nil && err != context.Canceled {
		log.Fatalf("replicator run failed: %v", err)
	}
}
