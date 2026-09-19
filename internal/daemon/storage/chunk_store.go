// Package: internal/daemon/storage
// Feature: F-010 (P2P Transfer Protocol)
// Story: US-010-07 (Upload Queue & Seeding)
// Purpose: SQLite chunk storage for P2P seeding

package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

// SharedFile represents metadata for a shared file
type SharedFile struct {
	CID          string
	Filename     string
	TotalSize    int64
	TotalChunks  int
	ManifestData []byte
}

// ChunkStore manages SQLite database for chunk storage
type SQLiteChunkStore struct {
	db     *sql.DB
	closed atomic.Bool
}

// ErrChunkStoreClosed is returned by every method once the store is closed (or when
// called on a nil store): the daemon closes the store during Shutdown while
// background seeding may still be finishing, and a closed store must fail
// cleanly instead of dereferencing a nil handle.
var ErrChunkStoreClosed = errors.New("chunk store is closed")

// guard reports ErrChunkStoreClosed for a nil or closed store. A call that races
// Close after the flag check still cannot panic: database/sql returns
// "database is closed" from a closed *sql.DB.
func (cs *SQLiteChunkStore) guard() error {
	if cs == nil || cs.closed.Load() {
		return ErrChunkStoreClosed
	}
	return nil
}

// NewChunkStore creates a new SQLite chunk store
func NewSQLiteChunkStore(dbPath string) (*SQLiteChunkStore, error) {
	// Open SQLite database (pure Go driver for cross-platform builds)
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Create tables
	err = createTables(db)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create tables: %w", err)
	}

	store := &SQLiteChunkStore{db: db}
	store.ensureMigrations()
	return store, nil
}

