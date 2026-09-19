// Package: internal/daemon/download
// Feature: F-029 (P2P Download Parallelization)
// Story: US-029-P7 (Observability)
// Purpose: Metrics and structured logging for parallel download internals

package download

import (
	"log"
	"sync"
)

// Metrics tracks download parallelization observability counters and gauges.
// Thread-safe: all methods acquire mu before mutating state.
type Metrics struct {
	mu sync.Mutex

	// Lease events
	LeaseAcquired int
	LeaseExpired  int
	LeaseReleased int

	// Worker state (gauges — can go up and down)
	WorkersInFlight int
	WorkersIdle     int

	// Per-peer concurrency (gauge — tracks active requests per peer)
	PeerConcurrency map[string]int // peerID -> active requests

	// Retries (counter — per-chunk retry count)
	ChunkRetries map[int]int // chunkIndex -> retry count
}

// NewMetrics creates a new Metrics instance with initialized maps.
func NewMetrics() *Metrics {
	return &Metrics{
		PeerConcurrency: make(map[string]int),
		ChunkRetries:    make(map[int]int),
	}
}

// RecordLeaseAcquired increments lease acquisition counter.
func (m *Metrics) RecordLeaseAcquired(chunkIndex int, workerID int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.LeaseAcquired++
	log.Printf("[download.worker.%d] lease.acquired chunk=%d total=%d", workerID, chunkIndex, m.LeaseAcquired)
}

// RecordLeaseExpired increments lease expiry counter.
func (m *Metrics) RecordLeaseExpired(chunkIndex int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.LeaseExpired++
	log.Printf("[download.coordinator] lease.expired chunk=%d total=%d", chunkIndex, m.LeaseExpired)
}

// RecordLeaseReleased increments lease release counter.
func (m *Metrics) RecordLeaseReleased(chunkIndex int, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.LeaseReleased++
	log.Printf("[download.coordinator] lease.released chunk=%d reason=%s total=%d", chunkIndex, reason, m.LeaseReleased)
}

// RecordWorkerInFlight updates in-flight worker gauge.
func (m *Metrics) RecordWorkerInFlight(delta int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.WorkersInFlight += delta
	log.Printf("[download.metrics] worker.in_flight=%d", m.WorkersInFlight)
}

// RecordWorkerIdle updates idle worker gauge.
func (m *Metrics) RecordWorkerIdle(delta int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.WorkersIdle += delta
	log.Printf("[download.metrics] worker.idle=%d", m.WorkersIdle)
}

// RecordPeerConcurrency updates per-peer active request count.
func (m *Metrics) RecordPeerConcurrency(peerID string, delta int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.PeerConcurrency[peerID] += delta
	log.Printf("[download.metrics] peer.concurrency peer=%s active=%d", peerID, m.PeerConcurrency[peerID])
}

// RecordChunkRetry increments retry counter for a chunk.
func (m *Metrics) RecordChunkRetry(chunkIndex int, attempt int, errType string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.ChunkRetries[chunkIndex]++
	log.Printf("[download.worker] chunk.retry chunk=%d attempt=%d error_type=%s total_retries=%d",
		chunkIndex, attempt, errType, m.ChunkRetries[chunkIndex])
}

// RecordWorkerShutdown logs worker exit.
func (m *Metrics) RecordWorkerShutdown(workerID int, reason string, inFlightChunks int) {
	log.Printf("[download.worker.%d] worker.shutdown reason=%s in_flight_chunks=%d", workerID, reason, inFlightChunks)
}

// Snapshot returns current metric values as a map (for testing/export).
// Returns copies of internal maps to prevent external mutation.
func (m *Metrics) Snapshot() map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()

	return map[string]interface{}{
		"lease_acquired":    m.LeaseAcquired,
		"lease_expired":     m.LeaseExpired,
		"lease_released":    m.LeaseReleased,
		"workers_in_flight": m.WorkersInFlight,
		"workers_idle":      m.WorkersIdle,
		"peer_concurrency":  copyStringIntMap(m.PeerConcurrency),
		"chunk_retries":     copyIntIntMap(m.ChunkRetries),
	}
}

func copyStringIntMap(src map[string]int) map[string]int {
	dst := make(map[string]int, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func copyIntIntMap(src map[int]int) map[int]int {
	dst := make(map[int]int, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
