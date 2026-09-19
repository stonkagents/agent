// Package: internal/daemon/history
// Feature: F-029 (Transfer Page E2E)
// Story: US-029-05 (Transfer History)
// Purpose: SQLite repository for completed/failed transfer history with 10k retention cap

package history

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// TransferRecord represents a single history entry.
type TransferRecord struct {
	ID           int64     `json:"id"`
	CID          string    `json:"cid"`
	Filename     string    `json:"filename"`
	FileType     string    `json:"file_type"`
	Direction    string    `json:"direction"`
	TotalSize    int64     `json:"total_size"`
	State        string    `json:"state"`
	ErrorMessage string    `json:"error_message,omitempty"`
	StartedAt    time.Time `json:"started_at"`
	CompletedAt  time.Time `json:"completed_at"`
	PeerID       string    `json:"peer_id,omitempty"`
}

// Repository manages transfer history persistence.
type Repository struct {
	db *sql.DB
}

// NewRepository opens (or creates) the history SQLite database.
func NewRepository(dbPath string) (*Repository, error) {
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("failed to open history db: %w", err)
	}

	if err := createSchema(db); err != nil {
		db.Close()
		return nil, err
	}

	return &Repository{db: db}, nil
}

func createSchema(db *sql.DB) error {
	schema := `
	CREATE TABLE IF NOT EXISTS transfer_history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		cid TEXT NOT NULL,
		filename TEXT NOT NULL,
		file_type TEXT NOT NULL DEFAULT 'file',
		direction TEXT NOT NULL CHECK(direction IN ('download','upload')),
		total_size INTEGER NOT NULL DEFAULT 0,
		state TEXT NOT NULL CHECK(state IN ('completed','failed')),
		error_message TEXT,
		started_at TEXT NOT NULL,
		completed_at TEXT NOT NULL,
		peer_id TEXT NOT NULL DEFAULT '',
		UNIQUE(cid, direction, completed_at)
	);
	CREATE INDEX IF NOT EXISTS idx_history_completed_at ON transfer_history(completed_at DESC);
	CREATE INDEX IF NOT EXISTS idx_history_cid ON transfer_history(cid);
	`
	_, err := db.Exec(schema)
	if err != nil {
		return fmt.Errorf("failed to create history schema: %w", err)
	}
	return nil
}

// Insert adds a transfer record to history and enforces 10k retention cap (AC-12b).
func (r *Repository) Insert(rec *TransferRecord) error {
	_, err := r.db.Exec(
		`INSERT INTO transfer_history (cid, filename, file_type, direction, total_size, state, error_message, started_at, completed_at, peer_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.CID, rec.Filename, rec.FileType, rec.Direction, rec.TotalSize,
		rec.State, rec.ErrorMessage,
		rec.StartedAt.UTC().Format(time.RFC3339),
		rec.CompletedAt.UTC().Format(time.RFC3339),
		rec.PeerID,
	)
	if err != nil {
		return err
	}

	// Enforce 10k retention cap — prune oldest records beyond limit.
	_, _ = r.db.Exec(
		`DELETE FROM transfer_history WHERE id NOT IN (
			SELECT id FROM transfer_history ORDER BY completed_at DESC LIMIT 10000
		)`,
	)

	return nil
}

// List returns paginated transfer history (most recent first).
func (r *Repository) List(limit, offset int) ([]*TransferRecord, int, error) {
	var total int
	if err := r.db.QueryRow("SELECT COUNT(*) FROM transfer_history").Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("failed to count history: %w", err)
	}

	rows, err := r.db.Query(
		`SELECT id, cid, filename, file_type, direction, total_size, state, error_message, started_at, completed_at, peer_id
		 FROM transfer_history ORDER BY completed_at DESC LIMIT ? OFFSET ?`,
		limit, offset,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to query history: %w", err)
	}
	defer rows.Close()

	var records []*TransferRecord
	for rows.Next() {
		rec := &TransferRecord{}
		var startedStr, completedStr string
		var errMsg sql.NullString
		if err := rows.Scan(
			&rec.ID, &rec.CID, &rec.Filename, &rec.FileType, &rec.Direction,
			&rec.TotalSize, &rec.State, &errMsg, &startedStr, &completedStr, &rec.PeerID,
		); err != nil {
			return nil, 0, fmt.Errorf("failed to scan history row: %w", err)
		}
		rec.ErrorMessage = errMsg.String
		rec.StartedAt, _ = time.Parse(time.RFC3339, startedStr)
		rec.CompletedAt, _ = time.Parse(time.RFC3339, completedStr)
		records = append(records, rec)
	}

	return records, total, nil
}

// Recent returns the last n transfer records (most recent first).
// Returns an empty non-nil slice if no records exist.
func (r *Repository) Recent(n int) ([]*TransferRecord, error) {
	rows, err := r.db.Query(
		`SELECT id, cid, filename, file_type, direction, total_size, state, error_message, started_at, completed_at, peer_id
		 FROM transfer_history ORDER BY completed_at DESC LIMIT ?`,
		n,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query recent history: %w", err)
	}
	defer rows.Close()

	records := make([]*TransferRecord, 0, n)
	for rows.Next() {
		rec := &TransferRecord{}
		var startedStr, completedStr string
		var errMsg sql.NullString
		if err := rows.Scan(
			&rec.ID, &rec.CID, &rec.Filename, &rec.FileType, &rec.Direction,
			&rec.TotalSize, &rec.State, &errMsg, &startedStr, &completedStr, &rec.PeerID,
		); err != nil {
			return nil, fmt.Errorf("failed to scan recent history row: %w", err)
		}
		rec.ErrorMessage = errMsg.String
		rec.StartedAt, _ = time.Parse(time.RFC3339, startedStr)
		rec.CompletedAt, _ = time.Parse(time.RFC3339, completedStr)
		records = append(records, rec)
	}

	return records, nil
}

// Close closes the database.
func (r *Repository) Close() error {
	if r.db != nil {
		return r.db.Close()
	}
	return nil
}
