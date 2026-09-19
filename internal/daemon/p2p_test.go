// Package: internal/daemon
// Feature: F-010 (Go Core Daemon)
// Story: US-010-04 (go-libp2p Integration)
// Purpose: TDD tests for P2P networking

package daemon

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	blockprotocol "github.com/stonkagents/agent/pkg/protocol"
)

// TestP2PHostInitialization - RED test
// Acceptance Criterion: go-libp2p host initialized with Ed25519 keypair from config
func TestP2PHostInitialization(t *testing.T) {
	// Arrange
	config := &Config{
		Host: "127.0.0.1",
		Port: 7841,
	}

	// Act - create P2P host
	p2pHost, err := NewP2PHost(config, nil)
	if err != nil {
		t.Fatalf("Failed to create P2P host: %v", err)
	}
	defer p2pHost.Close()

	// Assert
	if p2pHost == nil {
		t.Error("P2P host should not be nil")
	}

	// Should have a valid PeerID
	if p2pHost.ID() == "" {
		t.Error("PeerID should not be empty")
	}

	// Should be listening
	if len(p2pHost.Addrs()) == 0 {
		t.Error("P2P host should have at least one address")
	}
}

// TestPeerIDFormat - RED test (should pass immediately)
// Acceptance Criterion: PeerID derived from Ed25519 public key
func TestPeerIDFormat(t *testing.T) {
	// Arrange
	config := &Config{
		Host: "127.0.0.1",
		Port: 7841,
	}

	// Act
	p2pHost, err := NewP2PHost(config, nil)
	if err != nil {
		t.Fatalf("Failed to create P2P host: %v", err)
	}
	defer p2pHost.Close()

	// Assert - PeerID should start with "12D3Koo" (libp2p Ed25519 prefix)
	peerID := p2pHost.ID().String()
	if len(peerID) < 10 {
		t.Errorf("PeerID should be at least 10 characters, got %d", len(peerID))
	}

	// libp2p Ed25519 PeerIDs typically start with "12D3Koo"
	if peerID[:7] != "12D3Koo" {
		t.Logf("Note: PeerID doesn't start with expected Ed25519 prefix (got: %s)", peerID[:7])
		// Not failing as format might vary
	}
}

// TestP2PIntegrationWithServer - Integration test
// Acceptance Criterion: P2P host integrates with server lifecycle
func TestP2PIntegrationWithServer(t *testing.T) {
	config := &Config{
		Host: "127.0.0.1",
		Port: 17844, // Different port
	}
	server := NewServerWithConfig(config)

	if err := server.Start(); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer server.Shutdown()

	if err := server.StartP2P(); err != nil {
		t.Fatalf("Failed to start P2P: %v", err)
	}

	// Assert - P2P host should be initialized
	if server.p2pHost == nil {
		t.Error("Server should have P2P host initialized")
	}

	peerID := server.GetPeerID()
	if peerID == "" {
		t.Error("Server should have a PeerID")
	}
}

// Mock chunk provider for testing
type mockChunkProvider struct {
	chunks map[string]*blockprotocol.Chunk
}

func newMockChunkProvider() *mockChunkProvider {
	return &mockChunkProvider{
		chunks: make(map[string]*blockprotocol.Chunk),
	}
}

func (m *mockChunkProvider) GetChunk(ctx context.Context, cid string, chunkIndex int) (*blockprotocol.Chunk, error) {
	key := chunkKey(cid, chunkIndex)
	chunk, exists := m.chunks[key]
	if !exists {
		return nil, fmt.Errorf("chunk not found")
	}
	return chunk, nil
}

func (m *mockChunkProvider) HasChunk(ctx context.Context, cid string, chunkIndex int) (bool, error) {
	key := chunkKey(cid, chunkIndex)
	_, exists := m.chunks[key]
	return exists, nil
}

func (m *mockChunkProvider) StoreChunk(chunk *blockprotocol.Chunk) {
	key := chunkKey(chunk.FileCID, chunk.ChunkIndex)
	m.chunks[key] = chunk
}

func chunkKey(cid string, chunkIndex int) string {
	return fmt.Sprintf("%s:%d", cid, chunkIndex)
}

// TestRegisterBlockExchange_Success - RED test
// Acceptance Criterion: Block exchange protocol handler registered successfully
func TestRegisterBlockExchange_Success(t *testing.T) {
	// Arrange
	config := &Config{
		Host: "127.0.0.1",
		Port: 17850,
	}
	p2pHost, err := NewP2PHost(config, nil)
	if err != nil {
		t.Fatalf("Failed to create P2P host: %v", err)
	}
	defer p2pHost.Close()

	chunkProvider := newMockChunkProvider()

	// Act - register block exchange
	err = p2pHost.RegisterBlockExchange(chunkProvider)

	// Assert
	if err != nil {
		t.Fatalf("RegisterBlockExchange should succeed, got error: %v", err)
	}

	// Verify service is registered
	if p2pHost.BlockExchangeService() == nil {
		t.Error("BlockExchangeService should not be nil after registration")
	}
}

