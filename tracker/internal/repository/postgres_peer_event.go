// Package: tracker/internal/repository
// Feature: F-032 (Peers & Reputation)
// Story: US-032-02 (Peer Activity & Reputation)
// Purpose: Postgres implementation of PeerEventRepository (TD-062)

package repository

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// PostgresPeerEventRepository implements PeerEventRepository with Postgres.
type PostgresPeerEventRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresPeerEventRepository creates a new Postgres peer event repository.
func NewPostgresPeerEventRepository(pool *pgxpool.Pool) *PostgresPeerEventRepository {
	return &PostgresPeerEventRepository{pool: pool}
}

// Insert adds a new peer event.
func (r *PostgresPeerEventRepository) Insert(ctx context.Context, event *models.PeerEvent) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO peer_events (peer_id, action, details, created_at)
		 VALUES ($1, $2, $3, $4)`,
		event.PeerID, event.Action, event.Details, event.CreatedAt,
	)
	return err
}

// ListByPeerID returns events for a peer, ordered by CreatedAt DESC.
// Returns (events, total, error).
func (r *PostgresPeerEventRepository) ListByPeerID(ctx context.Context, peerID string, limit, offset int) ([]*models.PeerEvent, int, error) {
	// Get total count
	var total int
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM peer_events WHERE peer_id = $1`,
		peerID,
	).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	if total == 0 {
		return []*models.PeerEvent{}, 0, nil
	}

	rows, err := r.pool.Query(ctx,
		`SELECT id, peer_id, action, details, created_at
		 FROM peer_events
		 WHERE peer_id = $1
		 ORDER BY created_at DESC
		 LIMIT $2 OFFSET $3`,
		peerID, limit, offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var events []*models.PeerEvent
	for rows.Next() {
		e := &models.PeerEvent{}
		if err := rows.Scan(&e.ID, &e.PeerID, &e.Action, &e.Details, &e.CreatedAt); err != nil {
			return nil, 0, err
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return events, total, nil
}

// CountByPeerIDAndAction returns the count of events for a peer with the given action.
func (r *PostgresPeerEventRepository) CountByPeerIDAndAction(ctx context.Context, peerID, action string) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM peer_events WHERE peer_id = $1 AND action = $2`,
		peerID, action,
	).Scan(&count)
	return count, err
}
