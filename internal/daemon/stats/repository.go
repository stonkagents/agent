// Package: internal/daemon/stats
// Feature: F-029 (Transfer Page E2E)
// Story: US-029-24 (Stats Persistence)
// Purpose: SQLite-backed transfer stats with periodic save and graceful shutdown

package stats

import (
	"database/sql"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// TransferStatsSnapshot holds cumulative stats plus instantaneous speed.
type TransferStatsSnapshot struct {
	TotalUpload      int64 `json:"total_upload"`
	TotalDownload    int64 `json:"total_download"`
	UploadSpeedBPS   int64 `json:"upload_speed_bps"`
	DownloadSpeedBPS int64 `json:"download_speed_bps"`
}

type speedSample struct {
	bytes     int64
	timestamp time.Time
}

// Repository provides SQLite-backed cumulative transfer stats
// with a 60-second sliding window for speed calculation.
type Repository struct {
	mu             sync.RWMutex
	totalUpload    int64
	totalDownload  int64
	uploadWindow   []speedSample
	downloadWindow []speedSample
	db             *sql.DB
	stopTicker     chan struct{}
	stopped        bool
}

// NewRepository opens (or creates) the SQLite database and initialises the schema.
func NewRepository(dbPath string) (*Repository, error) {
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("stats: open db: %w", err)
	}

	// Create single-row table (AC-35)
	schema := `
		CREATE TABLE IF NOT EXISTS transfer_stats (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			total_upload INTEGER NOT NULL DEFAULT 0,
			total_download INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL
		);
		INSERT OR IGNORE INTO transfer_stats (id, total_upload, total_download, updated_at)
			VALUES (1, 0, 0, datetime('now'));
	`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("stats: init schema: %w", err)
	}

	return &Repository{
		db:             db,
		uploadWindow:   make([]speedSample, 0),
		downloadWindow: make([]speedSample, 0),
	}, nil
}

// RecordDownload adds downloaded bytes to in-memory totals and speed window.
func (r *Repository) RecordDownload(bytes int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.totalDownload += bytes
	now := time.Now()
	r.downloadWindow = addAndPrune(r.downloadWindow, bytes, now)
}

// RecordUpload adds uploaded bytes to in-memory totals and speed window.
func (r *Repository) RecordUpload(bytes int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.totalUpload += bytes
	now := time.Now()
	r.uploadWindow = addAndPrune(r.uploadWindow, bytes, now)
}

// GetStats returns a snapshot of cumulative bytes and current speed (AC-34).
func (r *Repository) GetStats() TransferStatsSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return TransferStatsSnapshot{
		TotalUpload:      r.totalUpload,
		TotalDownload:    r.totalDownload,
		UploadSpeedBPS:   calcWindowSpeed(r.uploadWindow),
		DownloadSpeedBPS: calcWindowSpeed(r.downloadWindow),
	}
}

// SaveStats persists in-memory totals to SQLite.
func (r *Repository) SaveStats() error {
	r.mu.RLock()
	up := r.totalUpload
	down := r.totalDownload
	r.mu.RUnlock()

	if r.db == nil {
		return nil
	}

	_, err := r.db.Exec(
		`UPDATE transfer_stats SET total_upload = ?, total_download = ?, updated_at = datetime('now') WHERE id = 1`,
		up, down,
	)
	return err
}

// LoadStats reads persisted totals from SQLite into memory (called on daemon init, AC-32).
func (r *Repository) LoadStats() error {
	if r.db == nil {
		return nil
	}

	var up, down int64
	err := r.db.QueryRow(`SELECT total_upload, total_download FROM transfer_stats WHERE id = 1`).Scan(&up, &down)
	if err != nil {
		return fmt.Errorf("stats: load: %w", err)
	}

	r.mu.Lock()
	r.totalUpload = up
	r.totalDownload = down
	r.mu.Unlock()
	return nil
}

// StartTicker starts the 30-second periodic save goroutine (AC-33).
func (r *Repository) StartTicker() {
	r.mu.Lock()
	r.stopTicker = make(chan struct{})
	r.stopped = false
	r.mu.Unlock()

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-r.stopTicker:
				return
			case <-ticker.C:
				_ = r.SaveStats()
			}
		}
	}()
}

// StopAndSave stops the ticker, performs a final save, and closes the DB (AC-33 shutdown).
// Safe to call multiple times.
func (r *Repository) StopAndSave() {
	r.mu.Lock()
	if r.stopTicker != nil && !r.stopped {
		close(r.stopTicker)
		r.stopped = true
	}
	r.mu.Unlock()

	_ = r.SaveStats()

	if r.db != nil {
		r.db.Close()
		r.db = nil
	}
}

// Close closes the database connection without saving.
func (r *Repository) Close() {
	if r.db != nil {
		r.db.Close()
		r.db = nil
	}
}

// calcWindowSpeed returns average bytes/sec over the last 60 seconds.
func calcWindowSpeed(window []speedSample) int64 {
	now := time.Now()
	windowStart := now.Add(-60 * time.Second)
	totalBytes := int64(0)

	for _, s := range window {
		if s.timestamp.After(windowStart) {
			totalBytes += s.bytes
		}
	}

	if totalBytes > 0 {
		windowDuration := now.Sub(windowStart).Seconds()
		if windowDuration > 0 {
			return int64(float64(totalBytes) / windowDuration)
		}
	}
	return 0
}

// addAndPrune appends a sample and drops entries older than 60 seconds.
func addAndPrune(window []speedSample, bytes int64, ts time.Time) []speedSample {
	window = append(window, speedSample{bytes: bytes, timestamp: ts})

	cutoff := ts.Add(-60 * time.Second)
	validStart := 0
	for i, s := range window {
		if s.timestamp.After(cutoff) {
			validStart = i
			break
		}
	}
	if validStart > 0 {
		window = window[validStart:]
	}
	return window
}
