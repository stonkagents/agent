// Package: tracker/internal/repository
// Feature: F-007 (Centralized Tracker)
// Story: US-007-01 (PostgreSQL Schema and Migrations)
// Purpose: In-memory implementation of AssetRepository for MVP/testing

package repository

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// downloadEvent is used for time-windowed trending (e.g. last 24h).
type downloadEvent struct {
	cid string
	at  time.Time
}

// MemoryAssetRepository is a thread-safe in-memory AssetRepository.
type MemoryAssetRepository struct {
	mu             sync.RWMutex
	assets         map[string]*models.Asset // keyed by CID
	downloadEvents []downloadEvent          // bounded by pruning (keep last 48h)
}

// NewMemoryAssetRepository creates a new in-memory asset repository.
func NewMemoryAssetRepository() *MemoryAssetRepository {
	return &MemoryAssetRepository{
		assets: make(map[string]*models.Asset),
	}
}

// Create adds a new asset. Returns ErrAlreadyExists if CID is taken.
func (r *MemoryAssetRepository) Create(ctx context.Context, asset *models.Asset) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.assets[asset.CID]; exists {
		return models.ErrAlreadyExists
	}

	stored := *asset
	r.assets[asset.CID] = &stored
	return nil
}

// FindByCID returns an asset by CID. Returns ErrNotFound if absent.
func (r *MemoryAssetRepository) FindByCID(ctx context.Context, cid string) (*models.Asset, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	asset, exists := r.assets[cid]
	if !exists {
		return nil, models.ErrNotFound
	}

	result := *asset
	return &result, nil
}

// Search returns assets matching the given options.
// Quarantined assets are excluded from results.
// Returns (results, totalMatchCount, error).
func (r *MemoryAssetRepository) Search(ctx context.Context, opts SearchAssetsOptions) ([]*models.Asset, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	peerIDSet := buildPeerIDSet(opts.PeerIDs)
	matched := make([]*models.Asset, 0)

	for _, a := range r.assets {
		if a.Quarantined {
			continue
		}
		if !matchesSearchCriteria(a, opts, peerIDSet) {
			continue
		}
		cp := *a
		matched = append(matched, &cp)
	}

	total := len(matched)

	start := opts.Offset
	if start > total {
		return []*models.Asset{}, total, nil
	}
	end := total
	if opts.Limit > 0 && start+opts.Limit < end {
		end = start + opts.Limit
	}

	return matched[start:end], total, nil
}

// Delete removes an asset by CID. Returns ErrNotFound if absent.
func (r *MemoryAssetRepository) Delete(ctx context.Context, cid string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.assets[cid]; !exists {
		return models.ErrNotFound
	}
	delete(r.assets, cid)
	return nil
}

// SetQuarantined sets the quarantine flag on an asset.
func (r *MemoryAssetRepository) SetQuarantined(ctx context.Context, cid string, quarantined bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	asset, exists := r.assets[cid]
	if !exists {
		return models.ErrNotFound
	}
	asset.Quarantined = quarantined
	return nil
}

func matchesSearchCriteria(a *models.Asset, opts SearchAssetsOptions, peerIDSet map[string]bool) bool {
	if opts.Query != "" && !strings.Contains(
		strings.ToLower(a.Filename), strings.ToLower(opts.Query),
	) {
		return false
	}
	if opts.MimeType != "" && a.MimeType != opts.MimeType {
		return false
	}
	if opts.ManifestType != "" && a.ManifestType != opts.ManifestType {
		return false
	}
	if peerIDSet != nil && !peerIDSet[a.PeerID] {
		return false
	}
	return true
}

// IncrementDownloadCount increments the download count for an asset.
func (r *MemoryAssetRepository) IncrementDownloadCount(ctx context.Context, cid string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	asset, exists := r.assets[cid]
	if !exists {
		return models.ErrNotFound
	}
	asset.DownloadCount++
	return nil
}

// RecordDownloadEvent records a download completion for time-windowed trending.
// Prunes events older than 48h to bound memory.
func (r *MemoryAssetRepository) RecordDownloadEvent(ctx context.Context, cid string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-48 * time.Hour)
	r.downloadEvents = append(r.downloadEvents, downloadEvent{cid: cid, at: now})

	// Prune old events
	n := 0
	for _, e := range r.downloadEvents {
		if e.at.After(cutoff) {
			r.downloadEvents[n] = e
			n++
		}
	}
	r.downloadEvents = r.downloadEvents[:n]
	return nil
}

// ListTrendingSince returns assets with most downloads in [since, now], ordered by count desc.
func (r *MemoryAssetRepository) ListTrendingSince(ctx context.Context, since time.Time, limit, offset int) ([]TrendingRow, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	countByCID := make(map[string]int)
	for _, e := range r.downloadEvents {
		if !e.at.Before(since) {
			countByCID[e.cid]++
		}
	}

	rows := make([]TrendingRow, 0)
	for cid, cnt := range countByCID {
		a, ok := r.assets[cid]
		if !ok || a.Quarantined {
			continue
		}
		cp := *a
		rows = append(rows, TrendingRow{Asset: &cp, CountInWindow: cnt})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CountInWindow != rows[j].CountInWindow {
			return rows[i].CountInWindow > rows[j].CountInWindow
		}
		return rows[i].Asset.AnnouncedAt.After(rows[j].Asset.AnnouncedAt)
	})

	total := len(rows)
	start := offset
	if start > total {
		return []TrendingRow{}, total, nil
	}
	end := total
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	return rows[start:end], total, nil
}

