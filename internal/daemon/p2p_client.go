// Package: internal/daemon
// Feature: F-003 (Universal File Sharing)
// Story: US-003-07 (P2P Socket Client Implementation)
// Purpose: P2P block exchange client for requesting chunks from remote peers
//
// Implementation Phases (TDD RED-GREEN-REFACTOR):
// Phase 1: Basic Stream Handling ✅
// Phase 2: CID Verification (Security) ✅
// Phase 3: Retry Logic (Resilience) ✅
// Phase 4: Peer Failover ✅
// Phase 5: Pipelining (Concurrency) ✅
// Phase 6: Security Hardening ✅

package daemon

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	protov1 "github.com/stonkagents/agent/api/proto/v1"
	"github.com/stonkagents/agent/internal/logger"
	crypto "github.com/stonkagents/agent/pkg/cryptography"
	"github.com/stonkagents/agent/pkg/protocol"
	"golang.org/x/time/rate"
	protobuf "google.golang.org/protobuf/proto"
)

// Phase 6: Security constants
const (
	MaxMessageSize        = 4 * 1024 * 1024  // 4MB max protobuf message size
	MaxBlockSize          = 2 * 1024 * 1024  // 2MB max chunk/block size
	RequestTimeout        = 30 * time.Second // 30s timeout per request
	DefaultRequestsPerSec = 100              // 100 requests/second rate limit
)

// P2PClient implements client-side block exchange over libp2p streams
// Phase 6: Added rate limiter for security
type P2PClient struct {
	host        host.Host
	logger      *logger.Logger
	rateLimiter *rate.Limiter // Phase 6: Rate limiting (100 req/sec default)
}

// NewP2PClient creates a new P2P block exchange client with default rate limit
func NewP2PClient(h host.Host, log *logger.Logger) *P2PClient {
	return NewP2PClientWithRateLimit(h, log, DefaultRequestsPerSec)
}

// NewP2PClientWithRateLimit creates a new P2P block exchange client with custom rate limit
// Phase 6: Configurable rate limiting for security
func NewP2PClientWithRateLimit(h host.Host, log *logger.Logger, requestsPerSec int) *P2PClient {
	return &P2PClient{
		host:        h,
		logger:      log,
		rateLimiter: rate.NewLimiter(rate.Limit(requestsPerSec), requestsPerSec),
	}
}

// RequestChunk requests a specific chunk from peers via libp2p stream with retry logic and peer failover
// Phase 1: Basic stream handling (create stream, send request, receive response) ✅
// Phase 2: Client-side CID verification (cryptographic integrity check) ✅
// Phase 3: Retry logic with exponential backoff and jitter ✅
// Phase 4: Peer failover (try each peer max 2 times before switching) ✅
func (c *P2PClient) RequestChunk(ctx context.Context, peerIDs []peer.ID, cid string, chunkIndex int) (*protocol.Chunk, error) {
	if len(peerIDs) == 0 {
		return nil, fmt.Errorf("no peers provided for chunk request")
	}

	// Phase 4: Peer failover - try each peer up to 2 times
	const maxAttemptsPerPeer = 2
	backoffDurations := []time.Duration{1 * time.Second, 2 * time.Second} // Only 2 backoffs since 2 attempts per peer

	var lastErr error
	totalAttempts := 0

	// Iterate through peer list
	for peerIdx, peerID := range peerIDs {
		// Try current peer up to maxAttemptsPerPeer times
		for attempt := 0; attempt < maxAttemptsPerPeer; attempt++ {
			totalAttempts++

			chunk, err := c.requestChunkOnce(ctx, peerID, cid, chunkIndex)
			if err == nil {
				// Success!
				if totalAttempts > 1 && c.logger != nil {
					c.logger.Info("P2PClient", "RequestChunk", "Request succeeded after retries/failover", map[string]interface{}{
						"peer":           peerID.String(),
						"peer_index":     peerIdx,
						"cid":            cid,
						"chunk_index":    chunkIndex,
						"attempt":        attempt + 1,
						"total_attempts": totalAttempts,
					})
				}
				return chunk, nil
			}

			lastErr = err

			// If this wasn't the last attempt for this peer, apply backoff
			if attempt < maxAttemptsPerPeer-1 {
				// Log retry attempt (same peer)
				if c.logger != nil {
					c.logger.Warn("P2PClient", "RequestChunk", "Request failed, retrying same peer", map[string]interface{}{
						"peer":        peerID.String(),
						"peer_index":  peerIdx,
						"cid":         cid,
						"chunk_index": chunkIndex,
						"attempt":     attempt + 1,
						"error":       err.Error(),
						"next_retry":  backoffDurations[attempt].String(),
					})
				}

				// Phase 3: Exponential backoff with ±20% jitter
				backoff := backoffDurations[attempt]
				jitter := time.Duration(float64(backoff) * (rand.Float64()*0.4 - 0.2)) // ±20%
				sleepDuration := backoff + jitter

				// Respect context cancellation during backoff
				select {
				case <-time.After(sleepDuration):
					// Continue to next retry
				case <-ctx.Done():
					return nil, fmt.Errorf("request cancelled during retry backoff: %w", ctx.Err())
				}
			} else if peerIdx < len(peerIDs)-1 {
				// Failed all attempts for this peer, switching to next peer (no backoff between peers)
				if c.logger != nil {
					c.logger.Warn("P2PClient", "RequestChunk", "Peer exhausted, switching to next peer", map[string]interface{}{
						"failed_peer":       peerID.String(),
						"failed_peer_index": peerIdx,
						"next_peer":         peerIDs[peerIdx+1].String(),
						"next_peer_index":   peerIdx + 1,
						"cid":               cid,
						"chunk_index":       chunkIndex,
					})
				}
			}
		}
	}

	// All peers exhausted
	return nil, fmt.Errorf("all peers exhausted (%d peers, %d total attempts) for chunk request (cid=%s, index=%d): %w", len(peerIDs), totalAttempts, cid, chunkIndex, lastErr)
}