// createTables creates shared_files and chunks tables
func createTables(db *sql.DB) error {
	// Create shared_files table
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS shared_files (
			file_cid TEXT PRIMARY KEY,
			filename TEXT NOT NULL,
			total_size INTEGER NOT NULL,
			total_chunks INTEGER NOT NULL,
			manifest_data BLOB
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create shared_files table: %w", err)
	}

	// Create chunks table
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS chunks (
			file_cid TEXT NOT NULL,
			chunk_index INTEGER NOT NULL,
			chunk_cid TEXT NOT NULL,
			chunk_data BLOB NOT NULL,
			PRIMARY KEY (file_cid, chunk_index)
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create chunks table: %w", err)
	}

	// Create index on chunk_cid for fast lookups
	_, err = db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_chunk_cid ON chunks(chunk_cid)
	`)
	if err != nil {
		return fmt.Errorf("failed to create chunk_cid index: %w", err)
	}

	return nil
}

// StoreFile stores file metadata with created_at timestamp.
func (cs *SQLiteChunkStore) StoreFile(fileCID, filename string, totalSize int64, totalChunks int, manifestData []byte) error {
	if err := cs.guard(); err != nil {
		return err
	}
	createdAt := time.Now().UTC().Format(time.RFC3339)
	_, err := cs.db.Exec(`
		INSERT OR REPLACE INTO shared_files (file_cid, filename, total_size, total_chunks, manifest_data, created_at, upload_count)
		VALUES (?, ?, ?, ?, ?, ?, 0)
	`, fileCID, filename, totalSize, totalChunks, manifestData, createdAt)

	if err != nil {
		return fmt.Errorf("failed to store file: %w", err)
	}

	return nil
}

// GetFile retrieves file metadata
func (cs *SQLiteChunkStore) GetFile(fileCID string) (*SharedFile, error) {
	if err := cs.guard(); err != nil {
		return nil, err
	}
	var file SharedFile

	err := cs.db.QueryRow(`
		SELECT file_cid, filename, total_size, total_chunks, manifest_data
		FROM shared_files
		WHERE file_cid = ?
	`, fileCID).Scan(&file.CID, &file.Filename, &file.TotalSize, &file.TotalChunks, &file.ManifestData)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("file not found: %s", fileCID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get file: %w", err)
	}

	return &file, nil
}

// StoreChunk stores chunk data
func (cs *SQLiteChunkStore) StoreChunk(fileCID string, chunkIndex int, chunkCID string, chunkData []byte) error {
	if err := cs.guard(); err != nil {
		return err
	}
	_, err := cs.db.Exec(`
		INSERT OR REPLACE INTO chunks (file_cid, chunk_index, chunk_cid, chunk_data)
		VALUES (?, ?, ?, ?)
	`, fileCID, chunkIndex, chunkCID, chunkData)

	if err != nil {
		return fmt.Errorf("failed to store chunk: %w", err)
	}

	return nil
}

// GetChunk retrieves chunk data by file CID and chunk index
func (cs *SQLiteChunkStore) GetChunk(fileCID string, chunkIndex int) ([]byte, error) {
	if err := cs.guard(); err != nil {
		return nil, err
	}
	var chunkData []byte

	err := cs.db.QueryRow(`
		SELECT chunk_data
		FROM chunks
		WHERE file_cid = ? AND chunk_index = ?
	`, fileCID, chunkIndex).Scan(&chunkData)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("chunk not found: file=%s, index=%d", fileCID, chunkIndex)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get chunk: %w", err)
	}

	return chunkData, nil
}

// GetChunkByCID retrieves chunk data by chunk CID only (for serving to peers)
func (cs *SQLiteChunkStore) GetChunkByCID(chunkCID string) ([]byte, error) {
	if err := cs.guard(); err != nil {
		return nil, err
	}
	var chunkData []byte

	err := cs.db.QueryRow(`
		SELECT chunk_data
		FROM chunks
		WHERE chunk_cid = ?
		LIMIT 1
	`, chunkCID).Scan(&chunkData)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("chunk not found: %s", chunkCID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get chunk by CID: %w", err)
	}

	return chunkData, nil
}

// ListFiles returns all shared files
func (cs *SQLiteChunkStore) ListFiles() ([]*SharedFile, error) {
	if err := cs.guard(); err != nil {
		return nil, err
	}
	rows, err := cs.db.Query(`
		SELECT file_cid, filename, total_size, total_chunks, manifest_data
		FROM shared_files
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to list files: %w", err)
	}
	defer rows.Close()

	var files []*SharedFile

	for rows.Next() {
		var file SharedFile
		err := rows.Scan(&file.CID, &file.Filename, &file.TotalSize, &file.TotalChunks, &file.ManifestData)
		if err != nil {
			return nil, fmt.Errorf("failed to scan file: %w", err)
		}
		files = append(files, &file)
	}

	return files, nil
}

