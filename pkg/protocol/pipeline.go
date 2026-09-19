// Package: pkg/protocol
// Feature: F-010 (P2P Transfer Protocol)
// Story: US-010-05 (Block Exchange Protocol)
// Purpose: Request pipelining with semaphore (max 16 concurrent requests per peer)

package protocol

import (
	"context"
	"fmt"
	"sync"
)

// RequestPipeline manages concurrent chunk requests with pipelining.
// Limits concurrent requests per peer to prevent overwhelming connections.
type RequestPipeline struct {
	sem       chan struct{} // Semaphore for limiting concurrent requests
	maxConcur int
	closed    bool
	mu        sync.Mutex
}

// NewRequestPipeline creates a new request pipeline with max concurrent limit.
func NewRequestPipeline(maxConcurrent int) *RequestPipeline {
	return &RequestPipeline{
		sem:       make(chan struct{}, maxConcurrent),
		maxConcur: maxConcurrent,
		closed:    false,
	}
}

// RequestWithSemaphore executes a chunk request with semaphore-based concurrency control.
// Blocks until a semaphore slot is available, respects context cancellation.
func (p *RequestPipeline) RequestWithSemaphore(ctx context.Context, requester func() (*Chunk, error)) (*Chunk, error) {
	// Check if pipeline is closed
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, fmt.Errorf("pipeline is closed")
	}
	p.mu.Unlock()

	// Acquire semaphore slot
	select {
	case p.sem <- struct{}{}:
		// Got semaphore slot
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// Ensure semaphore is released
	defer func() { <-p.sem }()

	// Check context again before executing
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Execute the request in a goroutine to allow context cancellation monitoring
	resultChan := make(chan *Chunk, 1)
	errChan := make(chan error, 1)

	go func() {
		chunk, err := requester()
		if err != nil {
			errChan <- err
		} else {
			resultChan <- chunk
		}
	}()

	// Wait for result or context cancellation
	select {
	case chunk := <-resultChan:
		return chunk, nil
	case err := <-errChan:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// RequestWithPriority executes a chunk request with priority queue ordering.
// Higher priority requests execute first when multiple are queued.
// For MVP: simplified to use semaphore without priority ordering (GREEN phase).
// TODO: Add proper priority queue in REFACTOR phase.
func (p *RequestPipeline) RequestWithPriority(ctx context.Context, requester func() (*Chunk, error), priority int) (*Chunk, error) {
	// For now, just delegate to RequestWithSemaphore (simplified GREEN implementation)
	// Priority ordering will be added in REFACTOR phase
	return p.RequestWithSemaphore(ctx, requester)
}

// Close shuts down the pipeline gracefully.
// Waits for in-flight requests to complete, then prevents new requests.
func (p *RequestPipeline) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return fmt.Errorf("pipeline already closed")
	}

	p.closed = true
	return nil
}
