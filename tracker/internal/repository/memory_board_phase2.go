// Package repository: In-memory community board phase 2 repositories (tests): autopilot
// categories per peer and room holder snapshots.
package repository

import (
	"context"
	"sync"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// --- PeerAutopilotRepository ---

// MemoryPeerAutopilotRepository implements PeerAutopilotRepository in memory. It counts the
// Clear calls so tests can check that heartbeats without the field do not delete every time.
type MemoryPeerAutopilotRepository struct {
	mu     sync.RWMutex
	rows   map[string]*models.PeerAutopilot
	clears int
}

// Clears returns how many times Clear was called.
func (r *MemoryPeerAutopilotRepository) Clears() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.clears
}

// NewMemoryPeerAutopilotRepository creates an empty repository.
func NewMemoryPeerAutopilotRepository() *MemoryPeerAutopilotRepository {
	return &MemoryPeerAutopilotRepository{rows: map[string]*models.PeerAutopilot{}}
}

// Set replaces the peer's categories.
func (r *MemoryPeerAutopilotRepository) Set(ctx context.Context, peerID string, categories []string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows[peerID] = &models.PeerAutopilot{PeerID: peerID, Categories: append([]string{}, categories...), UpdatedAt: at}
	return nil
}

// Clear removes the peer's row.
func (r *MemoryPeerAutopilotRepository) Clear(ctx context.Context, peerID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clears++
	delete(r.rows, peerID)
	return nil
}

// CategoriesByIDs returns peer id -> categories for the peers that have a row.
func (r *MemoryPeerAutopilotRepository) CategoriesByIDs(ctx context.Context, peerIDs []string) (map[string][]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string][]string, len(peerIDs))
	for _, id := range peerIDs {
		if row, ok := r.rows[id]; ok {
			out[id] = append([]string{}, row.Categories...)
		}
	}
	return out, nil
}

// --- RoomHolderSnapshotRepository ---

// MemoryRoomHolderSnapshotRepository implements RoomHolderSnapshotRepository in memory.
type MemoryRoomHolderSnapshotRepository struct {
	mu   sync.RWMutex
	rows map[string]map[time.Time]int // mint -> UTC day -> holders
}

// NewMemoryRoomHolderSnapshotRepository creates an empty repository.
func NewMemoryRoomHolderSnapshotRepository() *MemoryRoomHolderSnapshotRepository {
	return &MemoryRoomHolderSnapshotRepository{rows: map[string]map[time.Time]int{}}
}

// Record upserts the room's holder count for the UTC day of takenOn.
func (r *MemoryRoomHolderSnapshotRepository) Record(ctx context.Context, mint string, takenOn time.Time, holders int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.rows[mint] == nil {
		r.rows[mint] = map[time.Time]int{}
	}
	r.rows[mint][utcDay(takenOn)] = holders
	return nil
}

// LatestAtOrBefore returns the newest snapshot taken on or before the UTC day of at.
func (r *MemoryRoomHolderSnapshotRepository) LatestAtOrBefore(ctx context.Context, mint string, at time.Time) (*models.RoomHolderSnapshot, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	day := utcDay(at)
	var best *models.RoomHolderSnapshot
	for d, n := range r.rows[mint] {
		if d.After(day) {
			continue
		}
		if best == nil || d.After(best.TakenOn) {
			best = &models.RoomHolderSnapshot{Mint: mint, TakenOn: d, Holders: n}
		}
	}
	if best == nil {
		return nil, models.ErrNotFound
	}
	return best, nil
}
