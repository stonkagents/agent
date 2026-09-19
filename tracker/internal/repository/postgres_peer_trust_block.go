// Package repository: Postgres implementation of PeerTrustBlockRepository.

package repository

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresPeerTrustBlockRepository implements PeerTrustBlockRepository.
type PostgresPeerTrustBlockRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresPeerTrustBlockRepository creates a new Postgres peer trust/block repository.
func NewPostgresPeerTrustBlockRepository(pool *pgxpool.Pool) *PostgresPeerTrustBlockRepository {
	return &PostgresPeerTrustBlockRepository{pool: pool}
}

// Trust adds actor->target trust. Idempotent.
func (r *PostgresPeerTrustBlockRepository) Trust(ctx context.Context, actorPeerID, targetPeerID string) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO peer_relationships (actor_peer_id, target_peer_id, relationship) VALUES ($1, $2, 'trust')
		 ON CONFLICT (actor_peer_id, target_peer_id, relationship) DO NOTHING`,
		actorPeerID, targetPeerID,
	)
	return err
}

// Untrust removes actor->target trust.
func (r *PostgresPeerTrustBlockRepository) Untrust(ctx context.Context, actorPeerID, targetPeerID string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM peer_relationships WHERE actor_peer_id = $1 AND target_peer_id = $2 AND relationship = 'trust'`,
		actorPeerID, targetPeerID,
	)
	return err
}

// Block adds actor->target block. Idempotent.
func (r *PostgresPeerTrustBlockRepository) Block(ctx context.Context, actorPeerID, targetPeerID string) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO peer_relationships (actor_peer_id, target_peer_id, relationship) VALUES ($1, $2, 'block')
		 ON CONFLICT (actor_peer_id, target_peer_id, relationship) DO NOTHING`,
		actorPeerID, targetPeerID,
	)
	return err
}

// Unblock removes actor->target block.
func (r *PostgresPeerTrustBlockRepository) Unblock(ctx context.Context, actorPeerID, targetPeerID string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM peer_relationships WHERE actor_peer_id = $1 AND target_peer_id = $2 AND relationship = 'block'`,
		actorPeerID, targetPeerID,
	)
	return err
}

// IsTrusted returns true if actor has trusted target.
func (r *PostgresPeerTrustBlockRepository) IsTrusted(ctx context.Context, actorPeerID, targetPeerID string) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM peer_relationships WHERE actor_peer_id = $1 AND target_peer_id = $2 AND relationship = 'trust')`,
		actorPeerID, targetPeerID,
	).Scan(&exists)
	return exists, err
}

// IsBlocked returns true if actor has blocked target.
func (r *PostgresPeerTrustBlockRepository) IsBlocked(ctx context.Context, actorPeerID, targetPeerID string) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM peer_relationships WHERE actor_peer_id = $1 AND target_peer_id = $2 AND relationship = 'block')`,
		actorPeerID, targetPeerID,
	).Scan(&exists)
	return exists, err
}

// TrustedByActor returns peer IDs that actor has trusted.
func (r *PostgresPeerTrustBlockRepository) TrustedByActor(ctx context.Context, actorPeerID string) ([]string, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT target_peer_id FROM peer_relationships WHERE actor_peer_id = $1 AND relationship = 'trust'`,
		actorPeerID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// CountTrustsReceived returns how many distinct actors have trusted the target peer. (F-032, US-032-02)
func (r *PostgresPeerTrustBlockRepository) CountTrustsReceived(ctx context.Context, targetPeerID string) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM peer_relationships WHERE target_peer_id = $1 AND relationship = 'trust'`,
		targetPeerID,
	).Scan(&count)
	return count, err
}

// BlockedByActor returns peer IDs that actor has blocked.
func (r *PostgresPeerTrustBlockRepository) BlockedByActor(ctx context.Context, actorPeerID string) ([]string, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT target_peer_id FROM peer_relationships WHERE actor_peer_id = $1 AND relationship = 'block'`,
		actorPeerID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
