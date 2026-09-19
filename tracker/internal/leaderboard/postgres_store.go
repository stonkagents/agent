// Package leaderboard: Postgres-backed leaderboard store (delegates to peer and asset repos).
package leaderboard

import (
	"context"

	"github.com/stonkagents/agent/tracker/internal/geo"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// PostgresStore implements Store by querying peer and asset repositories.
type PostgresStore struct {
	peerRepo  repository.PeerRepository
	assetRepo repository.AssetRepository
}

// NewPostgresStore creates a new Postgres leaderboard store.
func NewPostgresStore(peerRepo repository.PeerRepository, assetRepo repository.AssetRepository) *PostgresStore {
	return &PostgresStore{peerRepo: peerRepo, assetRepo: assetRepo}
}

// TopSeeders returns peers with assets, sorted by upload bytes DESC.
// ListPeersByUploadBytes(..., true) already filters to peers with at least one non-quarantined asset.
func (s *PostgresStore) TopSeeders(ctx context.Context, limit, offset int) ([]Entry, error) {
	peers, err := s.peerRepo.ListPeersByUploadBytes(ctx, limit, offset, true)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(peers))
	rank := offset + 1
	for _, p := range peers {
		masked := p.MaskedPeerID
		if masked == "" {
			masked = geo.MaskPeerID(p.PeerID)
		}
		entries = append(entries, Entry{
			Rank:                    rank,
			MaskedPeerID:            masked,
			DisplayName:             p.DisplayName,
			Country:                 p.Country,
			Region:                  p.Region,
			TotalUploadBytes:        p.TotalUploadBytes,
			AverageSpeedBytesPerSec: p.AverageSpeedBytesPerSec,
			FirstSeen:               p.FirstSeen.Format("2006-01-02T15:04:05Z"),
			LastSeen:                p.LastSeen.Format("2006-01-02T15:04:05Z"),
		})
		rank++
	}
	return entries, nil
}

// TopLeechers returns peers with downloads, sorted by download bytes DESC.
func (s *PostgresStore) TopLeechers(ctx context.Context, limit, offset int) ([]Entry, error) {
	peers, err := s.peerRepo.ListPeersByDownloadBytes(ctx, limit, offset, true)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(peers))
	rank := offset + 1
	for _, p := range peers {
		masked := p.MaskedPeerID
		if masked == "" {
			masked = geo.MaskPeerID(p.PeerID)
		}
		entries = append(entries, Entry{
			Rank:                    rank,
			MaskedPeerID:            masked,
			DisplayName:             p.DisplayName,
			Country:                 p.Country,
			Region:                  p.Region,
			TotalDownloadBytes:      p.TotalDownloadBytes,
			AverageSpeedBytesPerSec: p.AverageSpeedBytesPerSec,
			FirstSeen:               p.FirstSeen.Format("2006-01-02T15:04:05Z"),
			LastSeen:                p.LastSeen.Format("2006-01-02T15:04:05Z"),
		})
		rank++
	}
	return entries, nil
}

// UpdateTransferStats is a no-op for Postgres (source of truth is already updated).
func (s *PostgresStore) UpdateTransferStats(ctx context.Context, peerID string, uploadBytes, downloadBytes int64) error {
	return nil
}
