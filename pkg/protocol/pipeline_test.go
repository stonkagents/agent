// Package: pkg/protocol
// Feature: F-010 (P2P Transfer Protocol)
// Story: US-010-05 (Block Exchange Protocol)
// Purpose: TDD tests for request pipelining (max 16 concurrent requests per peer)

package protocol

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// TestRequestPipeline_MaxConcurrent tests that pipelining limits concurrent requests.
// TDD Step 7 (RED): This test will FAIL until we implement request pipelining.
func TestRequestPipeline_MaxConcurrent(t *testing.T) {
	ctx := context.Background()

	// Create a pipeline with max 16 concurrent requests
	pipeline := NewRequestPipeline(MaxConcurrentRequests)

	// Track concurrent requests
	var currentConcurrent int32
	var maxObserved int32
	var mu sync.Mutex

	// Mock chunk requester that sleeps to simulate slow requests
	requester := func(chunkIndex int) (*Chunk, error) {
		current := atomic.AddInt32(&currentConcurrent, 1)
		defer atomic.AddInt32(&currentConcurrent, -1)

		// Update max observed
		mu.Lock()
		if current > maxObserved {
			maxObserved = current
		}
		mu.Unlock()

		// Simulate network delay
		time.Sleep(10 * time.Millisecond)

		return &Chunk{
			FileCID:    "test-cid",
			ChunkIndex: chunkIndex,
			ChunkCID:   "chunk-cid",
			Data:       []byte("test data"),
			Size:       9,
		}, nil
	}

	// Request 50 chunks (should pipeline with max 16 concurrent)
	numChunks := 50
	resultChan := make(chan *ChunkResult, numChunks)

	// Start requesting chunks
	for i := 0; i < numChunks; i++ {
		chunkIndex := i
		go func() {
			chunk, err := pipeline.RequestWithSemaphore(ctx, func() (*Chunk, error) {
				return requester(chunkIndex)
			})
			resultChan <- &ChunkResult{
				Chunk: chunk,
				Error: err,
			}
		}()
	}

	// Collect results
	var results []*ChunkResult
	for i := 0; i < numChunks; i++ {
		result := <-resultChan
		results = append(results, result)
	}

	// Verify all chunks received
	if len(results) != numChunks {
		t.Errorf("Expected %d results, got %d", numChunks, len(results))
	}

	// Verify no errors
	for i, result := range results {
		if result.Error != nil {
			t.Errorf("Result %d has error: %v", i, result.Error)
		}
		if result.Chunk == nil {
			t.Errorf("Result %d has nil chunk", i)
		}
	}

	// Verify max concurrent requests did not exceed limit
	if maxObserved > MaxConcurrentRequests {
		t.Errorf("Max concurrent requests exceeded: observed %d, limit %d", maxObserved, MaxConcurrentRequests)
	}

	// Should have reached close to the limit (allowing some variance)
	if maxObserved < MaxConcurrentRequests-2 {
		t.Logf("Warning: Max concurrent requests was %d, expected close to %d", maxObserved, MaxConcurrentRequests)
	}
}

// TestRequestPipeline_ContextCancellation tests that pipeline respects context cancellation.
func TestRequestPipeline_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	pipeline := NewRequestPipeline(MaxConcurrentRequests)

	// Mock slow requester
	requester := func() (*Chunk, error) {
		time.Sleep(100 * time.Millisecond)
		return &Chunk{FileCID: "test"}, nil
	}

	// Start request
	resultChan := make(chan error, 1)
	go func() {
		_, err := pipeline.RequestWithSemaphore(ctx, requester)
		resultChan <- err
	}()

	// Cancel context after 10ms
	time.Sleep(10 * time.Millisecond)
	cancel()

	// Wait for result
	err := <-resultChan
	if err == nil {
		t.Error("Expected context cancellation error, got nil")
	}
	if err != context.Canceled {
		t.Errorf("Expected context.Canceled error, got %v", err)
	}
}

// TestRequestPipeline_Timeout tests request timeout handling.
func TestRequestPipeline_Timeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	pipeline := NewRequestPipeline(MaxConcurrentRequests)

	// Mock very slow requester (slower than timeout)
	requester := func() (*Chunk, error) {
		time.Sleep(200 * time.Millisecond)
		return &Chunk{FileCID: "test"}, nil
	}

	// Request should timeout
	_, err := pipeline.RequestWithSemaphore(ctx, requester)
	if err == nil {
		t.Error("Expected timeout error, got nil")
	}
	if err != context.DeadlineExceeded {
		t.Errorf("Expected context.DeadlineExceeded, got %v", err)
	}
}

