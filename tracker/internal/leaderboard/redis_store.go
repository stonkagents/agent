// Package leaderboard: Redis-backed leaderboard cache (sorted sets) with Postgres hydration.
package leaderboard

import (
	"context"

	"github.com/redis/go-redis/v9"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

const (
	keyUpload   = "leaderboard:upload"
	keyDownload = "leaderboard:download"
)

// RedisStore implements Store using Redis sorted sets for scores and peerRepo for hydration.
type RedisStore struct {
	rdb       *redis.Client
	peerRepo  repository.PeerRepository
	assetRepo repository.AssetRepository
}

// NewRedisStore creates a new Redis leaderboard store.
func NewRedisStore(rdb *redis.Client, peerRepo repository.PeerRepository, assetRepo repository.AssetRepository) *RedisStore {
	return &RedisStore{rdb: rdb, peerRepo: peerRepo, assetRepo: assetRepo}
}

// TopSeeders returns top seeders (peers with assets, sorted by upload). Uses Redis for order, peerRepo for details.
func (s *RedisStore) TopSeeders(ctx context.Context, limit, offset int) ([]Entry, error) {
	fetch := offset + limit*2
	if fetch < 50 {
		fetch = 50
	}
	peerIDs, err := s.rdb.ZRevRange(ctx, keyUpload, int64(offset), int64(offset+fetch-1)).Result()
	if err != nil {
		return nil, err
	}
	if len(peerIDs) == 0 {
		return []Entry{}, nil
	}
	seederIDs, err := s.assetRepo.PeerIDsWithAssets(ctx, peerIDs)
	if err != nil {
		return nil, err
	}
	seederSet := make(map[string]bool, len(seederIDs))
	for _, id := range seederIDs {
		seederSet[id] = true
	}
	peers, err := s.peerRepo.FindByIDs(ctx, peerIDs)
	if err != nil {
		return nil, err
	}
	var entries []Entry
	rank := offset + 1
	for _, p := range peers {
		if !seederSet[p.PeerID] {
			continue
		}
		entries = append(entries, Entry{
			Rank:                    rank,
			MaskedPeerID:            p.MaskedPeerID,
			DisplayName:             p.DisplayName,
			Country:                 p.Country,
			Region:                  p.Region,
			TotalUploadBytes:        p.TotalUploadBytes,
			AverageSpeedBytesPerSec: p.AverageSpeedBytesPerSec,
			FirstSeen:               p.FirstSeen.Format("2006-01-02T15:04:05Z"),
			LastSeen:                p.LastSeen.Format("2006-01-02T15:04:05Z"),
		})
		rank++
		if len(entries) >= limit {
			break
		}
	}
	return entries, nil
}

// TopLeechers returns top leechers by download bytes. Uses Redis for order, peerRepo for details.
func (s *RedisStore) TopLeechers(ctx context.Context, limit, offset int) ([]Entry, error) {
	peerIDs, err := s.rdb.ZRevRange(ctx, keyDownload, int64(offset), int64(offset+limit-1)).Result()
	if err != nil {
		return nil, err
	}
	if len(peerIDs) == 0 {
		return []Entry{}, nil
	}
	peers, err := s.peerRepo.FindByIDs(ctx, peerIDs)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*models.Peer)
	for _, p := range peers {
		byID[p.PeerID] = p
	}
	entries := make([]Entry, 0, len(peerIDs))
	rank := offset + 1
	for _, id := range peerIDs {
		p, ok := byID[id]
		if !ok {
			continue
		}
		entries = append(entries, Entry{
			Rank:                    rank,
			MaskedPeerID:            p.MaskedPeerID,
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

// UpdateTransferStats updates Redis sorted sets with the peer's upload/download bytes.
func (s *RedisStore) UpdateTransferStats(ctx context.Context, peerID string, uploadBytes, downloadBytes int64) error {
	pipe := s.rdb.Pipeline()
	pipe.ZAdd(ctx, keyUpload, redis.Z{Score: float64(uploadBytes), Member: peerID})
	pipe.ZAdd(ctx, keyDownload, redis.Z{Score: float64(downloadBytes), Member: peerID})
	_, err := pipe.Exec(ctx)
	return err
}