// requestChunkOnce performs a single chunk request without retry logic
// Extracted from RequestChunk for Phase 3 (REFACTOR)
// Phase 6: Enhanced with security controls (rate limiting, timeout, size validation)
func (c *P2PClient) requestChunkOnce(ctx context.Context, peerID peer.ID, cid string, chunkIndex int) (*protocol.Chunk, error) {
	// Phase 6: Rate limiting (wait for rate limiter token)
	if err := c.rateLimiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("rate limiter cancelled: %w", err)
	}

	// Phase 6: Apply 30-second timeout to entire request
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()

	// Phase 1: Create libp2p stream
	stream, err := c.host.NewStream(ctx, peerID, protocol.ProtocolIDStonkAgents)
	if err != nil {
		return nil, fmt.Errorf("failed to create stream to peer %s: %w", peerID, err)
	}
	defer stream.Close()

	// Phase 1: Encode BlockRequest protobuf message
	requestID := uuid.New().String()
	blockReq := &protov1.BlockRequest{
		FileCid:    cid,
		ChunkIndex: int32(chunkIndex),
		RequestId:  requestID,
		Priority:   50, // Medium priority (0-100 scale)
	}

	envelope := &protov1.Message{
		Payload: &protov1.Message_BlockRequest{
			BlockRequest: blockReq,
		},
	}

	reqData, err := protobuf.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal BlockRequest: %w", err)
	}

	// Phase 1: Send request to stream
	_, err = stream.Write(reqData)
	if err != nil {
		return nil, fmt.Errorf("failed to write request to stream: %w", err)
	}
	// Signal end of request payload so remote can read without waiting for EOF.
	type closeWriter interface {
		CloseWrite() error
	}
	if cw, ok := stream.(closeWriter); ok {
		_ = cw.CloseWrite()
	}

	// Phase 1: Read response from stream
	respData, err := io.ReadAll(stream)
	if err != nil {
		return nil, fmt.Errorf("failed to read response from stream: %w", err)
	}

	// Phase 6: Validate message size (4MB limit)
	if len(respData) > MaxMessageSize {
		if c.logger != nil {
			c.logger.Error("P2PClient", "RequestChunk", "Security: Message size exceeds limit", map[string]interface{}{
				"peer":         peerID.String(),
				"cid":          cid,
				"chunk_index":  chunkIndex,
				"message_size": len(respData),
				"limit":        MaxMessageSize,
				"severity":     "Security",
			})
		}
		return nil, fmt.Errorf("message size (%d bytes) exceeds limit (%d bytes)", len(respData), MaxMessageSize)
	}

	// Phase 1: Decode BlockResponse protobuf message
	var respEnvelope protov1.Message
	err = protobuf.Unmarshal(respData, &respEnvelope)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	// Handle different response types
	if blockResp := respEnvelope.GetBlockResponse(); blockResp != nil {
		// Phase 1: Convert protobuf BlockResponse to protocol.Chunk
		chunk := &protocol.Chunk{
			FileCID:    blockResp.FileCid,
			ChunkIndex: int(blockResp.ChunkIndex),
			ChunkCID:   blockResp.ChunkCid,
			Data:       blockResp.Data,
			Size:       int64(len(blockResp.Data)),
		}

		// Phase 6: Validate chunk/block size (2MB limit)
		if len(chunk.Data) > MaxBlockSize {
			if c.logger != nil {
				c.logger.Error("P2PClient", "RequestChunk", "Security: Chunk size exceeds limit", map[string]interface{}{
					"peer":        peerID.String(),
					"cid":         cid,
					"chunk_index": chunkIndex,
					"chunk_size":  len(chunk.Data),
					"limit":       MaxBlockSize,
					"severity":    "Security",
				})
			}
			return nil, fmt.Errorf("chunk size (%d bytes) exceeds limit (%d bytes)", len(chunk.Data), MaxBlockSize)
		}

		// Phase 2: Client-side CID verification (SECURITY CONTROL)
		valid, err := crypto.VerifyCID(chunk.ChunkCID, chunk.Data)
		if err != nil {
			// Log security event: CID verification error
			if c.logger != nil {
				c.logger.Error("P2PClient", "RequestChunk", "CID verification error", map[string]interface{}{
					"peer":        peerID.String(),
					"cid":         cid,
					"chunk_index": chunkIndex,
					"error":       err.Error(),
				})
			}
			return nil, fmt.Errorf("CID verification error: %w", err)
		}

		if !valid {
			// Log security event: CID verification failed (data corruption or malicious peer)
			if c.logger != nil {
				c.logger.Error("P2PClient", "RequestChunk", "CID verification failed", map[string]interface{}{
					"peer":        peerID.String(),
					"cid":         cid,
					"chunk_index": chunkIndex,
					"chunk_cid":   chunk.ChunkCID,
				})
			}
			return nil, fmt.Errorf("CID verification failed: chunk data does not match CID (peer=%s, cid=%s, index=%d)", peerID, cid, chunkIndex)
		}

		// CID verified successfully - chunk is authentic
		return chunk, nil
	}

	if blockNotFound := respEnvelope.GetBlockNotFound(); blockNotFound != nil {
		return nil, fmt.Errorf("chunk not found: %s", blockNotFound.Reason)
	}

	return nil, fmt.Errorf("unexpected response type from peer")
}

