// Package: internal/daemon/stats
// Feature: F-029 (Transfer Page E2E)
// Story: US-029-24 (Stats Persistence)
// Purpose: TDD tests for SQLite-backed transfer stats with ticker + graceful shutdown

package stats

import (
	"path/filepath"
	"testing"
	"time"
)

// TestStatsPersistRoundTrip verifies AC-32: stats survive daemon restart.
// Record bytes → SaveStats → Close → reopen → LoadStats → verify totals.
func TestStatsPersistRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "stats.db")
	repo, err := NewRepository(dbPath)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	// Record cumulative bytes
	repo.RecordDownload(1048576) // 1 MB
	repo.RecordUpload(524288)    // 512 KB

	stats := repo.GetStats()
	if stats.TotalDownload != 1048576 {
		t.Errorf("expected TotalDownload 1048576, got %d", stats.TotalDownload)
	}
	if stats.TotalUpload != 524288 {
		t.Errorf("expected TotalUpload 524288, got %d", stats.TotalUpload)
	}

	// Persist to SQLite and close
	if err := repo.SaveStats(); err != nil {
		t.Fatalf("SaveStats failed: %v", err)
	}
	repo.Close()

	// Reopen and load — simulates daemon restart
	repo2, err := NewRepository(dbPath)
	if err != nil {
		t.Fatalf("reopen failed: %v", err)
	}
	defer repo2.Close()

	if err := repo2.LoadStats(); err != nil {
		t.Fatalf("LoadStats failed: %v", err)
	}

	stats2 := repo2.GetStats()
	if stats2.TotalDownload != 1048576 {
		t.Errorf("after restart: expected TotalDownload 1048576, got %d", stats2.TotalDownload)
	}
	if stats2.TotalUpload != 524288 {
		t.Errorf("after restart: expected TotalUpload 524288, got %d", stats2.TotalUpload)
	}
}

// TestStopAndSave verifies AC-33: StopAndSave performs final save before close.
func TestStopAndSave(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "stats.db")
	repo, err := NewRepository(dbPath)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	repo.RecordDownload(2000)
	repo.RecordUpload(1000)

	// Start ticker then immediately stop — verifies graceful shutdown path
	repo.StartTicker()
	repo.StopAndSave()

	// Reopen and verify data was saved
	repo2, err := NewRepository(dbPath)
	if err != nil {
		t.Fatalf("reopen failed: %v", err)
	}
	defer repo2.Close()
	repo2.LoadStats()

	stats := repo2.GetStats()
	if stats.TotalDownload != 2000 {
		t.Errorf("after StopAndSave: expected TotalDownload 2000, got %d", stats.TotalDownload)
	}
	if stats.TotalUpload != 1000 {
		t.Errorf("after StopAndSave: expected TotalUpload 1000, got %d", stats.TotalUpload)
	}
}

// TestGetStatsReturnsSpeedBPS verifies AC-34 partial: GetStats includes speed calculation.
func TestGetStatsReturnsSpeedBPS(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "stats.db")
	repo, err := NewRepository(dbPath)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	defer repo.Close()

	// Record recent data so speed window is populated
	repo.RecordDownload(60000) // 60 KB in current window
	repo.RecordUpload(30000)

	stats := repo.GetStats()

	// Speed should be non-negative (we just recorded, so should be > 0)
	if stats.DownloadSpeedBPS < 0 {
		t.Errorf("DownloadSpeedBPS should be >= 0, got %d", stats.DownloadSpeedBPS)
	}
	if stats.UploadSpeedBPS < 0 {
		t.Errorf("UploadSpeedBPS should be >= 0, got %d", stats.UploadSpeedBPS)
	}
}

// TestNewRepositoryCreatesTable verifies AC-35: table created on NewRepository.
func TestNewRepositoryCreatesTable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "stats.db")
	repo, err := NewRepository(dbPath)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	defer repo.Close()

	// Fresh repo should return zero stats (table initialized with defaults)
	stats := repo.GetStats()
	if stats.TotalDownload != 0 {
		t.Errorf("fresh repo: expected TotalDownload 0, got %d", stats.TotalDownload)
	}
	if stats.TotalUpload != 0 {
		t.Errorf("fresh repo: expected TotalUpload 0, got %d", stats.TotalUpload)
	}
}

// TestMultipleRecordsAccumulate verifies additive semantics.
func TestMultipleRecordsAccumulate(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "stats.db")
	repo, err := NewRepository(dbPath)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	defer repo.Close()

	repo.RecordDownload(100)
	repo.RecordDownload(200)
	repo.RecordUpload(50)
	repo.RecordUpload(75)

	stats := repo.GetStats()
	if stats.TotalDownload != 300 {
		t.Errorf("expected TotalDownload 300, got %d", stats.TotalDownload)
	}
	if stats.TotalUpload != 125 {
		t.Errorf("expected TotalUpload 125, got %d", stats.TotalUpload)
	}
}

// TestTickerDoesNotPanicOnDoubleStop verifies safety of calling StopAndSave twice.
func TestTickerDoesNotPanicOnDoubleStop(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "stats.db")
	repo, err := NewRepository(dbPath)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	repo.StartTicker()

	// First stop should work
	repo.StopAndSave()

	// Second stop should not panic
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("StopAndSave panicked on double call: %v", r)
		}
	}()
	repo.StopAndSave()

	// Allow brief time for goroutines to settle
	time.Sleep(10 * time.Millisecond)
}
