// Package: internal/daemon/download
// Feature: F-029 (P2P Download Parallelization)
// Story: US-029-P5 (Worker Pool)
// Purpose: TDD tests for parallel worker pool with bounded retry and graceful shutdown

package download

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stonkagents/agent/pkg/protocol"
)

// TestIsRetryable - RED test
// Acceptance Criterion: Error classifier distinguishes transient from permanent errors
func TestIsRetryable(t *testing.T) {
	tests := []struct {
		err      error
		expected bool
	}{
		{errors.New("connection timeout"), true},
		{errors.New("peer disconnected"), true},
		{errors.New("rate limit exceeded"), true},
		{errors.New("temporary failure in network"), true},
		{errors.New("chunk data corrupt"), false},
		{errors.New("CID validation failed"), false},
		{errors.New("cid mismatch detected"), false},
		{errors.New("invalid chunk index"), false},
		{errors.New("unknown error"), true}, // Default: retry
		{nil, false},
	}

	for _, tt := range tests {
		name := "nil"
		if tt.err != nil {
			name = tt.err.Error()
		}
		t.Run(name, func(t *testing.T) {
			result := isRetryable(tt.err)
			if result != tt.expected {
				t.Errorf("isRetryable(%v) = %v, want %v", tt.err, result, tt.expected)
			}
		})
	}
}

// TestClassifyError - RED test
// Acceptance Criterion: Error classifier returns short labels for metrics
func TestClassifyError(t *testing.T) {
	tests := []struct {
		err      error
		expected string
	}{
		{nil, "none"},
		{errors.New("connection timeout"), "network_timeout"},
		{errors.New("peer disconnected"), "peer_disconnect"},
		{errors.New("rate limit hit"), "rate_limit"},
		{errors.New("data corrupt"), "corruption"},
		{errors.New("validation error"), "validation_failure"},
		{errors.New("something else"), "unknown"},
	}

	for _, tt := range tests {
		name := "nil"
		if tt.err != nil {
			name = tt.err.Error()
		}
		t.Run(name, func(t *testing.T) {
			result := classifyError(tt.err)
			if result != tt.expected {
				t.Errorf("classifyError(%v) = %q, want %q", tt.err, result, tt.expected)
			}
		})
	}
}

// TestRunDownloadWorkerPool_AllChunksSucceed - RED test
// Acceptance Criterion: Worker pool downloads all chunks in parallel and succeeds
func TestRunDownloadWorkerPool_AllChunksSucceed(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir, 5)

	totalChunks := 10
	cid := "test-parallel-success"

	// Create metadata so repo operations work
	metadata := &DownloadMetadata{
		CID:         cid,
		Filename:    "test.bin",
		TotalSize:   int64(totalChunks * 256),
		TotalChunks: totalChunks,
		ChunkSize:   256,
		State:       StateActive,
		ChunksMap:   make([]bool, totalChunks),
	}
	if err := m.repo.SaveMetadata(metadata); err != nil {
		t.Fatalf("SaveMetadata failed: %v", err)
	}

	// Setup coordinator with all chunks available from one peer
	coord := NewCoordinator(totalChunks)
	chunks := make([]int, totalChunks)
	for i := range chunks {
		chunks[i] = i
	}
	// Use a valid base58-encoded peer ID
	peerIDStr := "12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN"
	coord.RegisterPeer(peerIDStr, chunks, []string{"/ip4/127.0.0.1/tcp/4001"})

	mockStore := &MockChunkStore{chunks: make(map[string]map[int][]byte)}
	mockP2P := &mockP2PClient{
		requestChunkFunc: func(ctx context.Context, peerIDs []peer.ID, cidArg string, chunkIndex int) (*protocol.Chunk, error) {
			return &protocol.Chunk{Data: []byte("chunk-data"), Size: 256}, nil
		},
	}

	m.SetP2PDependencies(&P2PDependencies{
		ChunkStore:    mockStore,
		P2PClient:     mockP2P,
		Coordinator:   coord,
		PeerConnector: &mockConnector{},
	})

	ctx := context.Background()
	err := m.runDownloadWorkerPool(ctx, cid, totalChunks)
	if err != nil {
		t.Fatalf("runDownloadWorkerPool failed: %v", err)
	}

	// Verify all chunks completed
	if !coord.AllChunksComplete() {
		completed := coord.GetCompletedChunks()
		t.Errorf("Expected all %d chunks complete, got %d: %v", totalChunks, len(completed), completed)
	}

	// Verify all chunks stored
	for i := 0; i < totalChunks; i++ {
		has, _ := mockStore.HasChunk(cid, i)
		if !has {
			t.Errorf("Chunk %d was not stored", i)
		}
	}
}

