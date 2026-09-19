// Package: tracker/internal/presence
// Feature: F-007 (Centralized Tracker)
// Story: US-007-02 (Peer Registry with Redis Presence Tracking)
// Purpose: In-memory presence store with TTL for MVP/testing

package presence

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
)

var (
	// ErrCapacityExceeded is returned when the maximum number of peers is reached
	ErrCapacityExceeded = errors.New("maximum peer capacity exceeded")
)

const (
	// SECURITY: Maximum registered peers to prevent memory exhaustion DoS
	MaxRegisteredPeers = 10000 // Cap at 10K peers

	// Cleanup interval for expired peer eviction
	cleanupInterval = 5 * time.Minute
)

type peerEntry struct {
	lastHeartbeat time.Time
	ttl           time.Duration
}

// MemoryPresenceStore tracks peer presence using an in-memory map with TTL.
// SECURITY: Implements capacity limits and TTL eviction to prevent memory exhaustion.
type MemoryPresenceStore struct {
	mu          sync.RWMutex
	peers       map[string]peerEntry
	clock       clock.Clock
	maxPeers    int // Configurable max peers (default: MaxRegisteredPeers)
	stopCleanup chan struct{}
	cleanupDone chan struct{}
}

// NewMemoryPresenceStore creates a new in-memory presence store with DoS protection.
func NewMemoryPresenceStore(clk clock.Clock) *MemoryPresenceStore {
	s := &MemoryPresenceStore{
		peers:       make(map[string]peerEntry),
		clock:       clk,
		maxPeers:    MaxRegisteredPeers,
		stopCleanup: make(chan struct{}),
		cleanupDone: make(chan struct{}),
	}

	// Start background cleanup goroutine
	go s.cleanupLoop()

	return s
}

// SetMaxPeers sets the maximum number of peers (for testing)
func (s *MemoryPresenceStore) SetMaxPeers(max int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maxPeers = max
}

// Close stops the background cleanup goroutine
func (s *MemoryPresenceStore) Close() {
	close(s.stopCleanup)
	<-s.cleanupDone
}

// cleanupLoop periodically evicts expired peers
func (s *MemoryPresenceStore) cleanupLoop() {
	defer close(s.cleanupDone)
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.evictExpired()
		case <-s.stopCleanup:
			return
		}
	}
}

// evictExpired removes expired peer entries (called by background goroutine)
func (s *MemoryPresenceStore) evictExpired() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.clock.Now()
	for id, entry := range s.peers {
		if now.Sub(entry.lastHeartbeat) >= entry.ttl {
			delete(s.peers, id)
		}
	}
}

// Heartbeat registers or refreshes a peer's online presence.
// SECURITY: Evicts expired entries and enforces capacity limits to prevent memory exhaustion.
func (s *MemoryPresenceStore) Heartbeat(ctx context.Context, peerID string, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// SECURITY: Check if this is a refresh of an existing peer
	_, isExisting := s.peers[peerID]

	// SECURITY: If at capacity and this is a new peer, evict expired entries first
	if !isExisting && len(s.peers) >= s.maxPeers {
		// Try to make space by evicting expired entries
		now := s.clock.Now()
		evicted := 0
		for id, entry := range s.peers {
			if now.Sub(entry.lastHeartbeat) >= entry.ttl {
				delete(s.peers, id)
				evicted++
			}
		}

		// If still at capacity after eviction, reject the new peer
		if len(s.peers) >= s.maxPeers {
			return ErrCapacityExceeded
		}
	}

	// Register or refresh the peer
	s.peers[peerID] = peerEntry{
		lastHeartbeat: s.clock.Now(),
		ttl:           ttl,
	}
	return nil
}

// IsOnline returns true if the peer has an unexpired heartbeat.
func (s *MemoryPresenceStore) IsOnline(ctx context.Context, peerID string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, exists := s.peers[peerID]
	if !exists {
		return false, nil
	}
	return s.clock.Now().Sub(entry.lastHeartbeat) < entry.ttl, nil
}

// OnlinePeerIDs returns the IDs of all peers with unexpired heartbeats.
func (s *MemoryPresenceStore) OnlinePeerIDs(ctx context.Context) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := s.clock.Now()
	ids := make([]string, 0)
	for id, entry := range s.peers {
		if now.Sub(entry.lastHeartbeat) < entry.ttl {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// Remove immediately marks a peer as offline.
func (s *MemoryPresenceStore) Remove(ctx context.Context, peerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.peers, peerID)
	return nil
}
