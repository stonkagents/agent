// Package: internal/daemon
// Feature: F-010 (Go Core Daemon)
// Purpose: Sprint 2 integration tests - HTTP + P2P concurrent operation
//
// WebSocket was removed; P2P/DHT is used for peer discovery and data flow.
// These tests validate concurrent operation of HTTP server and P2P host.

package daemon

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stonkagents/agent/internal/logger"
)

// TestIntegration_Sprint2_HTTPServerAndP2PHostConcurrent verifies that
// HTTP server and P2P host can run concurrently without conflicts.
func TestIntegration_Sprint2_HTTPServerAndP2PHostConcurrent(t *testing.T) {
	config := &Config{
		Host: "127.0.0.1",
		Port: 17850,
	}
	logConfig := logger.Config{}
	log, _ := logger.New(logConfig)
	server := NewServerWithLogger(config, log)

	if err := server.Start(); err != nil {
		t.Fatalf("Failed to start HTTP server: %v", err)
	}
	defer server.Shutdown()
	if err := server.StartP2P(); err != nil {
		t.Fatalf("Failed to start P2P host: %v", err)
	}

	// Verify HTTP server is still accessible
	resp, err := http.Get("http://127.0.0.1:17850/health")
	if err != nil {
		t.Errorf("HTTP server not accessible after P2P initialization: %v", err)
	}
	if resp != nil {
		resp.Body.Close()
	}

	// Verify P2P host is running
	peerID := server.GetPeerID()
	if peerID == "" {
		t.Error("P2P host not initialized")
	}

	// Verify both are still running after 100ms
	time.Sleep(100 * time.Millisecond)

	resp2, err := http.Get("http://127.0.0.1:17850/health")
	if err != nil {
		t.Errorf("HTTP server crashed after concurrent operation: %v", err)
	}
	if resp2 != nil {
		resp2.Body.Close()
	}

	peerID2 := server.GetPeerID()
	if peerID2 == "" || peerID2 != peerID {
		t.Error("P2P host crashed or changed during concurrent operation")
	}
}

// TestIntegration_Sprint2_AllComponentsConcurrent runs HTTP + P2P together,
// simulating real daemon operation (WebSocket removed; DHT used for P2P).
func TestIntegration_Sprint2_AllComponentsConcurrent(t *testing.T) {
	config := &Config{
		Host: "127.0.0.1",
		Port: 17852,
	}
	logConfig := logger.Config{}
	log, _ := logger.New(logConfig)
	server := NewServerWithLogger(config, log)

	if err := server.Start(); err != nil {
		t.Fatalf("Failed to start HTTP server: %v", err)
	}
	defer server.Shutdown()
	if err := server.StartP2P(); err != nil {
		t.Fatalf("Failed to start P2P: %v", err)
	}

	// Simulate concurrent activity on HTTP and P2P
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	// HTTP requests
	httpDone := make(chan bool)
	go func() {
		for {
			select {
			case <-ctx.Done():
				httpDone <- true
				return
			default:
				resp, err := http.Get("http://127.0.0.1:17852/health")
				if err != nil {
					t.Errorf("HTTP request failed during concurrent operation: %v", err)
					httpDone <- false
					return
				}
				resp.Body.Close()
				time.Sleep(50 * time.Millisecond)
			}
		}
	}()

	// P2P activity (check peer ID)
	p2pDone := make(chan bool)
	go func() {
		initialPeerID := server.GetPeerID()
		for {
			select {
			case <-ctx.Done():
				p2pDone <- true
				return
			default:
				peerID := server.GetPeerID()
				if peerID == "" || peerID != initialPeerID {
					t.Error("P2P host became unstable during concurrent operation")
					p2pDone <- false
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
		}
	}()

	httpOK := <-httpDone
	p2pOK := <-p2pDone

	if !httpOK {
		t.Error("HTTP component failed during concurrent operation")
	}
	if !p2pOK {
		t.Error("P2P component failed during concurrent operation")
	}

	// Final health check
	resp, err := http.Get("http://127.0.0.1:17852/health")
	if err != nil {
		t.Fatalf("Final health check failed: %v", err)
	}
	resp.Body.Close()

	finalPeerID := server.GetPeerID()
	if finalPeerID == "" {
		t.Error("P2P host not available after concurrent stress test")
	}
}

// TestIntegration_Sprint2_PortConflictDetection verifies that
// P2P host doesn't conflict with HTTP server port.
func TestIntegration_Sprint2_PortConflictDetection(t *testing.T) {
	config := &Config{
		Host: "127.0.0.1",
		Port: 17853,
	}
	logConfig := logger.Config{}
	log, _ := logger.New(logConfig)
	server := NewServerWithLogger(config, log)

	if err := server.Start(); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer server.Shutdown()
	if err := server.StartP2P(); err != nil {
		t.Fatalf("Failed to start P2P: %v", err)
	}

	// Verify HTTP server is still on original port
	resp, err := http.Get("http://127.0.0.1:17853/health")
	if err != nil {
		t.Errorf("HTTP server lost its port after P2P initialization: %v", err)
	}
	if resp != nil {
		resp.Body.Close()
	}

	// Get P2P addresses
	if server.p2pHost == nil {
		t.Fatal("P2P host not initialized")
	}

	p2pAddrs := server.p2pHost.Addrs()
	if len(p2pAddrs) == 0 {
		t.Error("P2P host has no listen addresses")
	}

	// Verify P2P is not using HTTP port
	for _, addr := range p2pAddrs {
		if addr == "/ip4/127.0.0.1/tcp/17853" {
			t.Errorf("P2P host is using HTTP server port (conflict): %s", addr)
		}
	}
}

// TestIntegration_Sprint2_ContextManagement verifies that
// server shutdown properly cancels all component contexts.
func TestIntegration_Sprint2_ContextManagement(t *testing.T) {
	config := &Config{
		Host: "127.0.0.1",
		Port: 17854,
	}
	logConfig := logger.Config{}
	log, _ := logger.New(logConfig)
	server := NewServerWithLogger(config, log)

	if err := server.Start(); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	if err := server.StartP2P(); err != nil {
		t.Fatalf("Failed to start P2P: %v", err)
	}

	// Verify components are running
	if server.p2pHost == nil {
		t.Fatal("P2P host not initialized")
	}

	peerIDBefore := server.GetPeerID()
	if peerIDBefore == "" {
		t.Error("P2P not running before shutdown")
	}

	// Shutdown server
	server.Shutdown()

	// Wait for graceful shutdown
	time.Sleep(100 * time.Millisecond)

	_, err := http.Get("http://127.0.0.1:17854/health")
	if err == nil {
		t.Error("HTTP server still running after shutdown (context not cancelled)")
	}
}
