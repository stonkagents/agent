// Package: tracker/internal/repository
// Purpose: In-memory availability store for peer chunk maps

package repository

import (
	"context"
	"sort"
	"sync"
)

// MemoryAvailabilityRepository stores per-peer chunk availability in memory.
type MemoryAvailabilityRepository struct {
	mu           sync.RWMutex
	availability map[string]map[string]map[int]bool // cid -> peerID -> chunkIndex -> true
}

// NewMemoryAvailabilityRepository creates a new in-memory availability repo.
func NewMemoryAvailabilityRepository() *MemoryAvailabilityRepository {
	return &MemoryAvailabilityRepository{
		availability: make(map[string]map[string]map[int]bool),
	}
}

// UpsertPeerChunks replaces the peer's chunk set for a CID.
func (r *MemoryAvailabilityRepository) UpsertPeerChunks(_ context.Context, cid string, peerID string, chunks []int) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.availability[cid]; !exists {
		r.availability[cid] = make(map[string]map[int]bool)
	}
	if _, exists := r.availability[cid][peerID]; !exists {
		r.availability[cid][peerID] = make(map[int]bool)
	}

	// Replace chunk map
	newMap := make(map[int]bool, len(chunks))
	for _, idx := range chunks {
		if idx >= 0 {
			newMap[idx] = true
		}
	}
	r.availability[cid][peerID] = newMap
	return nil
}

// GetPeerChunks returns per-peer chunk lists for a CID.
func (r *MemoryAvailabilityRepository) GetPeerChunks(_ context.Context, cid string) ([]PeerChunkAvailability, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	peerMap, exists := r.availability[cid]
	if !exists {
		return []PeerChunkAvailability{}, nil
	}

	results := make([]PeerChunkAvailability, 0, len(peerMap))
	for peerID, chunksMap := range peerMap {
		chunks := make([]int, 0, len(chunksMap))
		for idx := range chunksMap {
			chunks = append(chunks, idx)
		}
		sort.Ints(chunks)
		results = append(results, PeerChunkAvailability{
			PeerID: peerID,
			Chunks: chunks,
		})
	}

	return results, nil
}
