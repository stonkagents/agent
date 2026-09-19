// Package: internal/daemon/history
// Feature: F-029 (Transfer Page E2E)
// Story: US-029-05 (Transfer History)
// Purpose: TDD tests for SQLite-backed transfer history with retention cap

package history

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// TestInsertAndList verifies AC-9/AC-10: insert a record and retrieve via List.
func TestInsertAndList(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "history.db")
	repo, err := NewRepository(dbPath)
	if err != nil {
		t.Fatalf("failed to create repo: %v", err)
	}
	defer repo.Close()

	fixedTime := time.Date(2026, 2, 14, 10, 0, 0, 0, time.UTC)

	entry := &TransferRecord{
		CID:         "QmTest1234567890abcdefghijklmnopqrstuvwxyz12345",
		Filename:    "test.claw-skill",
		FileType:    ".claw-skill",
		Direction:   "download",
		TotalSize:   1048576,
		State:       "completed",
		StartedAt:   fixedTime.Add(-5 * time.Minute),
		CompletedAt: fixedTime,
	}

	err = repo.Insert(entry)
	if err != nil {
		t.Fatalf("insert failed: %v", err)
	}

	records, total, err := repo.List(20, 0)
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if total != 1 {
		t.Errorf("expected total 1, got %d", total)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].CID != "QmTest1234567890abcdefghijklmnopqrstuvwxyz12345" {
		t.Errorf("wrong CID: %s", records[0].CID)
	}
	if records[0].ID == 0 {
		t.Error("expected auto-generated ID > 0")
	}
}

// TestDuplicateRejected verifies AC-12: same (cid, direction, completed_at) is rejected.
func TestDuplicateRejected(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "history.db")
	repo, err := NewRepository(dbPath)
	if err != nil {
		t.Fatalf("failed to create repo: %v", err)
	}
	defer repo.Close()

	fixedTime := time.Date(2026, 2, 14, 11, 0, 0, 0, time.UTC)
	entry := &TransferRecord{
		CID: "QmDup1234567890abcdefghijklmnopqrstuvwxyz1234", Filename: "dup.txt", FileType: "file",
		Direction: "download", TotalSize: 100, State: "completed",
		StartedAt: fixedTime.Add(-1 * time.Minute), CompletedAt: fixedTime,
	}

	_ = repo.Insert(entry)
	err = repo.Insert(entry) // Same cid + direction + completed_at
	if err == nil {
		t.Error("expected duplicate insert to fail")
	}
}

// TestRetentionCapPrunesOldRecords verifies AC-12b: only 10,000 records retained.
func TestRetentionCapPrunesOldRecords(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping 10k-insert retention test in short mode (>30s with race detector)")
	}
	dbPath := filepath.Join(t.TempDir(), "history.db")
	repo, err := NewRepository(dbPath)
	if err != nil {
		t.Fatalf("failed to create repo: %v", err)
	}
	defer repo.Close()

	// Insert 10,002 records
	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10002; i++ {
		entry := &TransferRecord{
			CID: fmt.Sprintf("QmTest%06dabcdefghijklmnopqrstuvwxyz1234567890", i), Filename: "test.txt", FileType: "file",
			Direction: "download", TotalSize: 100, State: "completed",
			StartedAt:   baseTime.Add(time.Duration(i) * time.Minute),
			CompletedAt: baseTime.Add(time.Duration(i)*time.Minute + 30*time.Second),
		}
		if err := repo.Insert(entry); err != nil {
			t.Fatalf("insert %d failed: %v", i, err)
		}
	}

	// Verify only 10,000 remain
	_, total, err := repo.List(1, 0)
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if total != 10000 {
		t.Errorf("expected 10000 records after retention cap, got %d", total)
	}
}

// TestRecentReturnsLastNRecords verifies Recent(n) returns most recent terminal records.
func TestRecentReturnsLastNRecords(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "history.db")
	repo, err := NewRepository(dbPath)
	if err != nil {
		t.Fatalf("failed to create repo: %v", err)
	}
	defer repo.Close()

	baseTime := time.Date(2026, 2, 14, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 7; i++ {
		state := "completed"
		if i%3 == 0 {
			state = "failed"
		}
		_ = repo.Insert(&TransferRecord{
			CID:      fmt.Sprintf("QmRecent%04dabcdefghijklmnopqrstuvwxyz123456", i),
			Filename: fmt.Sprintf("file%d.txt", i), FileType: "file",
			Direction: "download", TotalSize: 100, State: state,
			StartedAt:   baseTime.Add(time.Duration(i) * time.Minute),
			CompletedAt: baseTime.Add(time.Duration(i)*time.Minute + 30*time.Second),
		})
	}

	recent, err := repo.Recent(5)
	if err != nil {
		t.Fatalf("Recent failed: %v", err)
	}
	if len(recent) != 5 {
		t.Errorf("expected 5 recent records, got %d", len(recent))
	}
	// Most recent first
	if len(recent) >= 2 && recent[0].CompletedAt.Before(recent[1].CompletedAt) {
		t.Error("expected recent records in descending order by completed_at")
	}
}

// TestRecentReturnsEmptyWhenNoRecords verifies Recent returns empty slice, not nil.
func TestRecentReturnsEmptyWhenNoRecords(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "history.db")
	repo, err := NewRepository(dbPath)
	if err != nil {
		t.Fatalf("failed to create repo: %v", err)
	}
	defer repo.Close()

	recent, err := repo.Recent(5)
	if err != nil {
		t.Fatalf("Recent failed: %v", err)
	}
	if recent == nil {
		t.Error("expected non-nil empty slice, got nil")
	}
	if len(recent) != 0 {
		t.Errorf("expected 0 records, got %d", len(recent))
	}
}

// TestPersistsAcrossReopen verifies AC-11: history survives daemon restart.
func TestPersistsAcrossReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "history.db")
	repo, err := NewRepository(dbPath)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	fixedTime := time.Date(2026, 2, 14, 12, 0, 0, 0, time.UTC)
	_ = repo.Insert(&TransferRecord{
		CID: "QmPersist1234567890abcdefghijklmnopqrstuvwxyz1", Filename: "p.txt", FileType: "file",
		Direction: "download", TotalSize: 100, State: "completed",
		StartedAt: fixedTime, CompletedAt: fixedTime,
	})
	repo.Close()

	// Reopen
	repo2, err := NewRepository(dbPath)
	if err != nil {
		t.Fatalf("reopen failed: %v", err)
	}
	defer repo2.Close()

	records, total, _ := repo2.List(50, 0)
	if total != 1 || len(records) != 1 {
		t.Errorf("expected 1 record after reopen, got %d", total)
	}
}