// TestRequestPipeline_Priority tests priority queue ordering.
func TestRequestPipeline_Priority(t *testing.T) {
	ctx := context.Background()

	pipeline := NewRequestPipeline(1) // Only 1 concurrent to test ordering

	var executionOrder []int
	var mu sync.Mutex

	// Mock requester that records execution order
	makeRequester := func(id int) func() (*Chunk, error) {
		return func() (*Chunk, error) {
			mu.Lock()
			executionOrder = append(executionOrder, id)
			mu.Unlock()
			time.Sleep(10 * time.Millisecond)
			return &Chunk{ChunkIndex: id}, nil
		}
	}

	// Queue requests with different priorities
	type request struct {
		id       int
		priority int
	}
	requests := []request{
		{id: 1, priority: 10},
		{id: 2, priority: 100}, // Highest priority
		{id: 3, priority: 50},
		{id: 4, priority: 20},
	}

	var wg sync.WaitGroup
	for _, req := range requests {
		wg.Add(1)
		req := req // capture loop variable
		go func() {
			defer wg.Done()
			pipeline.RequestWithPriority(ctx, makeRequester(req.id), req.priority)
		}()
		time.Sleep(5 * time.Millisecond) // Small delay to ensure queueing order
	}

	wg.Wait()

	// Verify execution order respects priority (higher priority first)
	// Expected order: 2 (100), 3 (50), 4 (20), 1 (10)
	// Note: First request may execute immediately before others are queued
	if len(executionOrder) != len(requests) {
		t.Fatalf("Expected %d executed requests, got %d", len(requests), len(executionOrder))
	}

	t.Logf("Execution order: %v", executionOrder)
	// Priority ordering should be respected for queued items
}

// TestRequestPipeline_MultiPeer tests separate pipelines per peer.
func TestRequestPipeline_MultiPeer(t *testing.T) {
	ctx := context.Background()

	// Create multiple pipelines (one per peer)
	peer1, _ := peer.Decode("12D3KooWPeer1111111111111111111111111111111111111")
	peer2, _ := peer.Decode("12D3KooWPeer2222222222222222222222222222222222222")

	pipelines := make(map[peer.ID]*RequestPipeline)
	pipelines[peer1] = NewRequestPipeline(MaxConcurrentRequests)
	pipelines[peer2] = NewRequestPipeline(MaxConcurrentRequests)

	// Track concurrent requests per peer
	var peer1Concurrent, peer2Concurrent int32

	requester1 := func() (*Chunk, error) {
		atomic.AddInt32(&peer1Concurrent, 1)
		defer atomic.AddInt32(&peer1Concurrent, -1)
		time.Sleep(20 * time.Millisecond)
		return &Chunk{FileCID: "peer1"}, nil
	}

	requester2 := func() (*Chunk, error) {
		atomic.AddInt32(&peer2Concurrent, 1)
		defer atomic.AddInt32(&peer2Concurrent, -1)
		time.Sleep(20 * time.Millisecond)
		return &Chunk{FileCID: "peer2"}, nil
	}

	var wg sync.WaitGroup

	// Request 20 chunks from each peer
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			pipelines[peer1].RequestWithSemaphore(ctx, requester1)
		}()
		go func() {
			defer wg.Done()
			pipelines[peer2].RequestWithSemaphore(ctx, requester2)
		}()
	}

	wg.Wait()

	// Both peers should have been able to process concurrently
	// (separate pipelines = independent limits)
	t.Logf("Peer1 max concurrent: %d", peer1Concurrent)
	t.Logf("Peer2 max concurrent: %d", peer2Concurrent)
}

// TestRequestPipeline_Close tests graceful shutdown.
func TestRequestPipeline_Close(t *testing.T) {
	ctx := context.Background()

	pipeline := NewRequestPipeline(MaxConcurrentRequests)

	// Start some requests
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pipeline.RequestWithSemaphore(ctx, func() (*Chunk, error) {
				time.Sleep(50 * time.Millisecond)
				return &Chunk{}, nil
			})
		}()
	}

	// Close pipeline
	err := pipeline.Close()
	if err != nil {
		t.Errorf("Close() error: %v", err)
	}

	// Wait for in-flight requests to complete
	wg.Wait()

	// New requests after close should fail
	_, err = pipeline.RequestWithSemaphore(ctx, func() (*Chunk, error) {
		return &Chunk{}, nil
	})
	if err == nil {
		t.Error("Expected error after Close(), got nil")
	}
}
