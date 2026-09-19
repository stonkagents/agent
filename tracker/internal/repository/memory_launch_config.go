// Package: tracker/internal/repository
// Feature: StonkAgents Launchpad (Raydium LaunchLab)
// Purpose: In-memory implementations of LaunchSettingsRepository and LaunchQuoteRepository for testing

package repository

import (
	"context"
	"sort"
	"sync"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// MemoryLaunchSettingsRepository is a thread-safe in-memory LaunchSettingsRepository.
type MemoryLaunchSettingsRepository struct {
	mu  sync.RWMutex
	fee *models.LaunchFee
}

// NewMemoryLaunchSettingsRepository creates an empty in-memory launch settings repository.
func NewMemoryLaunchSettingsRepository() *MemoryLaunchSettingsRepository {
	return &MemoryLaunchSettingsRepository{}
}

func (r *MemoryLaunchSettingsRepository) GetFee(_ context.Context) (*models.LaunchFee, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.fee == nil {
		return nil, models.ErrNotFound
	}
	out := *r.fee
	return &out, nil
}

func (r *MemoryLaunchSettingsRepository) UpsertFee(_ context.Context, fee *models.LaunchFee) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored := *fee
	r.fee = &stored
	return nil
}

// MemoryLaunchQuoteRepository is a thread-safe in-memory LaunchQuoteRepository. Like the Postgres
// one it is scoped to a cluster: rows for other clusters are stored but never returned.
type MemoryLaunchQuoteRepository struct {
	mu      sync.RWMutex
	cluster string
	quotes  map[string]*models.LaunchQuote // key: cluster + "|" + quote_mint
}

// NewMemoryLaunchQuoteRepository creates an empty in-memory launch quote repository reading mainnet rows.
func NewMemoryLaunchQuoteRepository() *MemoryLaunchQuoteRepository {
	return NewMemoryLaunchQuoteRepositoryForCluster(models.LaunchClusterMainnet)
}

// NewMemoryLaunchQuoteRepositoryForCluster creates an empty repository reading rows of cluster.
func NewMemoryLaunchQuoteRepositoryForCluster(cluster string) *MemoryLaunchQuoteRepository {
	if cluster == "" {
		cluster = models.LaunchClusterMainnet
	}
	return &MemoryLaunchQuoteRepository{cluster: cluster, quotes: make(map[string]*models.LaunchQuote)}
}

// Cluster returns the cluster this repository reads.
func (r *MemoryLaunchQuoteRepository) Cluster() string { return r.cluster }

// Put inserts or replaces a quote (test seeding helper; mirrors the migration seed in production).
// A quote with no Cluster is stored as mainnet, matching the column default.
func (r *MemoryLaunchQuoteRepository) Put(q *models.LaunchQuote) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored := *q
	if stored.Cluster == "" {
		stored.Cluster = models.LaunchClusterMainnet
	}
	r.quotes[stored.Cluster+"|"+stored.QuoteMint] = &stored
}

func (r *MemoryLaunchQuoteRepository) ListEnabled(_ context.Context) ([]*models.LaunchQuote, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*models.LaunchQuote, 0, len(r.quotes))
	for _, q := range r.quotes {
		if !q.Enabled || q.Cluster != r.cluster {
			continue
		}
		c := *q
		out = append(out, &c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SortOrder != out[j].SortOrder {
			return out[i].SortOrder < out[j].SortOrder
		}
		return out[i].QuoteMint < out[j].QuoteMint
	})
	return out, nil
}

func (r *MemoryLaunchQuoteRepository) GetByMint(_ context.Context, quoteMint string) (*models.LaunchQuote, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	q, ok := r.quotes[r.cluster+"|"+quoteMint]
	if !ok {
		return nil, models.ErrNotFound
	}
	c := *q
	return &c, nil
}
