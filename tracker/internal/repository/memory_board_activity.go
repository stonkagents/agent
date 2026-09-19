// Package repository: In-memory implementation of BoardActivityRepository (tests).
package repository

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// MemoryBoardActivityRepository implements BoardActivityRepository in memory.
type MemoryBoardActivityRepository struct {
	mu   sync.RWMutex
	rows []*models.BoardActivity
}

// NewMemoryBoardActivityRepository creates an empty in-memory activity repository.
func NewMemoryBoardActivityRepository() *MemoryBoardActivityRepository {
	return &MemoryBoardActivityRepository{}
}

// Create inserts one row; a missing ID is generated.
func (r *MemoryBoardActivityRepository) Create(ctx context.Context, a *models.BoardActivity) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a.ID == "" {
		a.ID = uuid.New().String()
	}
	cp := *a
	r.rows = append(r.rows, &cp)
	return nil
}

// Exists reports whether peerID already has a row of kind for postID from actorPeerID.
func (r *MemoryBoardActivityRepository) Exists(ctx context.Context, peerID, kind, postID, actorPeerID string) (bool, error) {
	return r.ExistsAfter(ctx, peerID, kind, postID, actorPeerID, time.Time{})
}

// ExistsAfter is Exists restricted to rows created at or after the given time (zero = any time).
func (r *MemoryBoardActivityRepository) ExistsAfter(ctx context.Context, peerID, kind, postID, actorPeerID string, after time.Time) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, a := range r.rows {
		if a.PeerID == peerID && a.Kind == kind && a.PostID == postID && a.ActorPeerID == actorPeerID && !a.CreatedAt.Before(after) {
			return true, nil
		}
	}
	return false, nil
}

// List returns peerID's rows newest first under the ActivityQuery filters.
func (r *MemoryBoardActivityRepository) List(ctx context.Context, peerID string, q ActivityQuery) ([]*models.BoardActivity, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	kinds := make(map[string]bool, len(q.Kinds))
	for _, k := range q.Kinds {
		kinds[k] = true
	}
	excluded := make(map[string]bool, len(q.ExcludeKinds))
	for _, k := range q.ExcludeKinds {
		excluded[k] = true
	}
	out := []*models.BoardActivity{}
	for _, a := range r.rows {
		if a.PeerID != peerID {
			continue
		}
		if q.Since != nil && !a.CreatedAt.After(*q.Since) {
			continue
		}
		if len(kinds) > 0 && !kinds[a.Kind] {
			continue
		}
		if excluded[a.Kind] {
			continue
		}
		if q.Unread && a.ReadAt != nil {
			continue
		}
		cp := *a
		out = append(out, &cp)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// CountUnread returns how many of peerID's rows have no read_at.
func (r *MemoryBoardActivityRepository) CountUnread(ctx context.Context, peerID string) (int, error) {
	return r.CountUnreadExcluding(ctx, peerID, nil)
}

// CountUnreadExcluding is CountUnread without rows of the given kinds.
func (r *MemoryBoardActivityRepository) CountUnreadExcluding(ctx context.Context, peerID string, kinds []string) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	excluded := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		excluded[k] = true
	}
	n := 0
	for _, a := range r.rows {
		if a.PeerID == peerID && a.ReadAt == nil && !excluded[a.Kind] {
			n++
		}
	}
	return n, nil
}

// MarkRead sets read_at on peerID's unread rows with the given ids.
func (r *MemoryBoardActivityRepository) MarkRead(ctx context.Context, peerID string, ids []string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	want := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	for _, a := range r.rows {
		if _, ok := want[a.ID]; ok && a.PeerID == peerID && a.ReadAt == nil {
			t := at
			a.ReadAt = &t
		}
	}
	return nil
}

// MarkAllRead sets read_at on all of peerID's unread rows.
func (r *MemoryBoardActivityRepository) MarkAllRead(ctx context.Context, peerID string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range r.rows {
		if a.PeerID == peerID && a.ReadAt == nil {
			t := at
			a.ReadAt = &t
		}
	}
	return nil
}
