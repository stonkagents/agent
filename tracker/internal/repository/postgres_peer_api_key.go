// Package repository: Postgres implementation of PeerAPIKeyRepository.
// Feature: Peer Forum + API Key Auth

package repository

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// PostgresPeerAPIKeyRepository implements PeerAPIKeyRepository using peer_api_keys table.
type PostgresPeerAPIKeyRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresPeerAPIKeyRepository creates a new Postgres peer API key repository.
func NewPostgresPeerAPIKeyRepository(pool *pgxpool.Pool) *PostgresPeerAPIKeyRepository {
	return &PostgresPeerAPIKeyRepository{pool: pool}
}

// Create generates a new API key and stores it for the peer. Returns the key. Call only on first register.
func (r *PostgresPeerAPIKeyRepository) Create(ctx context.Context, peerID string) (apiKey string, err error) {
	key, err := GenerateAPIKey()
	if err != nil {
		return "", err
	}
	_, err = r.pool.Exec(ctx, `INSERT INTO peer_api_keys (peer_id, api_key) VALUES ($1, $2)`, peerID, key)
	if err != nil {
		return "", err
	}
	return key, nil
}

// GetByAPIKey returns the peer_id for the given API key, or models.ErrNotFound.
func (r *PostgresPeerAPIKeyRepository) GetByAPIKey(ctx context.Context, apiKey string) (peerID string, err error) {
	err = r.pool.QueryRow(ctx, `SELECT peer_id FROM peer_api_keys WHERE api_key = $1`, apiKey).Scan(&peerID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", models.ErrNotFound
		}
		return "", err
	}
	return peerID, nil
}

// GetByPeerID returns the api_key for the given peer, or models.ErrNotFound.
func (r *PostgresPeerAPIKeyRepository) GetByPeerID(ctx context.Context, peerID string) (apiKey string, err error) {
	err = r.pool.QueryRow(ctx, `SELECT api_key FROM peer_api_keys WHERE peer_id = $1`, peerID).Scan(&apiKey)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", models.ErrNotFound
		}
		return "", err
	}
	return apiKey, nil
}

// ExistsForPeer returns true if the peer already has an API key.
func (r *PostgresPeerAPIKeyRepository) ExistsForPeer(ctx context.Context, peerID string) (bool, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT 1 FROM peer_api_keys WHERE peer_id = $1`, peerID).Scan(&n)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
