// Package: internal/daemon/download
// Feature: F-010 (P2P Transfer Protocol)
// Story: US-010-06 (Chunked Download Manager)
// Purpose: Multi-peer download coordinator with chunk allocation

package download

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// DefaultMaxPeers is the maximum number of peers a coordinator will track
// SECURITY: Prevents unbounded map growth from malicious peer registrations
const DefaultMaxPeers = 1000

// PeerInfo stores information about a peer and their available chunks
type PeerInfo struct {
	PeerID string
	Chunks []int // Chunk indices this peer has
	Addrs  []string
}

// Coordinator manages chunk allocation across multiple peers
type Coordinator struct {
	totalChunks     int
	maxPeers        int           // SECURITY: Caps peer registrations to prevent OOM
	leaseTimeout    time.Duration // Configurable lease duration (US-029-P6)
	metrics         *Metrics      // Optional observability (US-029-P7)
	completedChunks map[int]bool
	peers           map[string]*PeerInfo // peerID -> PeerInfo
	chunkToPeers    map[int][]string     // chunkIndex -> []peerID
	mu              sync.RWMutex

	// Lease tracking: prevents duplicate chunk downloads by concurrent workers
	leases map[int]time.Time // chunkIndex -> lease expiry

	// Endgame mode: when few chunks remain, allow duplicate leases (TD-057)
	endgameMode bool
}

// NewCoordinator creates a new multi-peer download coordinator with default max peers
func NewCoordinator(totalChunks int) *Coordinator {
	return NewCoordinatorWithMaxPeers(totalChunks, DefaultMaxPeers)
}

// NewCoordinatorWithMaxPeers creates a coordinator with a custom peer limit
// SECURITY: maxPeers bounds map growth to prevent denial-of-service via peer flooding
func NewCoordinatorWithMaxPeers(totalChunks, maxPeers int) *Coordinator {
	return NewCoordinatorWithConfig(totalChunks, maxPeers, 30*time.Second)
}

// NewCoordinatorWithConfig creates a coordinator with custom peer limit and lease timeout.
// US-029-P6: Externalizes the lease timeout instead of hardcoding 30s.
func NewCoordinatorWithConfig(totalChunks, maxPeers int, leaseTimeout time.Duration) *Coordinator {
	return &Coordinator{
		totalChunks:     totalChunks,
		maxPeers:        maxPeers,
		leaseTimeout:    leaseTimeout,
		completedChunks: make(map[int]bool),
		peers:           make(map[string]*PeerInfo),
		chunkToPeers:    make(map[int][]string),
		leases:          make(map[int]time.Time),
	}
}

// SetMetrics injects a Metrics instance for observability.
func (c *Coordinator) SetMetrics(m *Metrics) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.metrics = m
}

// Reset clears coordinator state for a new download.
func (c *Coordinator) Reset(totalChunks int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.totalChunks = totalChunks
	c.completedChunks = make(map[int]bool)
	c.peers = make(map[string]*PeerInfo)
	c.chunkToPeers = make(map[int][]string)
	c.leases = make(map[int]time.Time)
	c.endgameMode = false
}

// RegisterPeer adds a peer with their available chunks
// SECURITY: Enforces max peer limit and validates chunk indices
// Returns error if max peers exceeded; invalid chunk indices are silently filtered
func (c *Coordinator) RegisterPeer(peerID string, chunks []int, addrs []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// SECURITY: Enforce max peer limit to prevent unbounded map growth
	if len(c.peers) >= c.maxPeers {
		return fmt.Errorf("max peers reached (%d)", c.maxPeers)
	}

	// SECURITY: Filter chunk indices to valid range [0, totalChunks)
	validChunks := make([]int, 0, len(chunks))
	for _, idx := range chunks {
		if idx >= 0 && idx < c.totalChunks {
			validChunks = append(validChunks, idx)
		}
	}

	// Store peer info with validated chunks
	c.peers[peerID] = &PeerInfo{
		PeerID: peerID,
		Chunks: validChunks,
		Addrs:  addrs,
	}

	// Update chunk-to-peers mapping
	for _, chunkIndex := range validChunks {
		if c.chunkToPeers[chunkIndex] == nil {
			c.chunkToPeers[chunkIndex] = []string{}
		}
		c.chunkToPeers[chunkIndex] = append(c.chunkToPeers[chunkIndex], peerID)
	}

	return nil
}