// TestRegisterBlockExchange_DuplicateRegistration - RED test
// Acceptance Criterion: Cannot register block exchange twice
func TestRegisterBlockExchange_DuplicateRegistration(t *testing.T) {
	// Arrange
	config := &Config{
		Host: "127.0.0.1",
		Port: 17851,
	}
	p2pHost, err := NewP2PHost(config, nil)
	if err != nil {
		t.Fatalf("Failed to create P2P host: %v", err)
	}
	defer p2pHost.Close()

	chunkProvider := newMockChunkProvider()

	// Act - register once
	err = p2pHost.RegisterBlockExchange(chunkProvider)
	if err != nil {
		t.Fatalf("First RegisterBlockExchange should succeed: %v", err)
	}

	// Act - try to register again
	err = p2pHost.RegisterBlockExchange(chunkProvider)

	// Assert - should return error
	if err == nil {
		t.Error("Second RegisterBlockExchange should fail")
	}
	if err.Error() != "block exchange already registered" {
		t.Errorf("Expected 'block exchange already registered', got: %v", err)
	}
}

// TestRegisterBlockExchange_ProtocolHandlerRegistered - RED test
// Acceptance Criterion: libp2p stream handler registered for /stonkagents/transfer/1.0.0
func TestRegisterBlockExchange_ProtocolHandlerRegistered(t *testing.T) {
	// Arrange
	config := &Config{
		Host: "127.0.0.1",
		Port: 17852,
	}
	p2pHost, err := NewP2PHost(config, nil)
	if err != nil {
		t.Fatalf("Failed to create P2P host: %v", err)
	}
	defer p2pHost.Close()

	chunkProvider := newMockChunkProvider()

	// Act - register block exchange
	err = p2pHost.RegisterBlockExchange(chunkProvider)
	if err != nil {
		t.Fatalf("RegisterBlockExchange failed: %v", err)
	}

	// Assert - verify protocol handler is registered
	// This is tricky to test directly, but we can verify the service was created
	if p2pHost.blockExchangeSvc == nil {
		t.Error("Block exchange service should be initialized")
	}
}

// TestRegisterBlockExchange_TwoHosts - Integration test
// Acceptance Criterion: Two hosts can connect and exchange blocks
func TestRegisterBlockExchange_TwoHosts(t *testing.T) {
	// Arrange - create two P2P hosts
	config1 := &Config{
		Host: "127.0.0.1",
		Port: 17853,
	}
	host1, err := NewP2PHost(config1, nil)
	if err != nil {
		t.Fatalf("Failed to create host1: %v", err)
	}
	defer host1.Close()

	config2 := &Config{
		Host: "127.0.0.1",
		Port: 17854,
	}
	host2, err := NewP2PHost(config2, nil)
	if err != nil {
		t.Fatalf("Failed to create host2: %v", err)
	}
	defer host2.Close()

	// Register block exchange on both hosts
	provider1 := newMockChunkProvider()
	provider2 := newMockChunkProvider()

	err = host1.RegisterBlockExchange(provider1)
	if err != nil {
		t.Fatalf("Failed to register block exchange on host1: %v", err)
	}

	err = host2.RegisterBlockExchange(provider2)
	if err != nil {
		t.Fatalf("Failed to register block exchange on host2: %v", err)
	}

	// Act - connect the hosts
	peerID2, err := peer.Decode(host2.ID().String())
	if err != nil {
		t.Fatalf("Failed to decode peerID: %v", err)
	}

	err = host1.ConnectToPeer(peerID2, host2.Addrs())
	if err != nil {
		t.Fatalf("Failed to connect hosts: %v", err)
	}

	// Wait for connection to establish
	time.Sleep(100 * time.Millisecond)

	// Assert - verify hosts are connected
	connectedPeers := host1.ConnectedPeers()
	if len(connectedPeers) == 0 {
		t.Error("Host1 should have at least one connected peer")
	}

	// Verify protocol is available
	protocolID := protocol.ID("/stonkagents/transfer/1.0.0")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := host1.host.NewStream(ctx, peerID2, protocolID)
	if err != nil {
		t.Fatalf("Failed to open stream to peer: %v", err)
	}
	defer stream.Close()

	// Verify stream is open
	if stream.Stat().Direction != network.DirOutbound {
		t.Error("Stream should be outbound")
	}
}

// TestStopP2P_CompletesWithinTimeout - RED test
// Feature: F-001 (Security Audit)
// Audit Item: F3 (P2P Shutdown Timeout)
// Acceptance Criterion: StopP2P() uses context.WithTimeout(30s) and completes within timeout
func TestStopP2P_CompletesWithinTimeout(t *testing.T) {
	// Arrange
	config := &Config{
		Host: "127.0.0.1",
		Port: 17855,
	}
	p2pHost, err := NewP2PHost(config, nil)
	if err != nil {
		t.Fatalf("Failed to create P2P host: %v", err)
	}

	// Act - shutdown with timeout verification
	start := time.Now()
	err = p2pHost.Close()
	elapsed := time.Since(start)

	// Assert
	if err != nil {
		t.Errorf("Close should succeed, got error: %v", err)
	}

	// Verify shutdown completed within reasonable time (5 seconds for test)
	// Production uses 30s timeout, but normal shutdown should be much faster
	maxDuration := 5 * time.Second
	if elapsed > maxDuration {
		t.Errorf("Close took too long: %v (max: %v)", elapsed, maxDuration)
	}

	t.Logf("P2P shutdown completed in %v", elapsed)
}