// ListTrending returns assets sorted by download_count DESC, then announced_at DESC.
func (r *MemoryAssetRepository) ListTrending(ctx context.Context, limit, offset int) ([]*models.Asset, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	all := make([]*models.Asset, 0, len(r.assets))
	for _, a := range r.assets {
		if a.Quarantined {
			continue
		}
		cp := *a
		all = append(all, &cp)
	}

	// Sort by download_count DESC, then announced_at DESC
	sort.Slice(all, func(i, j int) bool {
		if all[i].DownloadCount != all[j].DownloadCount {
			return all[i].DownloadCount > all[j].DownloadCount
		}
		return all[i].AnnouncedAt.After(all[j].AnnouncedAt)
	})

	total := len(all)

	start := offset
	if start > total {
		return []*models.Asset{}, total, nil
	}

	end := total
	if limit > 0 && start+limit < end {
		end = start + limit
	}

	return all[start:end], total, nil
}

// Count returns the total number of non-quarantined assets.
func (r *MemoryAssetRepository) Count(ctx context.Context) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	count := 0
	for _, a := range r.assets {
		if !a.Quarantined {
			count++
		}
	}
	return count, nil
}

// CountByPeerID returns the number of assets announced by a specific peer.
func (r *MemoryAssetRepository) CountByPeerID(ctx context.Context, peerID string) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	count := 0
	for _, a := range r.assets {
		if a.PeerID == peerID && !a.Quarantined {
			count++
		}
	}
	return count, nil
}

// CountByPeerIDs returns non-quarantined asset counts for each peer ID. Peers with 0 assets are omitted.
func (r *MemoryAssetRepository) CountByPeerIDs(ctx context.Context, peerIDs []string) (map[string]int, error) {
	if len(peerIDs) == 0 {
		return map[string]int{}, nil
	}
	want := make(map[string]bool, len(peerIDs))
	for _, id := range peerIDs {
		want[id] = true
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	counts := make(map[string]int)
	for _, a := range r.assets {
		if !a.Quarantined && want[a.PeerID] {
			counts[a.PeerID]++
		}
	}
	return counts, nil
}

// TopByDownloads returns the top N non-quarantined assets for a peer, ordered by download_count DESC.
func (r *MemoryAssetRepository) TopByDownloads(ctx context.Context, peerID string, limit int) ([]*models.Asset, error) {
	if limit <= 0 {
		limit = 5
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	var matched []*models.Asset
	for _, a := range r.assets {
		if a.PeerID == peerID && !a.Quarantined {
			cp := *a
			matched = append(matched, &cp)
		}
	}
	sort.Slice(matched, func(i, j int) bool {
		if matched[i].DownloadCount != matched[j].DownloadCount {
			return matched[i].DownloadCount > matched[j].DownloadCount
		}
		return matched[i].AnnouncedAt.After(matched[j].AnnouncedAt)
	})
	if limit < len(matched) {
		matched = matched[:limit]
	}
	return matched, nil
}

// PeerIDsWithAssets returns peer IDs (from the input list) that have at least one non-quarantined asset.
func (r *MemoryAssetRepository) PeerIDsWithAssets(ctx context.Context, peerIDs []string) ([]string, error) {
	if len(peerIDs) == 0 {
		return nil, nil
	}
	want := make(map[string]bool, len(peerIDs))
	for _, id := range peerIDs {
		want[id] = true
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := make(map[string]bool)
	for _, a := range r.assets {
		if !a.Quarantined && want[a.PeerID] && !seen[a.PeerID] {
			seen[a.PeerID] = true
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	return out, nil
}

// RecentAnnouncements returns assets ordered by announced_at DESC.
func (r *MemoryAssetRepository) RecentAnnouncements(ctx context.Context, limit int) ([]*models.Asset, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	all := make([]*models.Asset, 0, len(r.assets))
	for _, a := range r.assets {
		if a.Quarantined {
			continue
		}
		cp := *a
		all = append(all, &cp)
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].AnnouncedAt.After(all[j].AnnouncedAt)
	})
	if limit > 0 && limit < len(all) {
		all = all[:limit]
	}
	return all, nil
}

// RecentDownloadEvents returns recent download events with asset filename.
func (r *MemoryAssetRepository) RecentDownloadEvents(ctx context.Context, limit int) ([]DownloadEventRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	events := make([]DownloadEventRecord, 0)
	for i := len(r.downloadEvents) - 1; i >= 0; i-- {
		e := r.downloadEvents[i]
		a, ok := r.assets[e.cid]
		if !ok || a.Quarantined {
			continue
		}
		events = append(events, DownloadEventRecord{
			CID:      e.cid,
			Filename: a.Filename,
			At:       e.at,
		})
		if limit > 0 && len(events) >= limit {
			break
		}
	}
	return events, nil
}

func buildPeerIDSet(peerIDs []string) map[string]bool {
	if len(peerIDs) == 0 {
		return nil
	}
	set := make(map[string]bool, len(peerIDs))
	for _, id := range peerIDs {
		set[id] = true
	}
	return set
}
