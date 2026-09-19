// Package: internal/daemon/download
// Feature: F-010 (P2P Transfer Protocol)
// Story: US-010-06 (Chunked Download Manager)
// Purpose: Download manager for state machine + queue

package download

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stonkagents/agent/pkg/protocol"
)

// cidCharsetRe validates that a CID contains only alphanumeric characters.
// SECURITY: Rejects path traversal chars (/, ..), null bytes, spaces.
var cidCharsetRe = regexp.MustCompile(`^[a-zA-Z0-9]+$`)

// Typed cancel causes — checked by workers via context.Cause(ctx) + errors.Is()
var (
	errUserPause         = errors.New("user paused download")
	errUserCancel        = errors.New("user cancelled download")
	errInactivityTimeout = errors.New("inactivity timeout")
)

// DownloadStatus represents the runtime status of a download
type DownloadStatus struct {
	CID             string        `json:"cid"`
	Filename        string        `json:"filename"`
	State           DownloadState `json:"state"`
	TotalSize       int64         `json:"total_size"`
	TotalChunks     int           `json:"total_chunks"`
	CompletedChunks int           `json:"completed_chunks"`
	DownloadedBytes int64         `json:"downloaded_bytes"`
	SpeedBPS        int64         `json:"speed_bps"`
	Progress        float64       `json:"progress"`
	ETASEC          int64         `json:"eta_seconds"`
	CompletedAt     *time.Time    `json:"completed_at"`
	ErrorMessage    *string       `json:"error_message"`
	ConnectedPeers  int           `json:"connected_peers"`
}

// P2PDependencies holds external dependencies for P2P transfer orchestration
type P2PDependencies struct {
	ChunkStore          ChunkStoreInterface
	TrackerClient       TrackerClientInterface
	P2PClient           P2PClientInterface // Modern P2P client with proper types (context, []peer.ID, *protocol.Chunk)
	Coordinator         *Coordinator
	PeerConnector       PeerConnector
	DHTContentDiscovery DHTContentDiscoveryInterface // Optional: DHT FindProviders for content (BitTorrent-style)
}

// DHTContentDiscoveryInterface finds peers that provide content via DHT (BitTorrent get_peers equivalent).
// Optional: if nil, discovery uses tracker only. If set, searchAndRegisterPeers merges tracker + DHT peers.
type DHTContentDiscoveryInterface interface {
	FindProvidersForContent(ctx context.Context, cid string, totalChunks int, limit int) ([]PeerInfo, error)
}

// ChunkStoreInterface defines chunk storage operations
type ChunkStoreInterface interface {
	StoreChunk(cid string, chunkIndex int, data []byte) error
	GetChunk(cid string, chunkIndex int) ([]byte, error)
	HasChunk(cid string, chunkIndex int) (bool, error)
}

// TrackerClientInterface defines tracker operations
type TrackerClientInterface interface {
	SearchByCID(cid string) ([]PeerInfo, error)
	UpdateAvailability(cid string, chunks []int) error
}

// P2PClientInterface defines P2P client operations for chunk requests
// Phase 1: Modern interface with proper types (context.Context, []peer.ID, *protocol.Chunk)
type P2PClientInterface interface {
	RequestChunk(ctx context.Context, peerIDs []peer.ID, cid string, chunkIndex int) (*protocol.Chunk, error)
	RequestChunks(ctx context.Context, peerIDs []peer.ID, cid string, chunkIndices []int) (<-chan *protocol.ChunkResult, error)
}

// PeerConnector defines how to connect to a peer using multiaddrs.
type PeerConnector interface {
	ConnectToPeer(peerID peer.ID, addrs []string) error
}

// DownloadCompletionCallback is called when a download completes successfully.
// Fields are passed directly to avoid deadlock (callback runs inside m.mu.Lock).
type DownloadCompletionCallback func(cid, filename string, totalBytes, totalSize int64)