// UnregisterPeer removes a peer (e.g., on disconnect)
func (c *Coordinator) UnregisterPeer(peerID string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	peerInfo, exists := c.peers[peerID]
	if !exists {
		return
	}

	// Remove peer from chunk-to-peers mapping
	for _, chunkIndex := range peerInfo.Chunks {
		peers := c.chunkToPeers[chunkIndex]
		for i, pid := range peers {
			if pid == peerID {
				c.chunkToPeers[chunkIndex] = append(peers[:i], peers[i+1:]...)
				break
			}
		}
	}

	// Remove peer
	delete(c.peers, peerID)
}

// GetPeerChunks returns the chunks a peer has
func (c *Coordinator) GetPeerChunks(peerID string) []int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	peerInfo, exists := c.peers[peerID]
	if !exists {
		return []int{}
	}

	return peerInfo.Chunks
}

// GetPeerInfo returns the full peer info by peer ID.
func (c *Coordinator) GetPeerInfo(peerID string) *PeerInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()

	peerInfo, exists := c.peers[peerID]
	if !exists {
		return nil
	}

	copied := *peerInfo
	return &copied
}

// GetAllPeers returns a copy of all peer info entries.
func (c *Coordinator) GetAllPeers() []*PeerInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()

	peers := make([]*PeerInfo, 0, len(c.peers))
	for _, info := range c.peers {
		copied := *info
		peers = append(peers, &copied)
	}
	return peers
}

// AllocateChunks allocates all chunks to peers using round-robin
// Returns map[chunkIndex]peerID
func (c *Coordinator) AllocateChunks() map[int]string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	allocations := make(map[int]string)

	// Get list of peer IDs for round-robin
	peerIDs := []string{}
	for peerID := range c.peers {
		peerIDs = append(peerIDs, peerID)
	}

	if len(peerIDs) == 0 {
		return allocations
	}

	peerIndex := 0

	// Allocate each chunk to a peer that has it (round-robin)
	for chunkIndex := 0; chunkIndex < c.totalChunks; chunkIndex++ {
		if c.completedChunks[chunkIndex] {
			continue // Skip completed chunks
		}

		peersWithChunk := c.chunkToPeers[chunkIndex]
		if len(peersWithChunk) == 0 {
			continue // No peer has this chunk
		}

		// Round-robin selection among peers that have this chunk
		selectedPeer := peersWithChunk[peerIndex%len(peersWithChunk)]
		allocations[chunkIndex] = selectedPeer

		peerIndex++
	}

	return allocations
}

// isLeased checks if a chunk is currently leased (must be called with lock held)
func (c *Coordinator) isLeased(chunkIndex int) bool {
	expiry, exists := c.leases[chunkIndex]
	if !exists {
		return false
	}

	// Check if lease has expired
	if time.Now().After(expiry) {
		delete(c.leases, chunkIndex)
		if c.metrics != nil {
			c.metrics.RecordLeaseExpired(chunkIndex)
		}
		return false
	}

	return true
}

