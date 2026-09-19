// Package: internal/daemon/download
// Feature: F-010 (P2P Transfer Protocol)
// Story: US-010-06 (Chunked Download Manager)
// Purpose: P2P download orchestration - peer discovery, chunk downloading

package download

import (
	"context"
	"fmt"
	"log"
	"math"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// executeDownload orchestrates the complete download process (Phase 5)
// Orchestrates 3 phases: peer discovery, chunk download, file assembly
// Security: Each phase validates inputs, errors are wrapped with context
// Context passed through for cancellation support
func (m *Manager) executeDownload(ctx context.Context, cid, filename string, totalChunks int) error {
	// SECURITY: Validate inputs
	if cid == "" {
		return fmt.Errorf("execute download failed: invalid CID: empty string")
	}
	if filename == "" {
		return fmt.Errorf("execute download failed: invalid filename: empty string")
	}
	if totalChunks <= 0 {
		return fmt.Errorf("execute download failed: invalid totalChunks: %d (must be > 0)", totalChunks)
	}
	if ctx == nil {
		return fmt.Errorf("execute download failed: invalid context: nil")
	}

	// One coordinator per download: concurrent downloads must not share (and
	// reset) each other's chunk map.
	m.mu.RLock()
	p2p := m.p2p
	m.mu.RUnlock()
	coord := m.newCoordinatorFor(cid, totalChunks)
	if coord != nil {
		defer m.releaseCoordinator(cid)
	}

	// Phase A: Peer discovery (tracker + DHT multi-source, BitTorrent-style)
	if err := m.searchAndRegisterPeers(ctx, cid, totalChunks); err != nil {
		return fmt.Errorf("download failed for CID %s: peer discovery failed: %w", cid, err)
	}

	// Phase A.5: Pre-warm connections in parallel before workers start (TD-059)
	if coord != nil && p2p != nil && p2p.PeerConnector != nil {
		warmupConnections(ctx, coord, p2p.PeerConnector, 500*time.Millisecond)
	}

	// Phase B: Download chunks (parallel worker pool — US-029-P5)
	if err := m.runDownloadWorkerPool(ctx, cid, totalChunks); err != nil {
		return fmt.Errorf("download failed for CID %s: chunk download failed: %w", cid, err)
	}

	// Phase C: Assemble file
	if err := m.assembleFile(cid, filename, totalChunks); err != nil {
		return fmt.Errorf("download failed for CID %s: file assembly failed: %w", cid, err)
	}

	return nil
}

// searchAndRegisterPeers discovers peers via tracker and DHT (multi-source, BitTorrent-style), then registers with coordinator.
// Phase 2: Tracker + DHT Search & Peer Registration. Merges by peer ID (tracker chunk list wins); DHT peers get optimistic full chunks.
// Context-aware: respects cancellation during retry backoff and DHT discovery.
func (m *Manager) searchAndRegisterPeers(ctx context.Context, cid string, totalChunks int) error {
	// SECURITY: Validate CID format (non-empty string)
	if cid == "" {
		return fmt.Errorf("peer discovery failed: invalid CID: empty string")
	}

	// Check if P2P dependencies are initialized
	m.mu.RLock()
	p2p := m.p2p
	m.mu.RUnlock()

	if p2p == nil {
		return fmt.Errorf("peer discovery failed: P2P dependencies not initialized")
	}
	if p2p.TrackerClient == nil {
		return fmt.Errorf("peer discovery failed: tracker client not initialized")
	}

	// 1. Tracker: retry with context-aware exponential backoff (3 attempts)
	var trackerPeers []PeerInfo
	var trackerErr error
	maxRetries := 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		trackerPeers, trackerErr = p2p.TrackerClient.SearchByCID(cid)
		if trackerErr == nil {
			break
		}
		if attempt < maxRetries-1 {
			backoff := time.Duration(math.Pow(2, float64(attempt))) * time.Second
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return fmt.Errorf("peer discovery cancelled during tracker retry: %w", ctx.Err())
			}
		}
	}

	// 2. DHT: FindProviders (BitTorrent get_peers equivalent). Uses parent ctx for cancellation.
	var dhtPeers []PeerInfo
	if p2p.DHTContentDiscovery != nil && totalChunks > 0 {
		dhtCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		dhtPeers, _ = p2p.DHTContentDiscovery.FindProvidersForContent(dhtCtx, cid, totalChunks, 20)
		cancel()
	}

	// 3. Merge by peer ID: tracker entry wins (has real chunk list); DHT-only peers get optimistic full chunks
	seen := make(map[string]bool)
	var merged []PeerInfo
	for _, p := range trackerPeers {
		if !seen[p.PeerID] {
			seen[p.PeerID] = true
			merged = append(merged, p)
		}
	}
	optimisticChunks := make([]int, totalChunks)
	for i := 0; i < totalChunks; i++ {
		optimisticChunks[i] = i
	}
	for _, p := range dhtPeers {
		if !seen[p.PeerID] {
			seen[p.PeerID] = true
			merged = append(merged, PeerInfo{
				PeerID: p.PeerID,
				Chunks: optimisticChunks,
				Addrs:  p.Addrs,
			})
		}
	}

	// 4. Require at least one peer (from any source)
	if len(merged) == 0 {
		if trackerErr != nil {
			return fmt.Errorf("peer discovery failed for CID %s: tracker failed after %d attempts and DHT returned no peers: %w", cid, maxRetries, trackerErr)
		}
		return fmt.Errorf("peer discovery failed: no peers found for CID %s (tracker and DHT returned empty)", cid)
	}

	// 5. Register each peer with Coordinator and connect (log errors, don't abort on single failure)
	for _, peerInfo := range merged {
		if err := m.coordinatorFor(cid).RegisterPeer(peerInfo.PeerID, peerInfo.Chunks, peerInfo.Addrs); err != nil {
			log.Printf("[download.searchAndRegisterPeers] RegisterPeer %s failed: %v", peerInfo.PeerID, err)
			continue
		}
		if p2p.PeerConnector != nil {
			peerID, err := peer.Decode(peerInfo.PeerID)
			if err == nil {
				_ = p2p.PeerConnector.ConnectToPeer(peerID, peerInfo.Addrs)
			}
		}
	}

	return nil
}

// getAllPeersForChunk converts peer strings from Coordinator to []peer.ID for P2PClient.
// Uses ensurePeerConnection for cached connections (US-029-P3).
// Skips invalid peer IDs without erroring (security: validate peer IDs before conversion).
func (m *Manager) getAllPeersForChunk(cid string, chunkIndex int) []peer.ID {
	// Handle nil P2P dependencies
	if m.coordinatorFor(cid) == nil {
		return []peer.ID{}
	}

	// Get peer strings from coordinator
	peerStrings := m.coordinatorFor(cid).GetPeersForChunk(chunkIndex)

	// Handle empty peer list
	if len(peerStrings) == 0 {
		return []peer.ID{}
	}

	// Convert peer strings to peer.ID objects
	peerIDs := make([]peer.ID, 0, len(peerStrings))
	for _, peerStr := range peerStrings {
		peerID, err := peer.Decode(peerStr)
		if err != nil {
			// SECURITY: Skip invalid peer IDs instead of failing
			continue
		}
		// Connect once, cached thereafter (US-029-P3)
		_ = m.ensurePeerConnection(cid, peerID)
		peerIDs = append(peerIDs, peerID)
	}

	return peerIDs
}