// DeleteFile deletes file and all its chunks
func (cs *SQLiteChunkStore) DeleteFile(fileCID string) error {
	if err := cs.guard(); err != nil {
		return err
	}
	// Start transaction
	tx, err := cs.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Delete chunks
	_, err = tx.Exec(`DELETE FROM chunks WHERE file_cid = ?`, fileCID)
	if err != nil {
		return fmt.Errorf("failed to delete chunks: %w", err)
	}

	// Delete file
	_, err = tx.Exec(`DELETE FROM shared_files WHERE file_cid = ?`, fileCID)
	if err != nil {
		return fmt.Errorf("failed to delete file: %w", err)
	}

	// Commit transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// CountChunks returns number of stored chunks for a file
func (cs *SQLiteChunkStore) CountChunks(fileCID string) (int, error) {
	if err := cs.guard(); err != nil {
		return 0, err
	}
	var count int

	err := cs.db.QueryRow(`
		SELECT COUNT(*) FROM chunks WHERE file_cid = ?
	`, fileCID).Scan(&count)

	if err != nil {
		return 0, fmt.Errorf("failed to count chunks: %w", err)
	}

	return count, nil
}

// HasChunk checks if a specific chunk exists
func (cs *SQLiteChunkStore) HasChunk(fileCID string, chunkIndex int) bool {
	if err := cs.guard(); err != nil {
		return false
	}
	var exists int

	err := cs.db.QueryRow(`
		SELECT 1 FROM chunks WHERE file_cid = ? AND chunk_index = ? LIMIT 1
	`, fileCID, chunkIndex).Scan(&exists)

	return err == nil && exists == 1
}

// ensureMigrations runs idempotent schema migrations on startup.
// SQLite doesn't have ALTER TABLE ADD COLUMN IF NOT EXISTS,
// so we attempt each ALTER and ignore "duplicate column" errors.
func (cs *SQLiteChunkStore) ensureMigrations() {
	migrations := []string{
		`ALTER TABLE shared_files ADD COLUMN created_at TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE shared_files ADD COLUMN upload_count INTEGER NOT NULL DEFAULT 0`,
	}
	for _, m := range migrations {
		_, err := cs.db.Exec(m)
		if err != nil && !strings.Contains(err.Error(), "duplicate column") {
			// Unexpected error — log but don't crash startup
			_ = err
		}
	}
}

// DB returns the underlying *sql.DB for direct queries (e.g., PRAGMA in tests).
func (cs *SQLiteChunkStore) DB() *sql.DB {
	if err := cs.guard(); err != nil {
		return nil
	}
	return cs.db
}

// LibraryFile represents a shared file with upload statistics for the library endpoint.
type LibraryFile struct {
	CID         string `json:"cid"`
	Filename    string `json:"filename"`
	TotalSize   int64  `json:"total_size"`
	TotalChunks int    `json:"total_chunks"`
	UploadCount int    `json:"upload_count"`
	CreatedAt   string `json:"created_at"`
}

// ListFilesPaginated returns paginated shared files ordered by created_at DESC.
func (cs *SQLiteChunkStore) ListFilesPaginated(limit, offset int) ([]*LibraryFile, int, error) {
	if err := cs.guard(); err != nil {
		return nil, 0, err
	}
	var total int
	if err := cs.db.QueryRow("SELECT COUNT(*) FROM shared_files").Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("failed to count files: %w", err)
	}

	rows, err := cs.db.Query(
		`SELECT file_cid, filename, total_size, total_chunks, upload_count, created_at
		 FROM shared_files ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		limit, offset,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to query files: %w", err)
	}
	defer rows.Close()

	var files []*LibraryFile
	for rows.Next() {
		f := &LibraryFile{}
		if err := rows.Scan(&f.CID, &f.Filename, &f.TotalSize, &f.TotalChunks, &f.UploadCount, &f.CreatedAt); err != nil {
			return nil, 0, fmt.Errorf("failed to scan file row: %w", err)
		}
		files = append(files, f)
	}

	return files, total, nil
}

// GetStorageSummary returns aggregate storage stats across ALL shared files.
func (cs *SQLiteChunkStore) GetStorageSummary() (int64, int, error) {
	if err := cs.guard(); err != nil {
		return 0, 0, err
	}
	var usedBytes sql.NullInt64
	var fileCount int
	err := cs.db.QueryRow(
		`SELECT COALESCE(SUM(total_size), 0), COUNT(*) FROM shared_files`,
	).Scan(&usedBytes, &fileCount)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get storage summary: %w", err)
	}
	return usedBytes.Int64, fileCount, nil
}

// IncrementUploadCount increments the upload counter for a CID (called when chunks are served).
func (cs *SQLiteChunkStore) IncrementUploadCount(cid string) error {
	if err := cs.guard(); err != nil {
		return err
	}
	_, err := cs.db.Exec(
		`UPDATE shared_files SET upload_count = upload_count + 1 WHERE file_cid = ?`, cid,
	)
	return err
}

// Close closes the database connection
func (cs *SQLiteChunkStore) Close() error {
	if cs == nil {
		return nil
	}
	cs.closed.Store(true)
	if cs.db != nil {
		return cs.db.Close()
	}
	return nil
}
