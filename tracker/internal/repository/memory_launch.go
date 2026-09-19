// Package: tracker/internal/repository
// Feature: StonkAgents launchpad (Raydium LaunchLab)
// Purpose: In-memory LaunchRepository for tests and offline development

package repository

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// MemoryLaunchRepository is a thread-safe in-memory LaunchRepository.
type MemoryLaunchRepository struct {
	mu          sync.RWMutex
	byMint      map[string]*models.TokenLaunch
	bySignature map[string]*models.TokenLaunch
}

// NewMemoryLaunchRepository creates an empty in-memory launch repository.
func NewMemoryLaunchRepository() *MemoryLaunchRepository {
	return &MemoryLaunchRepository{
		byMint:      make(map[string]*models.TokenLaunch),
		bySignature: make(map[string]*models.TokenLaunch),
	}
}

// Create inserts a launch. Returns ErrAlreadyExists if the mint or signature is already recorded.
func (r *MemoryLaunchRepository) Create(_ context.Context, launch *models.TokenLaunch) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.byMint[launch.Mint]; exists {
		return models.ErrAlreadyExists
	}
	if _, exists := r.bySignature[launch.LaunchSignature]; exists {
		return models.ErrAlreadyExists
	}
	stored := *launch
	if stored.Status == "" {
		stored.Status = models.LaunchStatusConfirmed
	}
	r.byMint[stored.Mint] = &stored
	r.bySignature[stored.LaunchSignature] = &stored
	return nil
}

// GetByMint returns the launch for a mint, or ErrNotFound.
func (r *MemoryLaunchRepository) GetByMint(_ context.Context, mint string) (*models.TokenLaunch, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	l, ok := r.byMint[mint]
	if !ok {
		return nil, models.ErrNotFound
	}
	out := *l
	return &out, nil
}

// GetBySignature returns the launch recorded from a launch transaction, or ErrNotFound.
func (r *MemoryLaunchRepository) GetBySignature(_ context.Context, signature string) (*models.TokenLaunch, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	l, ok := r.bySignature[signature]
	if !ok {
		return nil, models.ErrNotFound
	}
	out := *l
	return &out, nil
}

// List returns launches ordered by created_at DESC plus the total matching count.
func (r *MemoryLaunchRepository) List(_ context.Context, opts ListLaunchesOptions) ([]*models.TokenLaunch, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	all := make([]*models.TokenLaunch, 0, len(r.byMint))
	for _, l := range r.byMint {
		if opts.CreatorWallet != "" && l.CreatorWallet != opts.CreatorWallet {
			continue
		}
		if opts.UnboundOnly && l.IsBound() {
			continue
		}
		c := *l
		all = append(all, &c)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].Mint < all[j].Mint
		}
		return all[i].CreatedAt.After(all[j].CreatedAt)
	})

	total := len(all)
	if opts.Offset >= total {
		return []*models.TokenLaunch{}, total, nil
	}
	all = all[opts.Offset:]
	if opts.Limit > 0 && opts.Limit < len(all) {
		all = all[:opts.Limit]
	}
	return all, total, nil
}

// Bind sets peer_id, status='bound' and bound_at on an unbound launch.
func (r *MemoryLaunchRepository) Bind(_ context.Context, mint, peerID string, boundAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	l, ok := r.byMint[mint]
	if !ok {
		return models.ErrNotFound
	}
	if l.IsBound() {
		return models.ErrAlreadyExists
	}
	l.PeerID = peerID
	l.Status = models.LaunchStatusBound
	t := boundAt
	l.BoundAt = &t
	return nil
}

// GetByMints returns the launches that exist among mints, keyed by mint.
func (r *MemoryLaunchRepository) GetByMints(_ context.Context, mints []string) (map[string]*models.TokenLaunch, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]*models.TokenLaunch, len(mints))
	for _, m := range mints {
		if l, ok := r.byMint[m]; ok {
			c := *l
			out[m] = &c
		}
	}
	return out, nil
}

// ListByPeerID returns the launches bound to peerID, newest first.
func (r *MemoryLaunchRepository) ListByPeerID(_ context.Context, peerID string) ([]*models.TokenLaunch, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []*models.TokenLaunch{}
	for _, l := range r.byMint {
		if peerID != "" && l.PeerID == peerID {
			c := *l
			out = append(out, &c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].Mint < out[j].Mint
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}
