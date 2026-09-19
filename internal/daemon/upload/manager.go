// Package: internal/daemon/upload
// Feature: F-010 (P2P Transfer Protocol)
// Story: US-010-07 (Upload Queue & Seeding)
// Purpose: Upload manager with fair queuing for P2P seeding

package upload

import (
	"context"
	"sync"

	"github.com/stonkagents/agent/internal/daemon/download"
)

// UploadRequest represents a chunk upload request
type UploadRequest struct {
	PeerID     string
	FileCID    string
	ChunkIndex int
}

// UploadStats tracks upload statistics for a file
type UploadStats struct {
	FileCID        string
	BytesSent      int64
	RequestsServed int
}

// UploadStatusDetail is the enriched status returned by GET /api/v1/uploads/status.
// Combines per-file stats with active state for REST polling.
type UploadStatusDetail struct {
	FileCID        string `json:"file_cid"`
	Filename       string `json:"filename"`
	BytesSent      int64  `json:"bytes_sent"`
	RequestsServed int    `json:"requests_served"`
	Active         bool   `json:"active"`
}

// Manager manages upload queue with fair queuing (round-robin)
type Manager struct {
	maxConcurrent  int
	activeUploads  map[string]bool // uploadID -> bool
	queue          []*UploadRequest
	peerQueues     map[string][]*UploadRequest // Fair queuing: per-peer queues
	peerOrder      []string                    // Round-robin order
	stats          map[string]*UploadStats     // fileCID -> stats
	bandwidthLimit int64                       // Bytes per second (0 = unlimited)
	throttle       *download.Throttle          // Upload cap token bucket (nil = unlimited)
	mu             sync.RWMutex
}

// NewManager creates a new upload manager
func NewManager(maxConcurrent int) *Manager {
	return &Manager{
		maxConcurrent:  maxConcurrent,
		activeUploads:  make(map[string]bool),
		queue:          make([]*UploadRequest, 0),
		peerQueues:     make(map[string][]*UploadRequest),
		peerOrder:      make([]string, 0),
		stats:          make(map[string]*UploadStats),
		bandwidthLimit: 0,
		throttle:       nil,
	}
}

// QueueUpload adds an upload request to the fair queue
func (m *Manager) QueueUpload(peerID, fileCID string, chunkIndex int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	request := &UploadRequest{
		PeerID:     peerID,
		FileCID:    fileCID,
		ChunkIndex: chunkIndex,
	}

	// Add to peer-specific queue for fair scheduling
	if m.peerQueues[peerID] == nil {
		m.peerQueues[peerID] = make([]*UploadRequest, 0)
		m.peerOrder = append(m.peerOrder, peerID)
	}

	m.peerQueues[peerID] = append(m.peerQueues[peerID], request)
	m.queue = append(m.queue, request)

	return nil
}

// GetNextUpload returns the next upload using fair queuing (round-robin)
func (m *Manager) GetNextUpload() *UploadRequest {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Round-robin through peer queues
	for len(m.peerOrder) > 0 {
		// Get first peer in order
		peerID := m.peerOrder[0]
		peerQueue := m.peerQueues[peerID]

		if len(peerQueue) == 0 {
			// Remove empty peer queue
			delete(m.peerQueues, peerID)
			m.peerOrder = m.peerOrder[1:]
			continue
		}

		// Get first request from this peer's queue
		request := peerQueue[0]
		m.peerQueues[peerID] = peerQueue[1:]

		// Move peer to end of order (round-robin)
		m.peerOrder = append(m.peerOrder[1:], peerID)

		return request
	}

	return nil
}

// GetQueueSize returns the number of queued uploads
func (m *Manager) GetQueueSize() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return len(m.queue)
}

// GetPendingCount returns the number of queued upload requests.
func (m *Manager) GetPendingCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	pending := 0
	for _, q := range m.peerQueues {
		pending += len(q)
	}

	return pending
}

// StartUpload marks an upload as active
func (m *Manager) StartUpload(uploadID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.activeUploads[uploadID] = true
}

// CompleteUpload marks an upload as complete and frees slot
func (m *Manager) CompleteUpload(uploadID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.activeUploads, uploadID)
}

// GetActiveCount returns the number of active uploads
func (m *Manager) GetActiveCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return len(m.activeUploads)
}

// CanStartUpload checks if a new upload can start
func (m *Manager) CanStartUpload() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return len(m.activeUploads) < m.maxConcurrent
}

// RecordUpload records upload statistics
func (m *Manager) RecordUpload(fileCID string, bytesSent int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.stats[fileCID] == nil {
		m.stats[fileCID] = &UploadStats{
			FileCID: fileCID,
		}
	}

	m.stats[fileCID].BytesSent += bytesSent
	m.stats[fileCID].RequestsServed++
}

// GetStats returns upload statistics for a file
func (m *Manager) GetStats(fileCID string) *UploadStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := m.stats[fileCID]
	if stats == nil {
		return nil
	}

	// Return copy
	statsCopy := *stats
	return &statsCopy
}

// GetActiveUploads returns list of active upload IDs (for REST polling)
func (m *Manager) GetActiveUploads() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	uploads := make([]string, 0, len(m.activeUploads))
	for uploadID := range m.activeUploads {
		uploads = append(uploads, uploadID)
	}

	return uploads
}

// GetDetailedUploads returns enriched upload status for all files with stats.
// Combines per-file stats with active upload state for REST API response.
func (m *Manager) GetDetailedUploads() []UploadStatusDetail {
	m.mu.RLock()
	defer m.mu.RUnlock()

	details := make([]UploadStatusDetail, 0, len(m.stats))
	for fileCID, stats := range m.stats {
		details = append(details, UploadStatusDetail{
			FileCID:        fileCID,
			Filename:       "", // Filename not tracked in stats yet; enriched by caller if available
			BytesSent:      stats.BytesSent,
			RequestsServed: stats.RequestsServed,
			Active:         m.activeUploads[fileCID],
		})
	}
	return details
}

// SetBandwidthLimit sets the upload bandwidth limit (bytes/sec, 0 = unlimited).
// Applied live: the block-exchange chunk provider calls WaitUpload before each
// chunk is handed to a peer (see daemon.chunkProviderAdapter).
func (m *Manager) SetBandwidthLimit(bytesPerSecond int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.bandwidthLimit = bytesPerSecond

	if bytesPerSecond > 0 {
		m.throttle = download.NewThrottle(bytesPerSecond)
	} else {
		m.throttle = nil
	}
}

// BandwidthLimit returns the current upload cap in bytes/second (0 = unlimited).
func (m *Manager) BandwidthLimit() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.bandwidthLimit
}

// AllowUpload reports whether bytes can be sent now without exceeding the
// cap and, when so, consumes them (non-blocking).
func (m *Manager) AllowUpload(bytes int64) bool {
	m.mu.RLock()
	throttle := m.throttle
	m.mu.RUnlock()

	// No limit
	if throttle == nil {
		return true
	}
	return throttle.Allow(bytes)
}

// WaitUpload blocks until bytes may be sent under the cap (or ctx ends) and
// consumes them. Unlimited when no cap is set.
func (m *Manager) WaitUpload(ctx context.Context, bytes int64) error {
	m.mu.RLock()
	throttle := m.throttle
	m.mu.RUnlock()
	return throttle.Wait(ctx, bytes) // nil throttle = unlimited
}
