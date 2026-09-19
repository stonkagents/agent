// Package: tracker/internal/repository
// Feature: StonkAgents launchpad (Raydium LaunchLab)
// Purpose: In-memory RevenueRepository for tests and offline development

package repository

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// MemoryRevenueRepository is a thread-safe in-memory RevenueRepository.
type MemoryRevenueRepository struct {
	mu      sync.RWMutex
	entries []*models.PlatformRevenue
	bySig   map[string]struct{}
	nextID  int64
}

// NewMemoryRevenueRepository creates an empty in-memory revenue ledger.
func NewMemoryRevenueRepository() *MemoryRevenueRepository {
	return &MemoryRevenueRepository{bySig: make(map[string]struct{}), nextID: 1}
}

// Insert appends a ledger entry; duplicate signatures return ErrAlreadyExists.
func (r *MemoryRevenueRepository) Insert(_ context.Context, entry *models.PlatformRevenue) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if entry.Signature != "" {
		if _, dup := r.bySig[entry.Signature]; dup {
			return models.ErrAlreadyExists
		}
		r.bySig[entry.Signature] = struct{}{}
	}
	stored := *entry
	stored.ID = r.nextID
	r.nextID++
	if stored.OccurredAt.IsZero() {
		stored.OccurredAt = time.Now().UTC()
	}
	r.entries = append(r.entries, &stored)
	entry.ID = stored.ID
	return nil
}

// TotalsByKind returns count and sums per kind across the whole ledger.
func (r *MemoryRevenueRepository) TotalsByKind(_ context.Context) ([]*models.RevenueKindTotal, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	byKind := make(map[string]*models.RevenueKindTotal)
	for _, e := range r.entries {
		t, ok := byKind[e.Kind]
		if !ok {
			t = &models.RevenueKindTotal{Kind: e.Kind}
			byKind[e.Kind] = t
		}
		t.Count++
		t.AmountRaw += e.AmountRaw
		if e.AmountUSD != nil {
			t.AmountUSD += *e.AmountUSD
		}
	}
	out := make([]*models.RevenueKindTotal, 0, len(byKind))
	for _, t := range byKind {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out, nil
}

// DailyByKind returns per-day (UTC), per-kind aggregates for entries at or after since.
func (r *MemoryRevenueRepository) DailyByKind(_ context.Context, since time.Time) ([]*models.RevenueDailyRow, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	type key struct{ date, kind string }
	agg := make(map[key]*models.RevenueDailyRow)
	for _, e := range r.entries {
		if e.OccurredAt.Before(since) {
			continue
		}
		k := key{e.OccurredAt.UTC().Format("2006-01-02"), e.Kind}
		row, ok := agg[k]
		if !ok {
			row = &models.RevenueDailyRow{Date: k.date, Kind: k.kind}
			agg[k] = row
		}
		row.Count++
		row.AmountRaw += e.AmountRaw
		if e.AmountUSD != nil {
			row.AmountUSD += *e.AmountUSD
		}
	}
	out := make([]*models.RevenueDailyRow, 0, len(agg))
	for _, row := range agg {
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Date == out[j].Date {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Date < out[j].Date
	})
	return out, nil
}

// Recent returns the newest ledger entries first, at most limit of them.
func (r *MemoryRevenueRepository) Recent(_ context.Context, limit int) ([]*models.PlatformRevenue, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if limit <= 0 {
		return []*models.PlatformRevenue{}, nil
	}
	sorted := make([]*models.PlatformRevenue, len(r.entries))
	copy(sorted, r.entries)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].OccurredAt.Equal(sorted[j].OccurredAt) {
			return sorted[i].ID > sorted[j].ID
		}
		return sorted[i].OccurredAt.After(sorted[j].OccurredAt)
	})
	if len(sorted) > limit {
		sorted = sorted[:limit]
	}
	out := make([]*models.PlatformRevenue, 0, len(sorted))
	for _, e := range sorted {
		copied := *e
		out = append(out, &copied)
	}
	return out, nil
}