// TestRunDownloadWorkerPool_ParallelExecution - RED test
// Acceptance Criterion: Multiple workers execute concurrently (not sequentially)
func TestRunDownloadWorkerPool_ParallelExecution(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir, 5)
	m.config.WorkerCount = 3

	totalChunks := 9
	cid := "test-parallel-exec"

	metadata := &DownloadMetadata{
		CID: cid, Filename: "test.bin", TotalSize: int64(totalChunks * 256),
		TotalChunks: totalChunks, ChunkSize: 256, State: StateActive,
		ChunksMap: make([]bool, totalChunks),
	}
	m.repo.SaveMetadata(metadata)

	coord := NewCoordinator(totalChunks)
	chunks := make([]int, totalChunks)
	for i := range chunks {
		chunks[i] = i
	}
	peerIDStr := "12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN"
	coord.RegisterPeer(peerIDStr, chunks, nil)

	// Track max concurrent requests
	var concurrent int64
	var maxConcurrent int64

	mockP2P := &mockP2PClient{
		requestChunkFunc: func(ctx context.Context, peerIDs []peer.ID, cidArg string, chunkIndex int) (*protocol.Chunk, error) {
			cur := atomic.AddInt64(&concurrent, 1)
			for {
				old := atomic.LoadInt64(&maxConcurrent)
				if cur <= old || atomic.CompareAndSwapInt64(&maxConcurrent, old, cur) {
					break
				}
			}
			time.Sleep(50 * time.Millisecond) // Simulate work
			atomic.AddInt64(&concurrent, -1)
			return &protocol.Chunk{Data: []byte("data"), Size: 256}, nil
		},
	}

	m.SetP2PDependencies(&P2PDependencies{
		ChunkStore:    &MockChunkStore{chunks: make(map[string]map[int][]byte)},
		P2PClient:     mockP2P,
		Coordinator:   coord,
		PeerConnector: &mockConnector{},
	})

	err := m.runDownloadWorkerPool(context.Background(), cid, totalChunks)
	if err != nil {
		t.Fatalf("runDownloadWorkerPool failed: %v", err)
	}

	peak := atomic.LoadInt64(&maxConcurrent)
	if peak < 2 {
		t.Errorf("Expected concurrent execution (peak >= 2), got peak=%d", peak)
	}
}

