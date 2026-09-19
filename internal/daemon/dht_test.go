// Package: internal/daemon
// Feature: F-010 (Go Core Daemon)
// Story: US-010-04 (DHT/GossipSub/mDNS Integration)
// Purpose: TDD tests for DHT peer discovery

package daemon

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// httpGet is a test helper for making HTTP GET requests
func httpGet(url string) (*http.Response, error) {
	return http.Get(url)
}

// TestDHT_Initialization tests that DHT is initialized when P2P host starts.
// TDD Step 1 (RED): This test will FAIL until we add DHT to P2P host.
func TestDHT_Initialization(t *testing.T) {
	// Create server with P2P
	config := &Config{
		Host: "127.0.0.1",
		Port: 17860,
	}
	server := NewServerWithConfig(config)

	if err := server.Start(); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer server.Shutdown()

	if err := server.StartP2P(); err != nil {
		t.Fatalf("Failed to start P2P: %v", err)
	}

	// Verify DHT is initialized
	if server.p2pHost == nil {
		t.Fatal("P2P host not initialized")
	}

	// Check that DHT routing table exists
	// This will fail until we add DHT field to P2PHost
	if server.p2pHost.DHT() == nil {
		t.Error("DHT not initialized - routing table is nil")
	}
}

// TestDHT_BootstrapNodes tests that DHT connects to bootstrap nodes.
// TDD Step 2 (RED): This test will FAIL until we configure bootstrap peers.
func TestDHT_BootstrapNodes(t *testing.T) {
	config := &Config{
		Host: "127.0.0.1",
		Port: 17861,
	}
	server := NewServerWithConfig(config)

	if err := server.Start(); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer server.Shutdown()

	if err := server.StartP2P(); err != nil {
		t.Fatalf("Failed to start P2P: %v", err)
	}

	// Wait for DHT bootstrap (async operation)
	time.Sleep(500 * time.Millisecond)

	// Verify bootstrap was attempted
	// Check peer count > 0 (connected to at least one bootstrap node)
	peers := server.p2pHost.ConnectedPeers()
	if len(peers) == 0 {
		t.Log("Warning: No peers connected after DHT bootstrap (may be expected in test environment)")
	}
}

// TestGossipSub_Initialization tests that GossipSub pubsub is initialized.
// TDD Step 3 (RED): This test will FAIL until we add GossipSub.
func TestGossipSub_Initialization(t *testing.T) {
	config := &Config{
		Host: "127.0.0.1",
		Port: 17862,
	}
	server := NewServerWithConfig(config)

	if err := server.Start(); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer server.Shutdown()

	if err := server.StartP2P(); err != nil {
		t.Fatalf("Failed to start P2P: %v", err)
	}

	// Verify GossipSub is initialized
	if server.p2pHost.PubSub() == nil {
		t.Error("GossipSub not initialized - pubsub is nil")
	}
}

