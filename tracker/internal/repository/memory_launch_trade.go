// Package: tracker/internal/repository
// Feature: StonkAgents launchpad (trade indexer)
// Purpose: In-memory LaunchTradeRepository and LaunchBurnRepository for tests and offline development

package repository

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// MemoryLaunchTradeRepository is a thread-safe in-memory LaunchTradeRepository.
type MemoryLaunchTradeRepository struct {
	mu      sync.RWMutex
	trades  map[string]*models.LaunchTrade // by signature
	cursors map[string]*models.LaunchIndexCursor
}

// NewMemoryLaunchTradeRepository creates an empty in-memory trade repository.
func NewMemoryLaunchTradeRepository() *MemoryLaunchTradeRepository {
	return &MemoryLaunchTradeRepository{
		trades:  make(map[string]*models.LaunchTrade),
		cursors: make(map[string]*models.LaunchIndexCursor),
	}
}

// tradeNewer orders trades newest first by (block_time, slot, signature).
func tradeNewer(a, b *models.LaunchTrade) bool {
	if !a.BlockTime.Equal(b.BlockTime) {
		return a.BlockTime.After(b.BlockTime)
	}
	if a.Slot != b.Slot {
		return a.Slot > b.Slot
	}
	return a.Signature > b.Signature
}

// InsertTrades stores trades idempotently on signature; returns how many were new.
func (r *MemoryLaunchTradeRepository) InsertTrades(_ context.Context, trades []*models.LaunchTrade) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, t := range trades {
		if t == nil || t.Signature == "" {
			continue
		}
		if _, dup := r.trades[t.Signature]; dup {
			continue
		}
		stored := *t
		r.trades[t.Signature] = &stored
		n++
	}
	return n, nil
}

// forMint returns copies of a mint's trades, newest first.
func (r *MemoryLaunchTradeRepository) forMint(mint string) []*models.LaunchTrade {
	out := make([]*models.LaunchTrade, 0)
	for _, t := range r.trades {
		if t.Mint == mint {
			c := *t
			out = append(out, &c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return tradeNewer(out[i], out[j]) })
	return out
}

// ListTrades returns a mint's trades newest first, after the cursor.
func (r *MemoryLaunchTradeRepository) ListTrades(_ context.Context, mint string, limit int, after *TradeCursor) ([]*models.LaunchTrade, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	all := r.forMint(mint)
	out := make([]*models.LaunchTrade, 0, limit)
	for _, t := range all {
		if after != nil {
			pivot := &models.LaunchTrade{BlockTime: after.BlockTime, Slot: after.Slot, Signature: after.Signature}
			if !tradeNewer(pivot, t) {
				continue
			}
		}
		out = append(out, t)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// ListTradesAsc returns a mint's trades with block_time >= since, oldest first.
func (r *MemoryLaunchTradeRepository) ListTradesAsc(_ context.Context, mint string, since time.Time, limit int) ([]*models.LaunchTrade, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	all := r.forMint(mint)
	out := make([]*models.LaunchTrade, 0)
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].BlockTime.Before(since) {
			continue
		}
		out = append(out, all[i])
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// AggregateWindow returns per-mint volume, count and boundary prices for (from, to].
func (r *MemoryLaunchTradeRepository) AggregateWindow(_ context.Context, mints []string, from, to time.Time) (map[string]*models.LaunchTradeWindow, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]*models.LaunchTradeWindow)
	for _, mint := range mints {
		all := r.forMint(mint) // newest first
		if len(all) == 0 {
			continue
		}
		w := &models.LaunchTradeWindow{Mint: mint, QuoteSymbol: all[0].QuoteSymbol}
		for _, t := range all {
			if w.PriceAtTo == nil && !t.BlockTime.After(to) {
				p := t.PriceQuote
				w.PriceAtTo = &p
			}
			if w.PriceAtFrom == nil && !t.BlockTime.After(from) {
				p := t.PriceQuote
				w.PriceAtFrom = &p
			}
			if t.BlockTime.After(from) && !t.BlockTime.After(to) {
				w.VolumeQuote += t.QuoteAmount
				w.Trades++
			}
		}
		out[mint] = w
	}
	return out, nil
}

// GetCursor returns the index cursor for key, or ErrNotFound.
func (r *MemoryLaunchTradeRepository) GetCursor(_ context.Context, key string) (*models.LaunchIndexCursor, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.cursors[key]
	if !ok {
		return nil, models.ErrNotFound
	}
	out := *c
	return &out, nil
}

// UpsertCursor stores the index cursor for cursor.Key.
func (r *MemoryLaunchTradeRepository) UpsertCursor(_ context.Context, cursor *models.LaunchIndexCursor) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored := *cursor
	if stored.UpdatedAt.IsZero() {
		stored.UpdatedAt = time.Now().UTC()
	}
	r.cursors[cursor.Key] = &stored
	return nil
}

// Count returns the number of stored trades (tests).
func (r *MemoryLaunchTradeRepository) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.trades)
}

// MemoryLaunchBurnRepository is a thread-safe in-memory LaunchBurnRepository.
type MemoryLaunchBurnRepository struct {
	mu    sync.RWMutex
	burns map[string]*models.LaunchBurn // by signature
}

// NewMemoryLaunchBurnRepository creates an empty in-memory burn ledger.
func NewMemoryLaunchBurnRepository() *MemoryLaunchBurnRepository {
	return &MemoryLaunchBurnRepository{burns: make(map[string]*models.LaunchBurn)}
}

// InsertBurns stores burns idempotently on signature; returns how many were new.
func (r *MemoryLaunchBurnRepository) InsertBurns(_ context.Context, burns []*models.LaunchBurn) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, b := range burns {
		if b == nil || b.Signature == "" {
			continue
		}
		if _, dup := r.burns[b.Signature]; dup {
			continue
		}
		stored := *b
		r.burns[b.Signature] = &stored
		n++
	}
	return n, nil
}

func (r *MemoryLaunchBurnRepository) forMint(mint string) []*models.LaunchBurn {
	out := make([]*models.LaunchBurn, 0)
	for _, b := range r.burns {
		if b.Mint == mint {
			c := *b
			out = append(out, &c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].BlockTime.Equal(out[j].BlockTime) {
			return out[i].BlockTime.After(out[j].BlockTime)
		}
		if out[i].Slot != out[j].Slot {
			return out[i].Slot > out[j].Slot
		}
		return out[i].Signature > out[j].Signature
	})
	return out
}

// Summary returns the total burned and the burn count for mint.
func (r *MemoryLaunchBurnRepository) Summary(_ context.Context, mint string) (*models.LaunchBurnSummary, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s := &models.LaunchBurnSummary{}
	for _, b := range r.forMint(mint) {
		s.Burned += b.Amount
		s.Burns++
	}
	return s, nil
}

// Recent returns the newest burns first, at most limit.
func (r *MemoryLaunchBurnRepository) Recent(_ context.Context, mint string, limit int) ([]*models.LaunchBurn, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	all := r.forMint(mint)
	if limit > 0 && limit < len(all) {
		all = all[:limit]
	}
	return all, nil
}