// DownloadFailureCallback is called when a download fails.
// Fields are passed directly to avoid deadlock (callback runs inside m.mu.Lock).
type DownloadFailureCallback func(cid, filename string, totalSize int64, errorMessage string)

// workerHandle holds the cancel function and done channel for a per-download goroutine.
// The done channel is closed by the worker when it exits (regardless of reason).
// CancelDownload/PauseDownload cancel the context, then wait on done before cleanup.
type workerHandle struct {
	cancel context.CancelCauseFunc
	done   chan struct{} // closed by worker on exit
}

// Manager orchestrates download queue and state transitions
type Manager struct {
	baseDir              string
	maxConcurrent        int
	InactivityTimeoutSec int // Default 90s, configurable (AC-26)
	repo                 *Repository
	downloads            map[string]*DownloadStatus
	workers              map[string]*workerHandle // per-CID worker goroutine handles
	mu                   sync.RWMutex
	p2p                  *P2PDependencies

	// coordinators holds one Coordinator per in-flight download, keyed by CID.
	// P2PDependencies.Coordinator is only a configuration template: sharing a
	// single coordinator across concurrent downloads (maxConcurrent > 1) made
	// each new download Reset() the chunk map of the ones still running.
	coordMu      sync.Mutex
	coordinators map[string]*Coordinator
	workerDone   chan struct{}
	stopOnce     sync.Once // TD-035: prevents double-close panic on workerDone channel
	wg           sync.WaitGroup
	onComplete   DownloadCompletionCallback // Optional callback for download completion
	onFailure    DownloadFailureCallback    // Optional callback for download failure

	config  *DownloadConfig // Parallelization knobs (US-029-P6)
	metrics *Metrics        // Observability counters (US-029-P7)

	// Connection caching: prevents redundant ConnectToPeer calls in hot path (US-029-P3)
	connMu         sync.RWMutex
	connectedPeers map[string]map[peer.ID]bool // cid -> peer -> connected

	// Download bandwidth cap (setup surface: POST /api/v1/setup/bandwidth).
	throttleMu sync.RWMutex
	throttle   *Throttle // nil = unlimited
}

// SetBandwidthLimit caps received chunk bytes per second (0 = unlimited).
// Applied live: workers consult the throttle before storing each chunk.
func (m *Manager) SetBandwidthLimit(bytesPerSecond int64) {
	m.throttleMu.Lock()
	defer m.throttleMu.Unlock()
	if bytesPerSecond <= 0 {
		m.throttle = nil
		return
	}
	m.throttle = NewThrottle(bytesPerSecond)
}

// BandwidthLimit returns the current download cap in bytes/second (0 = unlimited).
func (m *Manager) BandwidthLimit() int64 {
	m.throttleMu.RLock()
	defer m.throttleMu.RUnlock()
	if m.throttle == nil {
		return 0
	}
	return m.throttle.rateLimit
}

func (m *Manager) waitBandwidth(ctx context.Context, bytes int64) error {
	m.throttleMu.RLock()
	t := m.throttle
	m.throttleMu.RUnlock()
	return t.Wait(ctx, bytes)
}

// SetCompletionCallback sets a callback to be called when downloads complete.
func (m *Manager) SetCompletionCallback(callback DownloadCompletionCallback) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onComplete = callback
}

// SetFailureCallback sets a callback to be called when downloads fail.
func (m *Manager) SetFailureCallback(callback DownloadFailureCallback) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onFailure = callback
}

// SetDownloadConfig overrides the download parallelization configuration.
// Thread-safe: acquires write lock before updating.
func (m *Manager) SetDownloadConfig(cfg *DownloadConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.config = cfg
}

// SetMetrics injects a Metrics instance for observability.
func (m *Manager) SetMetrics(metrics *Metrics) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.metrics = metrics
}

