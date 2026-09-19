// Package: internal/daemon/download
// Feature: F-010 (P2P Transfer Protocol)
// Story: US-010-15 (Download Worker P2P Integration)
// Purpose: Test helpers and mocks

package download

import (
	"context"
	"fmt"
	"sync"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stonkagents/agent/pkg/protocol"
)

// MockChunkStore is a mock implementation of ChunkStoreInterface for testing.
// Thread-safe: uses mutex to support concurrent worker pool tests (US-029-P5).
type MockChunkStore struct {
	mu      sync.Mutex
	chunks  map[string]map[int][]byte // fileCID -> chunkIndex -> data
	deleted bool
}

func (m *MockChunkStore) StoreChunk(cid string, chunkIndex int, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.chunks[cid] == nil {
		m.chunks[cid] = make(map[int][]byte)
	}
	m.chunks[cid][chunkIndex] = data
	return nil
}

func (m *MockChunkStore) GetChunk(cid string, chunkIndex int) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.chunks[cid] == nil {
		return nil, fmt.Errorf("file not found: %s", cid)
	}
	data, exists := m.chunks[cid][chunkIndex]
	if !exists {
		return nil, fmt.Errorf("chunk not found: file=%s, index=%d", cid, chunkIndex)
	}
	return data, nil
}

func (m *MockChunkStore) HasChunk(cid string, chunkIndex int) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.chunks[cid] == nil {
		return false, nil
	}
	_, exists := m.chunks[cid][chunkIndex]
	return exists, nil
}

func (m *MockChunkStore) DeleteChunks(cid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleted = true
	delete(m.chunks, cid)
	return nil
}

// Helper function to check if string contains substring
func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// mockP2PClient is a mock implementation of P2PClientInterface for testing
type mockP2PClient struct {
	requestChunkFunc func(ctx context.Context, peerIDs []peer.ID, cid string, chunkIndex int) (*protocol.Chunk, error)
}

func (m *mockP2PClient) RequestChunk(ctx context.Context, peerIDs []peer.ID, cid string, chunkIndex int) (*protocol.Chunk, error) {
	if m.requestChunkFunc != nil {
		return m.requestChunkFunc(ctx, peerIDs, cid, chunkIndex)
	}
	return nil, fmt.Errorf("mock not configured")
}

func (m *mockP2PClient) RequestChunks(ctx context.Context, peerIDs []peer.ID, cid string, chunkIndices []int) (<-chan *protocol.ChunkResult, error) {
	// Not used in tests
	return nil, fmt.Errorf("not implemented")
}

// mockConnector is a mock implementation of PeerConnector for testing
type mockConnector struct {
	connectFn func(peerID peer.ID, addrs []string) error
}

func (mc *mockConnector) ConnectToPeer(peerID peer.ID, addrs []string) error {
	if mc.connectFn != nil {
		return mc.connectFn(peerID, addrs)
	}
	return nil
}