// TestRunDownloadWorkerPool_ContextCancellation - RED test
// Acceptance Criterion: Workers exit gracefully when context is cancelled
func TestRunDownloadWorkerPool_ContextCancellation(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir, 5)
	m.config.WorkerCount = 3

	totalChunks := 100
	cid := "test-cancel"

	metadata := &DownloadMetadata{
		CID: cid, Filename: "test.bin", TotalSize: int64(totalChunks * 256),
		TotalChunks: totalChunks, ChunkSize: 256, State: StateActive,
		ChunksMap: make([]bool, totalChunks),
	}
	m.repo.SaveMetadata(metadata)

	coord := NewCoordinator(totalChunks)
	chunks := make([]int, totalChunks)
	for i := range chunks {
		chunks[i] = i
	}
	peerIDStr := "12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN"
	coord.RegisterPeer(peerIDStr, chunks, nil)

	// Slow downloads — context will be cancelled mid-download
	mockP2P := &mockP2PClient{
		requestChunkFunc: func(ctx context.Context, peerIDs []peer.ID, cidArg string, chunkIndex int) (*protocol.Chunk, error) {
			select {
			case <-time.After(500 * time.Millisecond):
				return &protocol.Chunk{Data: []byte("data"), Size: 256}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}

	m.SetP2PDependencies(&P2PDependencies{
		ChunkStore:    &MockChunkStore{chunks: make(map[string]map[int][]byte)},
		P2PClient:     mockP2P,
		Coordinator:   coord,
		PeerConnector: &mockConnector{},
	})

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after 200ms
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	done := make(chan error, 1)
	go func() {
		done <- m.runDownloadWorkerPool(ctx, cid, totalChunks)
	}()

	select {
	case err := <-done:
		// Should fail (not all chunks completed) but should exit promptly
		if err == nil {
			t.Error("Expected error after cancellation, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Worker pool did not exit within 5s after cancellation")
	}
}

// TestRunDownloadWorkerPool_RetryBudget - RED test
// Acceptance Criterion: Worker retries transient errors up to budget, then fails chunk
func TestRunDownloadWorkerPool_RetryBudget(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir, 5)
	m.config.WorkerCount = 1
	m.config.RetryBudget = 3
	m.config.RetryBackoffBase = 10 * time.Millisecond // Fast for tests

	totalChunks := 1
	cid := "test-retry"

	metadata := &DownloadMetadata{
		CID: cid, Filename: "test.bin", TotalSize: 256,
		TotalChunks: 1, ChunkSize: 256, State: StateActive,
		ChunksMap: make([]bool, 1),
	}
	m.repo.SaveMetadata(metadata)

	coord := NewCoordinator(totalChunks)
	peerIDStr := "12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN"
	coord.RegisterPeer(peerIDStr, []int{0}, nil)

	var attempts int32
	mockP2P := &mockP2PClient{
		requestChunkFunc: func(ctx context.Context, peerIDs []peer.ID, cidArg string, chunkIndex int) (*protocol.Chunk, error) {
			atomic.AddInt32(&attempts, 1)
			return nil, errors.New("connection timeout") // Retryable
		},
	}

	m.SetP2PDependencies(&P2PDependencies{
		ChunkStore:    &MockChunkStore{chunks: make(map[string]map[int][]byte)},
		P2PClient:     mockP2P,
		Coordinator:   coord,
		PeerConnector: &mockConnector{},
	})

	err := m.runDownloadWorkerPool(context.Background(), cid, totalChunks)
	if err == nil {
		t.Fatal("Expected error after retry budget exhausted")
	}

	totalAttempts := atomic.LoadInt32(&attempts)
	if totalAttempts != 3 {
		t.Errorf("Expected exactly 3 attempts (retry budget), got %d", totalAttempts)
	}
}

// TestRunDownloadWorkerPool_PermanentError_NoRetry - RED test
// Acceptance Criterion: Permanent errors skip retries
func TestRunDownloadWorkerPool_PermanentError_NoRetry(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir, 5)
	m.config.WorkerCount = 1
	m.config.RetryBudget = 3
	m.config.RetryBackoffBase = 10 * time.Millisecond

	totalChunks := 1
	cid := "test-permanent"

	metadata := &DownloadMetadata{
		CID: cid, Filename: "test.bin", TotalSize: 256,
		TotalChunks: 1, ChunkSize: 256, State: StateActive,
		ChunksMap: make([]bool, 1),
	}
	m.repo.SaveMetadata(metadata)

	coord := NewCoordinator(totalChunks)
	peerIDStr := "12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN"
	coord.RegisterPeer(peerIDStr, []int{0}, nil)

	var attempts int32
	mockP2P := &mockP2PClient{
		requestChunkFunc: func(ctx context.Context, peerIDs []peer.ID, cidArg string, chunkIndex int) (*protocol.Chunk, error) {
			atomic.AddInt32(&attempts, 1)
			return nil, errors.New("chunk data corrupt") // Permanent
		},
	}

	m.SetP2PDependencies(&P2PDependencies{
		ChunkStore:    &MockChunkStore{chunks: make(map[string]map[int][]byte)},
		P2PClient:     mockP2P,
		Coordinator:   coord,
		PeerConnector: &mockConnector{},
	})

	err := m.runDownloadWorkerPool(context.Background(), cid, totalChunks)
	if err == nil {
		t.Fatal("Expected error for corrupt chunk")
	}

	totalAttempts := atomic.LoadInt32(&attempts)
	if totalAttempts != 1 {
		t.Errorf("Expected exactly 1 attempt (no retry for permanent error), got %d", totalAttempts)
	}
}