// NewManager creates a new download manager
func NewManager(baseDir string, maxConcurrent int) *Manager {
	return &Manager{
		baseDir:              baseDir,
		maxConcurrent:        maxConcurrent,
		InactivityTimeoutSec: 90, // AC-26: default 90s
		repo:                 NewRepository(baseDir),
		downloads:            make(map[string]*DownloadStatus),
		workers:              make(map[string]*workerHandle),
		config:               DefaultDownloadConfig(),
		metrics:              NewMetrics(),
		connectedPeers:       make(map[string]map[peer.ID]bool),
	}
}

// QueueDownload adds a new download to the queue in StateQueued
func (m *Manager) QueueDownload(cid, filename string, totalSize int64, totalChunks int) error {
	// SECURITY: Validate inputs
	if cid == "" {
		return fmt.Errorf("queue download failed: invalid CID: empty string")
	}
	if filename == "" {
		return fmt.Errorf("queue download failed: invalid filename: empty string")
	}
	if totalSize <= 0 {
		return fmt.Errorf("queue download failed: invalid totalSize: %d (must be > 0)", totalSize)
	}
	if totalChunks <= 0 {
		return fmt.Errorf("queue download failed: invalid totalChunks: %d (must be > 0)", totalChunks)
	}

	// SECURITY: Sanitize filename to prevent path traversal
	sanitizedFilename := sanitizeFilename(filename)
	if sanitizedFilename == "" {
		return fmt.Errorf("queue download failed: invalid filename after sanitization: %s", filename)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if download already exists
	if _, exists := m.downloads[cid]; exists {
		return fmt.Errorf("queue download failed: download already queued for CID %s", cid)
	}

	// Create download status (use sanitized filename)
	status := &DownloadStatus{
		CID:         cid,
		Filename:    sanitizedFilename,
		State:       StateQueued,
		TotalSize:   totalSize,
		TotalChunks: totalChunks,
		Progress:    0.0,
	}

	// Create metadata for persistence (use sanitized filename)
	chunkSize := 262144 // 256 KB default
	if totalChunks > 0 && totalSize > 0 {
		chunkSize = int(totalSize / int64(totalChunks))
	}

	metadata := &DownloadMetadata{
		CID:         cid,
		Filename:    sanitizedFilename,
		TotalSize:   totalSize,
		TotalChunks: totalChunks,
		ChunkSize:   chunkSize,
		State:       StateQueued,
		ChunksMap:   make([]bool, totalChunks),
	}

	// Persist metadata
	if err := m.repo.SaveMetadata(metadata); err != nil {
		return fmt.Errorf("failed to save metadata: %w", err)
	}

	// Add to in-memory map
	m.downloads[cid] = status

	return nil
}

// StartDownload transitions download from queued → active
func (m *Manager) StartDownload(cid string) error {
	// SECURITY: Validate input
	if cid == "" {
		return fmt.Errorf("start download failed: invalid CID: empty string")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if download exists
	status, exists := m.downloads[cid]
	if !exists {
		return fmt.Errorf("start download failed: download not found for CID %s", cid)
	}

	// Check if already active
	if status.State == StateActive {
		return nil // Already active, no-op
	}

	// Check max concurrent limit
	activeCount := m.countActiveDownloadsLocked()
	if activeCount >= m.maxConcurrent {
		return fmt.Errorf("max concurrent downloads reached (%d/%d)", activeCount, m.maxConcurrent)
	}

	// Transition to active
	status.State = StateActive

	// Update metadata atomically
	if err := m.repo.UpdateMetadata(cid, func(metadata *DownloadMetadata) error {
		metadata.State = StateActive
		return nil
	}); err != nil {
		return fmt.Errorf("failed to update metadata: %w", err)
	}

	return nil
}

// PauseDownload transitions download from active → paused.
// If a worker goroutine is running for this CID, cancels its context with errUserPause
// and waits for it to exit before returning.
func (m *Manager) PauseDownload(cid string) error {
	// SECURITY: Validate input
	if cid == "" {
		return fmt.Errorf("pause download failed: invalid CID: empty string")
	}

	m.mu.Lock()

	status, exists := m.downloads[cid]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("pause download failed: download not found for CID %s", cid)
	}

	// Cancel worker context with pause cause
	wh, hasWorker := m.workers[cid]
	if hasWorker {
		wh.cancel(errUserPause)
		delete(m.workers, cid)
	}

	// Set state to paused
	status.State = StatePaused

	// Update persisted metadata atomically
	if err := m.repo.UpdateMetadata(cid, func(metadata *DownloadMetadata) error {
		metadata.State = StatePaused
		return nil
	}); err != nil {
		m.mu.Unlock()
		return fmt.Errorf("failed to update metadata: %w", err)
	}

	m.mu.Unlock()

	// Wait for worker to exit (outside lock to avoid deadlock)
	if hasWorker {
		select {
		case <-wh.done:
			// Worker exited cleanly
		case <-time.After(5 * time.Second):
			// Worker didn't exit in time — non-fatal
		}
	}

	return nil
}

// ResumeDownload transitions download from paused → active
func (m *Manager) ResumeDownload(cid string) error {
	// SECURITY: Validate input
	if cid == "" {
		return fmt.Errorf("resume download failed: invalid CID: empty string")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	status, exists := m.downloads[cid]
	if !exists {
		return fmt.Errorf("resume download failed: download not found for CID %s", cid)
	}

	// Check max concurrent limit
	activeCount := m.countActiveDownloadsLocked()
	if activeCount >= m.maxConcurrent {
		return fmt.Errorf("max concurrent downloads reached (%d/%d)", activeCount, m.maxConcurrent)
	}

	status.State = StateActive

	// Update metadata atomically
	if err := m.repo.UpdateMetadata(cid, func(metadata *DownloadMetadata) error {
		metadata.State = StateActive
		return nil
	}); err != nil {
		return fmt.Errorf("failed to update metadata: %w", err)
	}

	return nil
}

// CompleteDownload transitions download to completed state
func (m *Manager) CompleteDownload(cid string) error {
	// SECURITY: Validate input
	if cid == "" {
		return fmt.Errorf("complete download failed: invalid CID: empty string")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	status, exists := m.downloads[cid]
	if !exists {
		return fmt.Errorf("complete download failed: download not found for CID %s", cid)
	}

	status.State = StateCompleted
	now := time.Now()
	status.CompletedAt = &now
	status.Progress = 1.0

	// Update metadata atomically
	if err := m.repo.UpdateMetadata(cid, func(metadata *DownloadMetadata) error {
		metadata.State = StateCompleted
		return nil
	}); err != nil {
		return fmt.Errorf("failed to update metadata: %w", err)
	}

	// Clean up per-download lock to prevent memory leak
	m.repo.CleanupDownloadLock(cid)

	// Clean up connection cache (US-029-P3)
	m.clearConnectionCache(cid)

	// Call completion callback if set (pass fields to avoid deadlock — callback runs inside lock)
	if m.onComplete != nil {
		m.onComplete(cid, status.Filename, status.DownloadedBytes, status.TotalSize)
	}

	return nil
}

// FailDownload transitions download to failed state with error message
func (m *Manager) FailDownload(cid, errorMessage string) error {
	// SECURITY: Validate input
	if cid == "" {
		return fmt.Errorf("fail download failed: invalid CID: empty string")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	status, exists := m.downloads[cid]
	if !exists {
		return fmt.Errorf("fail download failed: download not found for CID %s", cid)
	}

	status.State = StateFailed
	status.ErrorMessage = &errorMessage

	// Update metadata atomically
	if err := m.repo.UpdateMetadata(cid, func(metadata *DownloadMetadata) error {
		metadata.State = StateFailed
		return nil
	}); err != nil {
		return fmt.Errorf("failed to update metadata: %w", err)
	}

	// Call failure callback if set (pass fields to avoid deadlock — callback runs inside lock)
	if m.onFailure != nil {
		m.onFailure(cid, status.Filename, status.TotalSize, errorMessage)
	}

	return nil
}

// ValidateCID checks CID format: alphanumeric only, bafy/Qm prefix, 46-62 chars.
// Exported so HTTP handlers can call it at the API boundary.
func ValidateCID(cid string) error {
	if len(cid) < 46 || len(cid) > 62 {
		return fmt.Errorf("invalid CID: length %d outside range 46-62", len(cid))
	}
	if !cidCharsetRe.MatchString(cid) {
		return fmt.Errorf("invalid CID: contains disallowed characters")
	}
	if !(len(cid) >= 4 && cid[:4] == "bafy") && !(len(cid) >= 2 && cid[:2] == "Qm") {
		return fmt.Errorf("invalid CID: must start with 'bafy' or 'Qm'")
	}
	return nil
}

// RetryDownload resets a failed download back to StateQueued so the worker re-processes it.
// AC-7: Only works on StateFailed. Resets progress to zero (full re-download).
func (m *Manager) RetryDownload(cid string) error {
	if cid == "" {
		return fmt.Errorf("retry download failed: invalid CID: empty string")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	status, exists := m.downloads[cid]
	if !exists {
		return fmt.Errorf("retry download failed: download not found for CID %s", cid)
	}

	if status.State != StateFailed {
		return fmt.Errorf("retry download failed: can only retry failed downloads, current state: %s", status.State)
	}

	// Reset runtime status — full restart, not resume (partial chunks may be corrupt)
	status.State = StateQueued
	status.ErrorMessage = nil
	status.Progress = 0.0
	status.DownloadedBytes = 0
	status.CompletedChunks = 0
	status.SpeedBPS = 0
	status.ETASEC = 0

	// Update persisted metadata atomically
	if err := m.repo.UpdateMetadata(cid, func(metadata *DownloadMetadata) error {
		metadata.State = StateQueued
		metadata.ChunksMap = make([]bool, metadata.TotalChunks)
		return nil
	}); err != nil {
		return fmt.Errorf("failed to save metadata for retry: %w", err)
	}

	return nil
}

// CancelDownload implements two-phase cancel (AC-24):
// 1. Cancel worker context with errUserCancel
// 2. Wait for worker ack via done channel (5s timeout)
// 3. Delete metadata + remove from in-memory map
func (m *Manager) CancelDownload(cid string) error {
	if cid == "" {
		return fmt.Errorf("cancel download failed: invalid CID: empty string")
	}

	m.mu.Lock()

	_, exists := m.downloads[cid]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("cancel download failed: download not found for CID %s", cid)
	}

	// Phase 1: Cancel worker context (if running)
	wh, hasWorker := m.workers[cid]
	if hasWorker {
		wh.cancel(errUserCancel)
	}

	m.mu.Unlock()

	// Phase 2: Wait for worker to acknowledge cancellation (outside lock)
	if hasWorker {
		select {
		case <-wh.done:
			// Worker exited cleanly
		case <-time.After(5 * time.Second):
			// Worker didn't exit in time — proceed with cleanup anyway
		}
	}

	// Phase 3: Re-lock and clean up
	m.mu.Lock()
	defer m.mu.Unlock()

	// Delete persisted metadata + chunk directory
	_ = m.repo.Delete(cid)

	// Clean up per-download lock to prevent memory leak
	m.repo.CleanupDownloadLock(cid)

	// Clean up connection cache (US-029-P3)
	m.clearConnectionCache(cid)

	// Remove from in-memory maps
	delete(m.downloads, cid)
	delete(m.workers, cid)

	return nil
}

// PauseAll pauses all active downloads. Returns count of paused downloads.
func (m *Manager) PauseAll() int {
	m.mu.RLock()
	var activeCIDs []string
	for cid, status := range m.downloads {
		if status.State == StateActive {
			activeCIDs = append(activeCIDs, cid)
		}
	}
	m.mu.RUnlock()

	paused := 0
	for _, cid := range activeCIDs {
		if err := m.PauseDownload(cid); err == nil {
			paused++
		}
	}
	return paused
}

// ResumeAll resumes all paused downloads. Returns count of resumed downloads.
func (m *Manager) ResumeAll() int {
	m.mu.RLock()
	var pausedCIDs []string
	for cid, status := range m.downloads {
		if status.State == StatePaused {
			pausedCIDs = append(pausedCIDs, cid)
		}
	}
	m.mu.RUnlock()

	resumed := 0
	for _, cid := range pausedCIDs {
		if err := m.ResumeDownload(cid); err == nil {
			resumed++
		}
	}
	return resumed
}

// ClearCompleted removes all completed and failed downloads from the manager.
// Returns count of cleared downloads.
func (m *Manager) ClearCompleted() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	var toRemove []string
	for cid, status := range m.downloads {
		if status.State == StateCompleted || status.State == StateFailed {
			toRemove = append(toRemove, cid)
		}
	}

	for _, cid := range toRemove {
		_ = m.repo.Delete(cid)
		m.repo.CleanupDownloadLock(cid)
		m.clearConnectionCache(cid)
		delete(m.downloads, cid)
		delete(m.workers, cid)
	}

	return len(toRemove)
}

// GetStatus returns the current status of a download
func (m *Manager) GetStatus(cid string) *DownloadStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	status, exists := m.downloads[cid]
	if !exists {
		return nil
	}

	// Return a copy to prevent external mutation
	statusCopy := *status
	statusCopy.ConnectedPeers = m.getConnectedPeerCount(cid)
	return &statusCopy
}

// GetActiveDownloads returns all downloads that are queued or active (for REST polling).
// Deprecated: Use GetVisibleDownloads which also includes paused downloads.
func (m *Manager) GetActiveDownloads() []*DownloadStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	active := make([]*DownloadStatus, 0)

	for _, status := range m.downloads {
		// Include queued and active, exclude completed/paused/failed
		if status.State == StateQueued || status.State == StateActive {
			statusCopy := *status
			statusCopy.ConnectedPeers = m.getConnectedPeerCount(status.CID)
			active = append(active, &statusCopy)
		}
	}

	return active
}

// GetVisibleDownloads returns all non-terminal downloads (queued, active, paused).
func (m *Manager) GetVisibleDownloads() []*DownloadStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	visible := make([]*DownloadStatus, 0)
	for _, status := range m.downloads {
		if status.State == StateQueued || status.State == StateActive || status.State == StatePaused {
			statusCopy := *status
			statusCopy.ConnectedPeers = m.getConnectedPeerCount(status.CID)
			visible = append(visible, &statusCopy)
		}
	}
	return visible
}

