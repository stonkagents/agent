// Package: cmd/daemon
// Feature: F-010 (Go Core Daemon)
// Story: US-010-01 (HTTP Server with Health Endpoint)
// Purpose: Daemon entry point

package main

import (
	"fmt"
	"os"

	"github.com/stonkagents/agent/internal/daemon"
	"github.com/stonkagents/agent/internal/logger"
)

// Version is the build version, can be overridden via ldflags:
// go build -ldflags "-X main.Version=1.0.0" ./cmd/daemon
var Version = "dev"

func main() {
	// Load daemon config
	cfg, err := daemon.LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		fmt.Fprintln(os.Stderr, "No StonkAgents config found: run the installer, or create config.yaml first")
		os.Exit(1)
	}
	cfg.Version = Version

	// Initialize logger
	log, err := logger.New(logger.Config{
		LogPath:    cfg.DaemonLogPath(),
		MaxSize:    cfg.LogMaxSize,
		MaxBackups: cfg.LogBackups,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	defer log.Close()

	// Log startup
	log.Info("Daemon", "main", "StonkAgents daemon starting...", map[string]interface{}{
		"version": Version,
		"peer_id": cfg.PeerID,
	})

	// Create server with full config (includes keys, data dir, tracker URL)
	server := daemon.NewServerWithLogger(cfg, log)

	// Initialize managers and P2P
	if err := server.InitializeManagers(); err != nil {
		log.Error("Daemon", "main", fmt.Sprintf("Failed to initialize managers: %v", err), nil)
		fmt.Fprintf(os.Stderr, "Failed to initialize managers: %v\n", err)
		os.Exit(1)
	}
	if err := server.StartP2P(); err != nil {
		log.Error("Daemon", "main", fmt.Sprintf("Failed to start P2P: %v", err), nil)
		fmt.Fprintf(os.Stderr, "Failed to start P2P: %v\n", err)
		os.Exit(1)
	}
	if err := server.WireP2PDownloads(); err != nil {
		log.Error("Daemon", "main", fmt.Sprintf("Failed to wire P2P downloads: %v", err), nil)
		fmt.Fprintf(os.Stderr, "Failed to wire P2P downloads: %v\n", err)
		os.Exit(1)
	}

	if err := server.RegisterWithTracker(); err != nil {
		log.Error("Daemon", "main", fmt.Sprintf("Failed to register with tracker: %v", err), nil)
	}
	server.StartTrackerHeartbeat()

	// Start HTTP server
	if err := server.Start(); err != nil {
		log.Error("Daemon", "main", fmt.Sprintf("Failed to start server: %v", err), nil)
		fmt.Fprintf(os.Stderr, "Failed to start server: %v\n", err)
		os.Exit(1)
	}

	// Wait for shutdown signal (SIGTERM/SIGINT)
	log.Info("Daemon", "main", "Daemon ready. Press Ctrl+C to stop.", nil)
	server.WaitForShutdown()

	log.Info("Daemon", "main", "Daemon stopped", nil)
}