// GetNextChunk returns the next chunk to download using rarest-first strategy
// with lease-based reservation to prevent duplicate downloads by concurrent workers.
// Returns (-1, error) if no chunks are available (all completed, leased, or no peers).
func (c *Coordinator) GetNextChunk() (int, error) {
	c.mu.Lock() // Write lock needed for lease creation
	defer c.mu.Unlock()

	// Build list of incomplete, unleased chunks with their rarity (number of peers)
	type chunkRarity struct {
		chunkIndex int
		numPeers   int
	}

	rarities := []chunkRarity{}

	for chunkIndex := 0; chunkIndex < c.totalChunks; chunkIndex++ {
		if c.completedChunks[chunkIndex] {
			continue // Skip completed chunks
		}

		// In endgame mode, ignore lease exclusivity — allow duplicate downloads (TD-057)
		if !c.endgameMode && c.isLeased(chunkIndex) {
			continue // Skip leased chunks (normal mode only)
		}

		numPeers := len(c.chunkToPeers[chunkIndex])
		if numPeers == 0 {
			continue // No peer has this chunk
		}

		rarities = append(rarities, chunkRarity{
			chunkIndex: chunkIndex,
			numPeers:   numPeers,
		})
	}

	if len(rarities) == 0 {
		return -1, fmt.Errorf("no available chunks")
	}

	// Sort by rarity (ascending) - rarest first
	sort.Slice(rarities, func(i, j int) bool {
		return rarities[i].numPeers < rarities[j].numPeers
	})

	// Lease the rarest available chunk
	chunkIndex := rarities[0].chunkIndex
	c.leases[chunkIndex] = time.Now().Add(c.leaseTimeout)

	if c.metrics != nil {
		c.metrics.RecordLeaseAcquired(chunkIndex, 0) // workerID set in Task 7
	}

	return chunkIndex, nil
}

// ReleaseLease releases a chunk lease (called when download fails)
func (c *Coordinator) ReleaseLease(chunkIndex int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.leases, chunkIndex)

	if c.metrics != nil {
		c.metrics.RecordLeaseReleased(chunkIndex, "failed")
	}
}

// MarkChunkComplete marks a chunk as downloaded and releases its lease
func (c *Coordinator) MarkChunkComplete(chunkIndex int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.completedChunks[chunkIndex] = true
	delete(c.leases, chunkIndex)

	if c.metrics != nil {
		c.metrics.RecordLeaseReleased(chunkIndex, "completed")
	}
}

// GetCompletedChunks returns a list of completed chunk indices.
func (c *Coordinator) GetCompletedChunks() []int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	completed := make([]int, 0, len(c.completedChunks))
	for idx := range c.completedChunks {
		completed = append(completed, idx)
	}
	sort.Ints(completed)
	return completed
}

// IsComplete checks if a specific chunk is completed
func (c *Coordinator) IsComplete(chunkIndex int) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.completedChunks[chunkIndex]
}

// AllChunksComplete checks if all chunks are downloaded
func (c *Coordinator) AllChunksComplete() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for i := 0; i < c.totalChunks; i++ {
		if !c.completedChunks[i] {
			return false
		}
	}

	return true
}

// GetPeerForChunk returns a peer ID that has the specified chunk
// Returns empty string if no peer has it
func (c *Coordinator) GetPeerForChunk(chunkIndex int) string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	peers := c.chunkToPeers[chunkIndex]
	if len(peers) == 0 {
		return ""
	}

	// Return first available peer
	return peers[0]
}

// GetPeersForChunk returns ALL peers that have the specified chunk
// Used by P2PClient for automatic failover across multiple peers
// Returns a copy of the peer slice to prevent external mutation
// Thread-safe: uses read lock
func (c *Coordinator) GetPeersForChunk(chunkIndex int) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	peers := c.chunkToPeers[chunkIndex]
	if len(peers) == 0 {
		return []string{}
	}

	// Return a copy to prevent external mutation
	peersCopy := make([]string, len(peers))
	copy(peersCopy, peers)
	return peersCopy
}

// GetProgress returns download progress as a percentage (0.0 to 1.0)
func (c *Coordinator) GetProgress() float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.totalChunks == 0 {
		return 0.0
	}

	completedCount := 0
	for i := 0; i < c.totalChunks; i++ {
		if c.completedChunks[i] {
			completedCount++
		}
	}

	return float64(completedCount) / float64(c.totalChunks)
}