// DownloadCounts holds counts for all download states.
type DownloadCounts struct {
	Queued    int `json:"queued"`
	Active    int `json:"active"`
	Paused    int `json:"paused"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
}

// GetCounts returns counts for all 5 download states.
func (m *Manager) GetCounts() DownloadCounts {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var counts DownloadCounts
	for _, status := range m.downloads {
		switch status.State {
		case StateQueued:
			counts.Queued++
		case StateActive:
			counts.Active++
		case StatePaused:
			counts.Paused++
		case StateCompleted:
			counts.Completed++
		case StateFailed:
			counts.Failed++
		}
	}
	return counts
}

// UpdateProgress updates download progress metrics
func (m *Manager) UpdateProgress(cid string, completedChunks int, downloadedBytes, speedBPS int64) error {
	// SECURITY: Validate inputs
	if cid == "" {
		return fmt.Errorf("update progress failed: invalid CID: empty string")
	}
	if completedChunks < 0 {
		return fmt.Errorf("update progress failed: invalid completedChunks: %d (must be >= 0)", completedChunks)
	}
	if downloadedBytes < 0 {
		return fmt.Errorf("update progress failed: invalid downloadedBytes: %d (must be >= 0)", downloadedBytes)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	status, exists := m.downloads[cid]
	if !exists {
		return fmt.Errorf("update progress failed: download not found for CID %s", cid)
	}

	status.CompletedChunks = completedChunks
	status.DownloadedBytes = downloadedBytes
	status.SpeedBPS = speedBPS

	// Calculate progress percentage
	if status.TotalChunks > 0 {
		status.Progress = float64(completedChunks) / float64(status.TotalChunks)
	}

	// Calculate ETA (estimated time remaining)
	if speedBPS > 0 && status.TotalSize > 0 {
		remainingBytes := status.TotalSize - downloadedBytes
		if remainingBytes > 0 {
			// Use ceiling division to avoid truncating to 0 for sub-second ETAs
			status.ETASEC = (remainingBytes + speedBPS - 1) / speedBPS
		} else {
			status.ETASEC = 0
		}
	}

	// NOTE: Chunk persistence is handled by downloadChunkFromPeer
	// via m.repo.UpdateChunkCompletion(cid, chunkIndex, true).
	// UpdateProgress ONLY updates in-memory progress metrics.
	// The old sequential persistence loop was removed because parallel
	// workers complete chunks out-of-order (e.g., 0,3,1,5,2).

	return nil
}

// LoadIncomplete loads incomplete downloads from disk on daemon startup.
// Active downloads are normalized to StateQueued so the worker re-spawns them.
// Orphan directories (chunks without metadata) are cleaned up.
func (m *Manager) LoadIncomplete() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Load all incomplete downloads from repository
	incompleteList, err := m.repo.ListIncomplete()
	if err != nil {
		return fmt.Errorf("failed to list incomplete downloads: %w", err)
	}

	// Track loaded CIDs for orphan detection
	loadedCIDs := make(map[string]bool)

	// Restore each download to in-memory map
	for _, metadata := range incompleteList {
		loadedCIDs[metadata.CID] = true

		// Calculate progress from chunks map
		completedChunks := 0
		for _, completed := range metadata.ChunksMap {
			if completed {
				completedChunks++
			}
		}

		var progress float64
		if metadata.TotalChunks > 0 {
			progress = float64(completedChunks) / float64(metadata.TotalChunks)
		}

		downloadedBytes := int64(completedChunks * metadata.ChunkSize)

		status := &DownloadStatus{
			CID:             metadata.CID,
			Filename:        metadata.Filename,
			State:           metadata.State,
			TotalSize:       metadata.TotalSize,
			TotalChunks:     metadata.TotalChunks,
			CompletedChunks: completedChunks,
			DownloadedBytes: downloadedBytes,
			Progress:        progress,
		}

		// Normalize: active downloads at crash time must restart as queued
		// so the worker picks them up again.
		if status.State == StateActive {
			status.State = StateQueued
			// Persist normalized state to disk
			metadata.State = StateQueued
			_ = m.repo.SaveMetadata(metadata)
		}

		m.downloads[metadata.CID] = status
	}

	// Orphan cleanup: scan baseDir for directories that have no corresponding metadata.
	// This handles crash-mid-cancel where chunks exist but metadata was already deleted.
	entries, err := os.ReadDir(m.baseDir)
	if err != nil {
		// Non-fatal — baseDir might not exist yet (first run)
		return nil
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dirName := entry.Name()
		if !loadedCIDs[dirName] {
			orphanPath := filepath.Join(m.baseDir, dirName)
			// Best-effort cleanup — non-fatal if it fails
			_ = os.RemoveAll(orphanPath)
		}
	}

	return nil
}

// countActiveDownloadsLocked counts downloads in StateActive (caller must hold lock)
func (m *Manager) countActiveDownloadsLocked() int {
	count := 0
	for _, status := range m.downloads {
		if status.State == StateActive {
			count++
		}
	}
	return count
}

// SetP2PDependencies injects P2P dependencies for download orchestration
// SECURITY: Must be called before StartWorker
func (m *Manager) SetP2PDependencies(deps *P2PDependencies) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.p2p = deps
}

// ensurePeerConnection connects to peer if not already connected (US-029-P3).
// Uses double-checked locking: fast read-lock check, then write-lock connect.
// Returns nil if already connected or connection succeeds.
func (m *Manager) ensurePeerConnection(cid string, peerID peer.ID) error {
	// Fast path: check if already connected (read lock)
	m.connMu.RLock()
	if peers, exists := m.connectedPeers[cid]; exists {
		if peers[peerID] {
			m.connMu.RUnlock()
			return nil
		}
	}
	m.connMu.RUnlock()

	// Slow path: connect (write lock)
	m.connMu.Lock()
	defer m.connMu.Unlock()

	// Double-check after acquiring write lock
	if peers, exists := m.connectedPeers[cid]; exists {
		if peers[peerID] {
			return nil
		}
	}

	// Get peer info from Coordinator for addresses
	m.mu.RLock()
	p2p := m.p2p
	m.mu.RUnlock()

	if p2p == nil || p2p.PeerConnector == nil {
		return fmt.Errorf("ensurePeerConnection: P2P dependencies not initialized")
	}

	peerIDStr := peerID.String() // Base58 encoding for coordinator lookup
	if coord := m.coordinatorFor(cid); coord != nil {
		info := coord.GetPeerInfo(peerIDStr)
		if info != nil && len(info.Addrs) > 0 {
			if err := p2p.PeerConnector.ConnectToPeer(peerID, info.Addrs); err != nil {
				return err
			}
		}
	}

	// Mark as connected
	if _, exists := m.connectedPeers[cid]; !exists {
		m.connectedPeers[cid] = make(map[peer.ID]bool)
	}
	m.connectedPeers[cid][peerID] = true

	return nil
}

// getConnectedPeerCount returns the number of peers currently connected for a CID.
// Caller must NOT hold connMu — this method acquires its own read lock.
func (m *Manager) getConnectedPeerCount(cid string) int {
	m.connMu.RLock()
	defer m.connMu.RUnlock()
	return len(m.connectedPeers[cid])
}

// clearConnectionCache removes all cached connections for a download.
// Called when download completes or is cancelled to prevent memory leak.
func (m *Manager) clearConnectionCache(cid string) {
	m.connMu.Lock()
	defer m.connMu.Unlock()
	delete(m.connectedPeers, cid)
}

// coordinatorFor returns the coordinator of an in-flight download, or the shared
// template when the download has none (unit tests that inject a coordinator).
func (m *Manager) coordinatorFor(cid string) *Coordinator {
	m.coordMu.Lock()
	c := m.coordinators[cid]
	m.coordMu.Unlock()
	if c != nil {
		return c
	}
	m.mu.RLock()
	p2p := m.p2p
	m.mu.RUnlock()
	if p2p == nil {
		return nil
	}
	return p2p.Coordinator
}

// newCoordinatorFor creates the per-download coordinator for cid, copying the
// template's peer limit and lease timeout. Returns nil when P2P is not wired.
func (m *Manager) newCoordinatorFor(cid string, totalChunks int) *Coordinator {
	m.mu.RLock()
	p2p := m.p2p
	metrics := m.metrics
	m.mu.RUnlock()
	if p2p == nil || p2p.Coordinator == nil {
		return nil
	}
	tpl := p2p.Coordinator
	tpl.mu.RLock()
	maxPeers, lease := tpl.maxPeers, tpl.leaseTimeout
	tpl.mu.RUnlock()
	c := NewCoordinatorWithConfig(totalChunks, maxPeers, lease)
	if metrics != nil {
		c.SetMetrics(metrics)
	}
	m.coordMu.Lock()
	if m.coordinators == nil {
		m.coordinators = make(map[string]*Coordinator)
	}
	m.coordinators[cid] = c
	m.coordMu.Unlock()
	return c
}

// releaseCoordinator forgets the per-download coordinator once the download ends.
func (m *Manager) releaseCoordinator(cid string) {
	m.coordMu.Lock()
	delete(m.coordinators, cid)
	m.coordMu.Unlock()
}
