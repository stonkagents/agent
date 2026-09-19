// Package: internal/daemon/download
// Feature: F-029 (P2P Download Parallelization)
// Story: US-029-P5 (Worker Pool)
// Purpose: Parallel worker pool with chunk-isolated failure handling and bounded retry

package download

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// isRetryable determines if an error is transient and worth retrying.
// Permanent errors (corruption, validation) skip retries.
// Unknown errors default to retryable (optimistic).
func isRetryable(err error) bool {
	if err == nil {
		return false
	}

	errMsg := strings.ToLower(err.Error())

	// Permanent: corruption, validation failure — no retry
	permanentPatterns := []string{
		"corrupt",
		"validation failed",
		"cid mismatch",
		"invalid chunk",
	}
	for _, pattern := range permanentPatterns {
		if strings.Contains(errMsg, pattern) {
			return false
		}
	}

	// Retryable: network issues
	retryablePatterns := []string{
		"timeout",
		"connection refused",
		"peer disconnect",
		"rate limit",
		"temporary failure",
	}
	for _, pattern := range retryablePatterns {
		if strings.Contains(errMsg, pattern) {
			return true
		}
	}

	// Default: assume retryable for unknown errors
	return true
}

// classifyError returns a short error type label for metrics.
func classifyError(err error) string {
	if err == nil {
		return "none"
	}

	errMsg := strings.ToLower(err.Error())

	if strings.Contains(errMsg, "timeout") {
		return "network_timeout"
	}
	if strings.Contains(errMsg, "disconnect") {
		return "peer_disconnect"
	}
	if strings.Contains(errMsg, "rate limit") {
		return "rate_limit"
	}
	if strings.Contains(errMsg, "corrupt") {
		return "corruption"
	}
	if strings.Contains(errMsg, "validation") {
		return "validation_failure"
	}

	return "unknown"
}

// runDownloadWorkerPool starts N workers downloading chunks in parallel.
// Workers run independently — one worker failure does NOT terminate others.
// Download succeeds when all chunks complete; fails if any worker exhausts
// its retry budget on a chunk.
func (m *Manager) runDownloadWorkerPool(ctx context.Context, cid string, totalChunks int) error {
	m.mu.RLock()
	workerCount := m.config.WorkerCount
	inactivityTimeout := time.Duration(m.InactivityTimeoutSec) * time.Second
	metrics := m.metrics
	m.mu.RUnlock()

	// Shared progress counters (atomic — safe for concurrent workers)
	var completedCount atomic.Int32
	var downloadedTotal atomic.Int64
	var lastChunkTime atomic.Int64
	lastChunkTime.Store(time.Now().UnixNano())
	startTime := time.Now()

	// Cancellable context for inactivity timeout
	poolCtx, poolCancel := context.WithCancelCause(ctx)
	defer poolCancel(nil)

	// Inactivity watchdog: cancels download if no chunk completes within timeout
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-poolCtx.Done():
				return
			case <-ticker.C:
				last := time.Unix(0, lastChunkTime.Load())
				if time.Since(last) > inactivityTimeout {
					poolCancel(errInactivityTimeout)
					return
				}
			}
		}
	}()

	var wg sync.WaitGroup
	errorsChan := make(chan error, workerCount)

	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		workerID := i
		go func() {
			defer wg.Done()
			if err := m.downloadWorker(poolCtx, cid, workerID, &completedCount, &downloadedTotal, &lastChunkTime, startTime); err != nil {
				errorsChan <- err
			}
		}()
	}

	wg.Wait()
	close(errorsChan)

	// Collect errors
	var workerErrors []error
	for err := range errorsChan {
		workerErrors = append(workerErrors, err)
	}

	// Success: all chunks downloaded
	m.mu.RLock()
	p2p := m.p2p
	m.mu.RUnlock()

	if p2p != nil && m.coordinatorFor(cid) != nil && m.coordinatorFor(cid).AllChunksComplete() {
		return nil
	}

	// Failure: report what went wrong
	finalCompleted := 0
	if p2p != nil && m.coordinatorFor(cid) != nil {
		finalCompleted = len(m.coordinatorFor(cid).GetCompletedChunks())
	}
	remaining := totalChunks - finalCompleted

	if len(workerErrors) > 0 {
		if metrics != nil {
			metrics.RecordWorkerShutdown(-1, "pool_failed", remaining)
		}
		return fmt.Errorf("%d workers failed, %d chunks incomplete: %v",
			len(workerErrors), remaining, workerErrors[0])
	}

	return fmt.Errorf("download stalled: no peers available for %d remaining chunks", remaining)
}