// TestRunDownloadWorkerPool_PartialFailure - RED test
// Acceptance Criterion: Some chunks succeed, some fail — partial progress is preserved
func TestRunDownloadWorkerPool_PartialFailure(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir, 5)
	m.config.WorkerCount = 2
	m.config.RetryBudget = 1 // No retries — fail immediately
	m.config.RetryBackoffBase = 10 * time.Millisecond

	totalChunks := 6
	cid := "test-partial"

	metadata := &DownloadMetadata{
		CID: cid, Filename: "test.bin", TotalSize: int64(totalChunks * 256),
		TotalChunks: totalChunks, ChunkSize: 256, State: StateActive,
		ChunksMap: make([]bool, totalChunks),
	}
	m.repo.SaveMetadata(metadata)

	coord := NewCoordinator(totalChunks)
	chunks := make([]int, totalChunks)
	for i := range chunks {
		chunks[i] = i
	}
	peerIDStr := "12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN"
	coord.RegisterPeer(peerIDStr, chunks, nil)

	mockStore := &MockChunkStore{chunks: make(map[string]map[int][]byte)}

	// Fail on chunk 3 (permanent error stops the worker that hits it)
	var mu sync.Mutex
	failChunk := 3
	mockP2P := &mockP2PClient{
		requestChunkFunc: func(ctx context.Context, peerIDs []peer.ID, cidArg string, chunkIndex int) (*protocol.Chunk, error) {
			mu.Lock()
			shouldFail := chunkIndex == failChunk
			mu.Unlock()
			if shouldFail {
				return nil, fmt.Errorf("chunk data corrupt for chunk %d", chunkIndex)
			}
			return &protocol.Chunk{Data: []byte("data"), Size: 256}, nil
		},
	}

	m.SetP2PDependencies(&P2PDependencies{
		ChunkStore:    mockStore,
		P2PClient:     mockP2P,
		Coordinator:   coord,
		PeerConnector: &mockConnector{},
	})

	err := m.runDownloadWorkerPool(context.Background(), cid, totalChunks)
	if err == nil {
		t.Fatal("Expected error due to failed chunk")
	}

	// Some chunks should have completed before the failure
	completed := coord.GetCompletedChunks()
	if len(completed) == 0 {
		t.Error("Expected at least some chunks to complete before failure")
	}
	if len(completed) >= totalChunks {
		t.Error("Should not have completed all chunks since chunk 3 is permanently corrupt")
	}
}

// TestDownloadChunkFromPeer_Success - RED test
// Acceptance Criterion: downloadChunkFromPeer stores chunk and updates repo
func TestDownloadChunkFromPeer_Success(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir, 5)

	cid := "test-single-chunk"
	totalChunks := 5

	metadata := &DownloadMetadata{
		CID: cid, Filename: "test.bin", TotalSize: int64(totalChunks * 256),
		TotalChunks: totalChunks, ChunkSize: 256, State: StateActive,
		ChunksMap: make([]bool, totalChunks),
	}
	m.repo.SaveMetadata(metadata)

	mockStore := &MockChunkStore{chunks: make(map[string]map[int][]byte)}
	mockP2P := &mockP2PClient{
		requestChunkFunc: func(ctx context.Context, peerIDs []peer.ID, cidArg string, chunkIndex int) (*protocol.Chunk, error) {
			return &protocol.Chunk{Data: []byte("chunk-data-here"), Size: 256}, nil
		},
	}

	coord := NewCoordinator(totalChunks)
	peerIDStr := "12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN"
	coord.RegisterPeer(peerIDStr, []int{0, 1, 2, 3, 4}, nil)

	m.SetP2PDependencies(&P2PDependencies{
		ChunkStore:    mockStore,
		P2PClient:     mockP2P,
		Coordinator:   coord,
		PeerConnector: &mockConnector{},
	})

	err := m.downloadChunkFromPeer(context.Background(), peerIDStr, cid, 2)
	if err != nil {
		t.Fatalf("downloadChunkFromPeer failed: %v", err)
	}

	// Verify chunk was stored
	has, _ := mockStore.HasChunk(cid, 2)
	if !has {
		t.Error("Chunk 2 was not stored in chunk store")
	}

	// Verify repo was updated
	loaded, err := m.repo.LoadMetadata(cid)
	if err != nil {
		t.Fatalf("LoadMetadata failed: %v", err)
	}
	if !loaded.ChunksMap[2] {
		t.Error("Chunk 2 was not marked complete in repository")
	}
}
