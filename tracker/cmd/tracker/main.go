// Package: main
// Feature: F-007 (Centralized Tracker)
// Story: US-007-01, US-007-02, US-007-03, US-007-06
// Purpose: Entry point for the tracker HTTP server

package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/stonkagents/agent/tracker/internal/db"
)

// Version is the build version, can be overridden via ldflags:
// go build -ldflags "-X main.Version=1.0.0" ./tracker/cmd/tracker
var Version = "dev"

const (
	defaultAddr        = ":7842"
	shutdownTimeout    = 10 * time.Second
	defaultPresenceTTL = 5 * time.Minute
)

// loadEnvFromFile reads KEY=VALUE lines from filename and sets env vars (only if not already set).
func loadEnvFromFile(filename string) {
	f, err := os.Open(filename)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.Index(line, "=")
		if i <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:i])
		val := strings.TrimSpace(line[i+1:])
		if len(key) == 0 {
			continue
		}
		if val != "" && (strings.HasPrefix(val, `"`) || strings.HasPrefix(val, "'")) {
			val = strings.Trim(val, `"'`)
		}
		if os.Getenv(key) == "" {
			_ = os.Setenv(key, val)
		}
	}
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	loadEnvFromFile(".env")

	secrets, err := loadTrackerSecrets()
	if err != nil {
		log.Fatalf("Secret configuration error: %v", err)
	}
	fmt.Printf("STONKAGENTS_ENV=%s\n", secrets.Env)

	addr := defaultAddr
	if envAddr := os.Getenv("TRACKER_ADDR"); envAddr != "" {
		addr = envAddr
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatalf("DATABASE_URL is required.")
	}
	pgPool, err := db.Connect()
	if err != nil {
		log.Fatalf("Postgres connect: %v", err)
	}
	defer pgPool.Close()
	if err := db.RunMigrations(databaseURL); err != nil {
		log.Fatalf("Migrations: %v", err)
	}

	jobCtx, jobCancel := context.WithCancel(context.Background())

	res, err := Bootstrap(BootstrapConfig{
		Addr:           addr,
		PgPool:         pgPool,
		Secrets:        secrets,
		Version:        Version,
		PresenceTTL:    defaultPresenceTTL,
		ShutdownCtx:    jobCtx,
		ShutdownCancel: jobCancel,
	})
	if err != nil {
		log.Fatalf("Bootstrap error: %v", err)
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)

	go func() {
		fmt.Printf("StonkAgents Tracker %s listening on %s\n", Version, addr)
		if err := res.Server.Start(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	<-done
	fmt.Println("\nShutting down tracker...")

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := res.Server.Shutdown(ctx); err != nil {
		log.Fatalf("Shutdown error: %v", err)
	}
	if res.RelayHost != nil {
		if err := res.RelayHost.Close(); err != nil {
			slog.Error("Relay host close error", "error", err)
		}
	}
	if res.Redis != nil {
		if err := res.Redis.Close(); err != nil {
			slog.Error("Redis close error", "error", err)
		}
	}
	res.RecalcDone()
	res.JobDone()
	fmt.Println("Tracker stopped.")
}
