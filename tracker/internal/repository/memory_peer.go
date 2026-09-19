// Package: tracker/internal/repository
// Feature: F-007 (Centralized Tracker)
// Story: US-007-01 (PostgreSQL Schema and Migrations)
// Purpose: In-memory implementation of PeerRepository for MVP/testing

package repository

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// MemoryPeerRepository is a thread-safe in-memory PeerRepository.
type MemoryPeerRepository struct {
	mu    sync.RWMutex
	peers map[string]*models.Peer
}

// NewMemoryPeerRepository creates a new in-memory peer repository.
func NewMemoryPeerRepository() *MemoryPeerRepository {
	return &MemoryPeerRepository{
		peers: make(map[string]*models.Peer),
	}
}

// Create adds a new peer. Returns ErrAlreadyExists if peer_id is taken.
func (r *MemoryPeerRepository) Create(ctx context.Context, peer *models.Peer) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.peers[peer.PeerID]; exists {
		return models.ErrAlreadyExists
	}

	stored := *peer
	stored.Multiaddrs = copyStrings(peer.Multiaddrs)
	r.peers[peer.PeerID] = &stored
	return nil
}

// Upsert creates or updates a peer.
func (r *MemoryPeerRepository) Upsert(ctx context.Context, peer *models.Peer) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	existing, exists := r.peers[peer.PeerID]
	stored := *peer
	stored.Multiaddrs = copyStrings(peer.Multiaddrs)

	if exists {
		stored.FirstSeen = existing.FirstSeen
	}

	r.peers[peer.PeerID] = &stored
	return nil
}

// FindByID returns a peer by ID. Returns ErrNotFound if absent.
func (r *MemoryPeerRepository) FindByID(ctx context.Context, peerID string) (*models.Peer, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	peer, exists := r.peers[peerID]
	if !exists {
		return nil, models.ErrNotFound
	}

	result := *peer
	result.Multiaddrs = copyStrings(peer.Multiaddrs)
	return &result, nil
}

// List returns peers with optional limit/offset.
func (r *MemoryPeerRepository) List(ctx context.Context, opts ListPeersOptions) ([]*models.Peer, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	all := make([]*models.Peer, 0, len(r.peers))
	for _, p := range r.peers {
		cp := *p
		cp.Multiaddrs = copyStrings(p.Multiaddrs)
		all = append(all, &cp)
	}

	start := opts.Offset
	if start > len(all) {
		return []*models.Peer{}, nil
	}

	end := len(all)
	if opts.Limit > 0 && start+opts.Limit < end {
		end = start + opts.Limit
	}

	return all[start:end], nil
}

// FindByIDs returns peers matching any of the given IDs.
func (r *MemoryPeerRepository) FindByIDs(ctx context.Context, peerIDs []string) ([]*models.Peer, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*models.Peer, 0)
	for _, id := range peerIDs {
		if p, exists := r.peers[id]; exists {
			cp := *p
			cp.Multiaddrs = copyStrings(p.Multiaddrs)
			result = append(result, &cp)
		}
	}
	return result, nil
}

// Delete removes a peer by ID. Returns ErrNotFound if absent.
func (r *MemoryPeerRepository) Delete(ctx context.Context, peerID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.peers[peerID]; !exists {
		return models.ErrNotFound
	}
	delete(r.peers, peerID)
	return nil
}

// UpdateTransferStats updates a peer's transfer statistics.
func (r *MemoryPeerRepository) UpdateTransferStats(ctx context.Context, peerID string, uploadBytes, downloadBytes int64, avgSpeed *int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	peer, exists := r.peers[peerID]
	if !exists {
		return models.ErrNotFound
	}

	peer.TotalUploadBytes = uploadBytes
	peer.TotalDownloadBytes = downloadBytes
	peer.AverageSpeedBytesPerSec = avgSpeed
	return nil
}

// DisplayNamesByIDs returns peer_id -> display_name for known peers that have a name.
func (r *MemoryPeerRepository) DisplayNamesByIDs(ctx context.Context, peerIDs []string) (map[string]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make(map[string]string, len(peerIDs))
	for _, id := range peerIDs {
		if p, exists := r.peers[id]; exists && p.DisplayName != "" {
			out[id] = p.DisplayName
		}
	}
	return out, nil
}

// PeerIDsByDisplayNames returns lowercased display name -> peer_id for the given names.
func (r *MemoryPeerRepository) PeerIDsByDisplayNames(ctx context.Context, lowerNames []string) (map[string]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	want := make(map[string]struct{}, len(lowerNames))
	for _, n := range lowerNames {
		want[strings.ToLower(n)] = struct{}{}
	}
	out := make(map[string]string, len(want))
	for _, p := range r.peers {
		if p.DisplayName == "" {
			continue
		}
		lower := strings.ToLower(p.DisplayName)
		if _, ok := want[lower]; ok {
			if _, dup := out[lower]; !dup || p.PeerID < out[lower] {
				out[lower] = p.PeerID
			}
		}
	}
	return out, nil
}