// downloadWorker runs in a goroutine, acquiring and downloading chunks until
// none are available or the context is cancelled. Updates shared progress
// counters after each successful chunk for live status reporting.
func (m *Manager) downloadWorker(ctx context.Context, cid string, workerID int,
	completedCount *atomic.Int32, downloadedTotal *atomic.Int64,
	lastChunkTime *atomic.Int64, startTime time.Time) error {
	m.mu.RLock()
	retryBudget := m.config.RetryBudget
	backoffBase := m.config.RetryBackoffBase
	metrics := m.metrics
	p2p := m.p2p
	m.mu.RUnlock()

	if p2p == nil || m.coordinatorFor(cid) == nil {
		return fmt.Errorf("worker %d: P2P dependencies not initialized", workerID)
	}

	for {
		// Check context cancellation
		select {
		case <-ctx.Done():
			if metrics != nil {
				metrics.RecordWorkerShutdown(workerID, "cancelled", 0)
			}
			return ctx.Err()
		default:
		}

		// Check if all chunks are complete (endgame: other workers may have finished)
		if m.coordinatorFor(cid).AllChunksComplete() {
			return nil
		}

		// Get next chunk (lease reservation + rarest-first; endgame ignores leases)
		chunkIndex, err := m.coordinatorFor(cid).GetNextChunk()
		if err != nil {
			// No more chunks — worker exits gracefully
			if metrics != nil {
				metrics.RecordWorkerShutdown(workerID, "idle_no_chunks", 0)
			}
			return nil
		}

		// In endgame mode, skip if already completed by another worker
		if m.coordinatorFor(cid).IsComplete(chunkIndex) {
			continue
		}

		if metrics != nil {
			metrics.RecordWorkerInFlight(1)
		}

		// Find peers for this chunk
		allPeers := m.coordinatorFor(cid).GetPeersForChunk(chunkIndex)
		if len(allPeers) == 0 {
			if metrics != nil {
				metrics.RecordWorkerInFlight(-1)
			}
			m.coordinatorFor(cid).ReleaseLease(chunkIndex)
			continue
		}

		// Random peer selection for load distribution across the swarm
		selectedPeer := allPeers[rand.Intn(len(allPeers))]

		if metrics != nil {
			metrics.RecordPeerConcurrency(selectedPeer, 1)
		}

		// Download with retry budget
		var downloadErr error
		for attempt := 0; attempt < retryBudget; attempt++ {
			if attempt > 0 {
				// Exponential backoff with jitter
				backoff := time.Duration(1<<uint(attempt-1)) * backoffBase
				jitter := time.Duration(rand.Intn(100)) * time.Millisecond
				select {
				case <-time.After(backoff + jitter):
				case <-ctx.Done():
					downloadErr = ctx.Err()
					break
				}
				if ctx.Err() != nil {
					downloadErr = ctx.Err()
					break
				}
				if metrics != nil {
					metrics.RecordChunkRetry(chunkIndex, attempt, classifyError(downloadErr))
				}
			}

			downloadErr = m.downloadChunkFromPeer(ctx, selectedPeer, cid, chunkIndex)
			if downloadErr == nil {
				break
			}

			if !isRetryable(downloadErr) {
				break
			}
		}

		// Always decrement counters
		if metrics != nil {
			metrics.RecordPeerConcurrency(selectedPeer, -1)
			metrics.RecordWorkerInFlight(-1)
		}

		if downloadErr != nil {
			m.coordinatorFor(cid).ReleaseLease(chunkIndex)
			return fmt.Errorf("chunk %d failed after %d attempts: %w", chunkIndex, retryBudget, downloadErr)
		}

		// Success — mark complete (releases lease automatically)
		m.coordinatorFor(cid).MarkChunkComplete(chunkIndex)

		// Endgame trigger: when few chunks remain, allow duplicate downloads (TD-057)
		if !m.coordinatorFor(cid).IsEndgame() && m.coordinatorFor(cid).ShouldEnterEndgame() {
			m.coordinatorFor(cid).EnterEndgame()
		}

		// Update shared progress counters + live status
		lastChunkTime.Store(time.Now().UnixNano())
		newCount := completedCount.Add(1)

		// Estimate downloaded bytes from chunk data size (downloadChunkFromPeer stored it)
		chunkData, _ := p2p.ChunkStore.GetChunk(cid, chunkIndex)
		chunkBytes := int64(len(chunkData))
		newBytes := downloadedTotal.Add(chunkBytes)

		// Calculate speed
		elapsed := time.Since(startTime).Seconds()
		var speedBPS int64
		if elapsed > 0 {
			speedBPS = int64(float64(newBytes) / elapsed)
		}

		_ = m.UpdateProgress(cid, int(newCount), newBytes, speedBPS)
	}
}

// downloadChunkFromPeer downloads a single chunk from a specific peer,
// stores it, and updates the repository.
func (m *Manager) downloadChunkFromPeer(ctx context.Context, peerIDStr string, cid string, chunkIndex int) error {
	m.mu.RLock()
	p2p := m.p2p
	m.mu.RUnlock()

	if p2p == nil || p2p.P2PClient == nil || p2p.ChunkStore == nil {
		return fmt.Errorf("downloadChunkFromPeer: P2P dependencies not initialized")
	}

	// Convert string peer ID to peer.ID
	pid, err := peer.Decode(peerIDStr)
	if err != nil {
		return fmt.Errorf("invalid peer ID %q: %w", peerIDStr, err)
	}

	// Ensure connection via PeerConnector (cached)
	if p2p.PeerConnector != nil {
		if err := m.ensurePeerConnection(cid, pid); err != nil {
			return fmt.Errorf("connection to %s failed: %w", peerIDStr, err)
		}
	}

	// Request chunk
	chunk, err := p2p.P2PClient.RequestChunk(ctx, []peer.ID{pid}, cid, chunkIndex)
	if err != nil {
		return fmt.Errorf("request chunk %d failed: %w", chunkIndex, err)
	}

	if chunk == nil || chunk.Data == nil {
		return fmt.Errorf("received nil chunk or data for chunk %d", chunkIndex)
	}

	// Download bandwidth cap (setup surface); no-op when unlimited
	if err := m.waitBandwidth(ctx, int64(len(chunk.Data))); err != nil {
		return fmt.Errorf("bandwidth throttle: %w", err)
	}

	// Store chunk
	if err := p2p.ChunkStore.StoreChunk(cid, chunkIndex, chunk.Data); err != nil {
		return fmt.Errorf("store chunk %d failed: %w", chunkIndex, err)
	}

	// Persist chunk completion (best-effort — coordinator tracks in-memory,
	// repo persistence is for crash recovery only)
	_ = m.repo.UpdateChunkCompletion(cid, chunkIndex, true)

	return nil
}