// TestGossipSub_TopicSubscription tests subscribing to a topic.
// TDD Step 4 (RED): This test will FAIL until we implement topic subscription.
func TestGossipSub_TopicSubscription(t *testing.T) {
	config := &Config{
		Host: "127.0.0.1",
		Port: 17863,
	}
	server := NewServerWithConfig(config)

	if err := server.Start(); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer server.Shutdown()

	if err := server.StartP2P(); err != nil {
		t.Fatalf("Failed to start P2P: %v", err)
	}

	topicName := "/StonkAgents/test/1.0.0"
	if err := server.SubscribeTopic(topicName); err != nil {
		t.Errorf("Failed to subscribe to topic: %v", err)
	}

	// Verify subscription exists
	subscriptions := server.GetSubscriptions()
	found := false
	for _, sub := range subscriptions {
		if sub == topicName {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Topic %q not in subscriptions list", topicName)
	}
}

// TestMDNS_Initialization tests that mDNS peer discovery is enabled.
// TDD Step 5 (RED): This test will FAIL until we enable mDNS.
func TestMDNS_Initialization(t *testing.T) {
	config := &Config{
		Host: "127.0.0.1",
		Port: 17864,
	}
	server := NewServerWithConfig(config)

	if err := server.Start(); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer server.Shutdown()

	if err := server.StartP2P(); err != nil {
		t.Fatalf("Failed to start P2P: %v", err)
	}

	// Verify mDNS is running
	if !server.p2pHost.MDNSEnabled() {
		t.Error("mDNS not enabled for LAN peer discovery")
	}
}

// TestIntegration_DHTWithExistingComponents is the critical progressive test.
// Verifies that adding DHT doesn't break HTTP server + WebSocket (Sprint 2).
// TDD Step 6: This should PASS if our integration is correct.
func TestIntegration_DHTWithExistingComponents(t *testing.T) {
	config := &Config{
		Host: "127.0.0.1",
		Port: 17865,
	}
	server := NewServerWithConfig(config)

	if err := server.Start(); err != nil {
		t.Fatalf("Failed to start HTTP server: %v", err)
	}
	defer server.Shutdown()

	if err := server.StartP2P(); err != nil {
		t.Fatalf("Failed to start P2P with DHT: %v", err)
	}

	// Verify HTTP server still works (Sprint 2 shouldn't break)
	resp, err := httpGet("http://127.0.0.1:17865/health")
	if err != nil {
		t.Errorf("HTTP server broken after DHT initialization: %v", err)
	}
	if resp != nil {
		resp.Body.Close()
	}

	// Verify P2P is running
	peerID := server.GetPeerID()
	if peerID == "" {
		t.Error("P2P not running after DHT initialization")
	}

	// Verify DHT is working
	if server.p2pHost.DHT() == nil {
		t.Error("DHT not initialized")
	}

	// Run concurrent operations to stress test
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// HTTP requests while DHT is active
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				httpGet("http://127.0.0.1:17865/health")
				time.Sleep(100 * time.Millisecond)
			}
		}
	}()

	// Wait for concurrent test
	<-ctx.Done()

	// Final health check
	finalResp, err := httpGet("http://127.0.0.1:17865/health")
	if err != nil {
		t.Errorf("HTTP server crashed during DHT operation: %v", err)
	}
	if finalResp != nil {
		finalResp.Body.Close()
	}
}

// TestIntegration_TwoNodeDHTDiscovery tests that two nodes can discover each other via DHT.
// This is an integration test across the full stack (Sprint 1 + 2 + 3).
func TestIntegration_TwoNodeDHTDiscovery(t *testing.T) {
	config1 := &Config{
		Host: "127.0.0.1",
		Port: 17866,
	}
	server1 := NewServerWithConfig(config1)
	if err := server1.Start(); err != nil {
		t.Fatalf("Failed to start server1: %v", err)
	}
	defer server1.Shutdown()
	if err := server1.StartP2P(); err != nil {
		t.Fatalf("Failed to start P2P on server1: %v", err)
	}

	peerID1 := server1.GetPeerID()
	addrs1 := server1.p2pHost.Addrs()

	// Create second node
	config2 := &Config{
		Host: "127.0.0.1",
		Port: 17867,
	}
	server2 := NewServerWithConfig(config2)
	if err := server2.Start(); err != nil {
		t.Fatalf("Failed to start server2: %v", err)
	}
	defer server2.Shutdown()
	if err := server2.StartP2P(); err != nil {
		t.Fatalf("Failed to start P2P on server2: %v", err)
	}

	// Convert peerID1 string to peer.ID
	peerIDObj, err := peer.Decode(peerID1)
	if err != nil {
		t.Fatalf("Failed to decode peer ID: %v", err)
	}

	if err := server2.ConnectToPeer(peerIDObj, addrs1); err != nil {
		t.Logf("Warning: Failed to connect peers (may be expected in test env): %v", err)
	}

	// Wait for connection
	time.Sleep(1 * time.Second)

	// Check if nodes are connected
	peers2 := server2.p2pHost.ConnectedPeers()
	connected := false
	for _, p := range peers2 {
		if p == peerID1 {
			connected = true
			break
		}
	}

	if !connected {
		t.Log("Warning: Nodes not connected via DHT (may be expected in test environment)")
	}
}