// SearchDisplayNames returns peers whose display name starts with prefix (case-insensitive),
// shortest name first then alphabetical, at most limit.
func (r *MemoryPeerRepository) SearchDisplayNames(ctx context.Context, prefix string, limit int) ([]DisplayNameMatch, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	lower := strings.ToLower(prefix)
	var out []DisplayNameMatch
	for _, p := range r.peers {
		if p.DisplayName != "" && strings.HasPrefix(strings.ToLower(p.DisplayName), lower) {
			out = append(out, DisplayNameMatch{PeerID: p.PeerID, DisplayName: p.DisplayName})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].DisplayName) != len(out[j].DisplayName) {
			return len(out[i].DisplayName) < len(out[j].DisplayName)
		}
		a, b := strings.ToLower(out[i].DisplayName), strings.ToLower(out[j].DisplayName)
		if a != b {
			return a < b
		}
		return out[i].PeerID < out[j].PeerID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// UpdateDisplayName sets (or, with an empty name, clears) a peer's display name.
func (r *MemoryPeerRepository) UpdateDisplayName(ctx context.Context, peerID, displayName string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	peer, exists := r.peers[peerID]
	if !exists {
		return models.ErrNotFound
	}
	peer.DisplayName = displayName
	return nil
}

// ListPeersByUploadBytes returns peers sorted by upload bytes DESC.
// If hasAssets is true, only includes peers that have announced at least one asset.
func (r *MemoryPeerRepository) ListPeersByUploadBytes(ctx context.Context, limit, offset int, hasAssets bool) ([]*models.Peer, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	all := make([]*models.Peer, 0, len(r.peers))
	for _, p := range r.peers {
		if hasAssets {
			// Filter: only peers with assets (we'll need asset repo for this check)
			// For now, include all peers - filtering will be done in service layer
		}
		cp := *p
		cp.Multiaddrs = copyStrings(p.Multiaddrs)
		all = append(all, &cp)
	}

	// Sort by upload bytes DESC
	sort.Slice(all, func(i, j int) bool {
		return all[i].TotalUploadBytes > all[j].TotalUploadBytes
	})

	start := offset
	if start > len(all) {
		return []*models.Peer{}, nil
	}

	end := len(all)
	if limit > 0 && start+limit < end {
		end = start + limit
	}

	return all[start:end], nil
}

// ListPeersByDownloadBytes returns peers sorted by download bytes DESC.
// If hasDownloads is true, only includes peers with download_bytes > 0.
func (r *MemoryPeerRepository) ListPeersByDownloadBytes(ctx context.Context, limit, offset int, hasDownloads bool) ([]*models.Peer, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	all := make([]*models.Peer, 0, len(r.peers))
	for _, p := range r.peers {
		if hasDownloads && p.TotalDownloadBytes == 0 {
			continue
		}
		cp := *p
		cp.Multiaddrs = copyStrings(p.Multiaddrs)
		all = append(all, &cp)
	}

	// Sort by download bytes DESC
	sort.Slice(all, func(i, j int) bool {
		return all[i].TotalDownloadBytes > all[j].TotalDownloadBytes
	})

	start := offset
	if start > len(all) {
		return []*models.Peer{}, nil
	}

	end := len(all)
	if limit > 0 && start+limit < end {
		end = start + limit
	}

	return all[start:end], nil
}

// Count returns the total number of registered peers.
func (r *MemoryPeerRepository) Count(ctx context.Context) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.peers), nil
}

// CountOnlineByCountry returns online peer counts by country (last_seen > since). Excludes empty country.
func (r *MemoryPeerRepository) CountOnlineByCountry(ctx context.Context, since time.Time) ([]CountryCount, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	byCountry := make(map[string]int)
	for _, p := range r.peers {
		if p.LastSeen.Before(since) || p.LastSeen.Equal(since) {
			continue
		}
		if p.Country == "" {
			continue
		}
		byCountry[p.Country]++
	}
	out := make([]CountryCount, 0, len(byCountry))
	for country, count := range byCountry {
		out = append(out, CountryCount{Country: country, Count: count})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out, nil
}

// RecentlyJoined returns peers ordered by first_seen DESC (newest first).
func (r *MemoryPeerRepository) RecentlyJoined(ctx context.Context, limit int) ([]*models.Peer, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	all := make([]*models.Peer, 0, len(r.peers))
	for _, p := range r.peers {
		cp := *p
		cp.Multiaddrs = copyStrings(p.Multiaddrs)
		all = append(all, &cp)
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].FirstSeen.After(all[j].FirstSeen)
	})
	if limit > 0 && limit < len(all) {
		all = all[:limit]
	}
	return all, nil
}

func copyStrings(s []string) []string {
	if s == nil {
		return nil
	}
	cp := make([]string, len(s))
	copy(cp, s)
	return cp
}

// PeerIDsSeenSince returns the peers with last_seen at or after since, most recent first.
func (r *MemoryPeerRepository) PeerIDsSeenSince(ctx context.Context, since time.Time, limit int) ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	all := make([]*models.Peer, 0, len(r.peers))
	for _, p := range r.peers {
		if !p.LastSeen.Before(since) {
			all = append(all, p)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].LastSeen.Equal(all[j].LastSeen) {
			return all[i].PeerID < all[j].PeerID
		}
		return all[i].LastSeen.After(all[j].LastSeen)
	})
	out := []string{}
	for _, p := range all {
		if limit > 0 && len(out) >= limit {
			break
		}
		out = append(out, p.PeerID)
	}
	return out, nil
}