// RequestChunks requests multiple chunks in parallel with pipelining
// Phase 5: Pipelining with max 16 concurrent requests ✅
// Phase 4: Updated to use peer list for failover ✅
func (c *P2PClient) RequestChunks(ctx context.Context, peerIDs []peer.ID, cid string, chunkIndices []int) (<-chan *protocol.ChunkResult, error) {
	if len(chunkIndices) == 0 {
		return nil, fmt.Errorf("no chunk indices provided")
	}
	if len(peerIDs) == 0 {
		return nil, fmt.Errorf("no peers provided")
	}

	// Create result channel (buffered to prevent goroutine blocking)
	resultChan := make(chan *protocol.ChunkResult, len(chunkIndices))

	// Use semaphore to limit concurrent requests to 16
	sem := make(chan struct{}, protocol.MaxConcurrentRequests)

	// WaitGroup to track goroutine completion
	var wg sync.WaitGroup

	// Launch goroutines for each chunk request
	for _, chunkIndex := range chunkIndices {
		wg.Add(1)

		go func(idx int) {
			defer wg.Done()

			// Acquire semaphore (blocks if 16 requests already in flight)
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }() // Release semaphore when done
			case <-ctx.Done():
				// SECURITY: Use select with ctx.Done() to prevent goroutine leaks on cancellation
				select {
				case resultChan <- &protocol.ChunkResult{
					Chunk:  nil,
					Error:  ctx.Err(),
					PeerID: peerIDs[0], // Use first peer for error reporting
				}:
				case <-ctx.Done():
				}
				return
			}

			// Check context before starting request
			if ctx.Err() != nil {
				// SECURITY: Use select with ctx.Done() to prevent goroutine leaks on cancellation
				select {
				case resultChan <- &protocol.ChunkResult{
					Chunk:  nil,
					Error:  ctx.Err(),
					PeerID: peerIDs[0], // Use first peer for error reporting
				}:
				case <-ctx.Done():
				}
				return
			}

			// Request chunk from peers (with retry logic from Phase 3 and peer failover from Phase 4)
			chunk, err := c.RequestChunk(ctx, peerIDs, cid, idx)

			// Send result to channel
			// Note: PeerID represents the first peer in the list; actual peer used may vary due to failover
			result := &protocol.ChunkResult{
				Chunk:  chunk,
				Error:  err,
				PeerID: peerIDs[0],
			}

			// SECURITY: Use select with ctx.Done() to prevent goroutine leaks on cancellation
			select {
			case resultChan <- result:
			case <-ctx.Done():
				return
			}
		}(chunkIndex)
	}

	// Close result channel after all goroutines complete
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	return resultChan, nil
}
