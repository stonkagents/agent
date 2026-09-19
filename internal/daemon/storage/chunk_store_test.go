// Package: internal/daemon/storage
// Feature: F-010 (P2P Transfer Protocol)
// Story: US-010-07 (Upload Queue & Seeding)
// Purpose: TDD tests for SQLite chunk storage

package storage

import (
	"bytes"
	"fmt"
	"testing"
)

// TestChunkStore_StoreFile - RED test
// Acceptance Criterion: Store file metadata in shared_files table
func TestChunkStore_StoreFile(t *testing.T) {
	// Arrange
	tmpDB := t.TempDir() + "/chunks.db"
	store, err := NewSQLiteChunkStore(tmpDB)
	if err != nil {
		t.Fatalf("Failed to create chunk store: %v", err)
	}
	defer store.Close()

	// Act - store file metadata
	fileCID := "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi"
	err = store.StoreFile(fileCID, "test.bin", 1048576, 4, []byte("manifest-data"))

	// Assert
	if err != nil {
		t.Fatalf("StoreFile() failed: %v", err)
	}

	// Verify file was stored
	file, err := store.GetFile(fileCID)
	if err != nil {
		t.Fatalf("GetFile() failed: %v", err)
	}

	if file.CID != fileCID {
		t.Errorf("Expected CID %s, got %s", fileCID, file.CID)
	}
	if file.Filename != "test.bin" {
		t.Errorf("Expected filename test.bin, got %s", file.Filename)
	}
	if file.TotalSize != 1048576 {
		t.Errorf("Expected size 1048576, got %d", file.TotalSize)
	}
}

// TestChunkStore_StoreChunk - RED test
// Acceptance Criterion: Store chunk data in chunks table
func TestChunkStore_StoreChunk(t *testing.T) {
	// Arrange
	tmpDB := t.TempDir() + "/chunks.db"
	store, err := NewSQLiteChunkStore(tmpDB)
	if err != nil {
		t.Fatalf("Failed to create chunk store: %v", err)
	}
	defer store.Close()

	fileCID := "bafybeigdyrzt5sfpfile"
	store.StoreFile(fileCID, "test.bin", 262144, 1, nil)

	// Act - store chunk
	chunkCID := "bafybeigdyrzt5sfpchunk"
	chunkData := []byte("test chunk data")
	err = store.StoreChunk(fileCID, 0, chunkCID, chunkData)

	// Assert
	if err != nil {
		t.Fatalf("StoreChunk() failed: %v", err)
	}

	// Verify chunk was stored
	retrievedData, err := store.GetChunk(fileCID, 0)
	if err != nil {
		t.Fatalf("GetChunk() failed: %v", err)
	}

	if !bytes.Equal(chunkData, retrievedData) {
		t.Error("Retrieved chunk data does not match stored data")
	}
}

// TestChunkStore_GetChunkByCID - RED test
// Acceptance Criterion: Retrieve chunk by CID (for serving to peers)
func TestChunkStore_GetChunkByCID(t *testing.T) {
	// Arrange
	tmpDB := t.TempDir() + "/chunks.db"
	store, err := NewSQLiteChunkStore(tmpDB)
	if err != nil {
		t.Fatalf("Failed to create chunk store: %v", err)
	}
	defer store.Close()

	fileCID := "bafybeigdyrzt5sfpfile"
	chunkCID := "bafybeigdyrzt5sfpchunk"
	chunkData := []byte("chunk data for peer")

	store.StoreFile(fileCID, "test.bin", 262144, 1, nil)
	store.StoreChunk(fileCID, 0, chunkCID, chunkData)

	// Act - retrieve by chunk CID only
	retrievedData, err := store.GetChunkByCID(chunkCID)

	// Assert
	if err != nil {
		t.Fatalf("GetChunkByCID() failed: %v", err)
	}

	if !bytes.Equal(chunkData, retrievedData) {
		t.Error("Retrieved chunk data does not match")
	}
}

// TestChunkStore_ListFiles - RED test
// Acceptance Criterion: List all shared files
func TestChunkStore_ListFiles(t *testing.T) {
	// Arrange
	tmpDB := t.TempDir() + "/chunks.db"
	store, err := NewSQLiteChunkStore(tmpDB)
	if err != nil {
		t.Fatalf("Failed to create chunk store: %v", err)
	}
	defer store.Close()

	// Store 3 files
	store.StoreFile("bafybeigdyrzt5sfp1", "file1.bin", 1048576, 4, nil)
	store.StoreFile("bafybeigdyrzt5sfp2", "file2.bin", 2097152, 8, nil)
	store.StoreFile("bafybeigdyrzt5sfp3", "file3.bin", 524288, 2, nil)

	// Act - list all files
	files, err := store.ListFiles()

	// Assert
	if err != nil {
		t.Fatalf("ListFiles() failed: %v", err)
	}

	if len(files) != 3 {
		t.Errorf("Expected 3 files, got %d", len(files))
	}
}

// TestChunkStore_DeleteFile - RED test
// Acceptance Criterion: Delete file and all its chunks
func TestChunkStore_DeleteFile(t *testing.T) {
	// Arrange
	tmpDB := t.TempDir() + "/chunks.db"
	store, err := NewSQLiteChunkStore(tmpDB)
	if err != nil {
		t.Fatalf("Failed to create chunk store: %v", err)
	}
	defer store.Close()

	fileCID := "bafybeigdyrzt5sfpfile"
	store.StoreFile(fileCID, "test.bin", 524288, 2, nil)
	store.StoreChunk(fileCID, 0, "chunk0", []byte("data0"))
	store.StoreChunk(fileCID, 1, "chunk1", []byte("data1"))

	// Act - delete file
	err = store.DeleteFile(fileCID)

	// Assert
	if err != nil {
		t.Fatalf("DeleteFile() failed: %v", err)
	}

	// Verify file is gone
	_, err = store.GetFile(fileCID)
	if err == nil {
		t.Error("File should be deleted")
	}

	// Verify chunks are gone
	_, err = store.GetChunk(fileCID, 0)
	if err == nil {
		t.Error("Chunks should be deleted with file")
	}
}

// TestChunkStore_CountChunks - RED test
// Acceptance Criterion: Count stored chunks for a file
func TestChunkStore_CountChunks(t *testing.T) {
	// Arrange
	tmpDB := t.TempDir() + "/chunks.db"
	store, err := NewSQLiteChunkStore(tmpDB)
	if err != nil {
		t.Fatalf("Failed to create chunk store: %v", err)
	}
	defer store.Close()

	fileCID := "bafybeigdyrzt5sfpfile"
	store.StoreFile(fileCID, "test.bin", 1048576, 4, nil)

	// Store 2 out of 4 chunks
	store.StoreChunk(fileCID, 0, "chunk0", []byte("data0"))
	store.StoreChunk(fileCID, 2, "chunk2", []byte("data2"))

	// Act - count chunks
	count, err := store.CountChunks(fileCID)

	// Assert
	if err != nil {
		t.Fatalf("CountChunks() failed: %v", err)
	}

	if count != 2 {
		t.Errorf("Expected 2 chunks, got %d", count)
	}
}

// TestChunkStore_HasChunk - RED test
// Acceptance Criterion: Check if specific chunk exists
func TestChunkStore_HasChunk(t *testing.T) {
	// Arrange
	tmpDB := t.TempDir() + "/chunks.db"
	store, err := NewSQLiteChunkStore(tmpDB)
	if err != nil {
		t.Fatalf("Failed to create chunk store: %v", err)
	}
	defer store.Close()

	fileCID := "bafybeigdyrzt5sfpfile"
	store.StoreFile(fileCID, "test.bin", 524288, 2, nil)
	store.StoreChunk(fileCID, 0, "chunk0", []byte("data0"))

	// Act - check existing chunk
	has := store.HasChunk(fileCID, 0)
	if !has {
		t.Error("Should have chunk 0")
	}

	// Act - check missing chunk
	has = store.HasChunk(fileCID, 1)
	if has {
		t.Error("Should not have chunk 1")
	}
}

// --- F-029 Task 9 tests: schema migration + library queries ---

// TestSharedFilesMigrationAddsColumns verifies idempotent migration adds created_at and upload_count.
func TestSharedFilesMigrationAddsColumns(t *testing.T) {
	dbPath := t.TempDir() + "/chunks.db"
	store, err := NewSQLiteChunkStore(dbPath)
	if err != nil {
		t.Fatalf("create store failed: %v", err)
	}
	defer store.Close()

	// Verify columns exist by querying PRAGMA
	rows, err := store.DB().Query("PRAGMA table_info(shared_files)")
	if err != nil {
		t.Fatalf("pragma failed: %v", err)
	}
	defer rows.Close()

	columns := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, typeName string
		var notnull, pk int
		var dflt interface{}
		rows.Scan(&cid, &name, &typeName, &notnull, &dflt, &pk)
		columns[name] = true
	}

	if !columns["created_at"] {
		t.Error("expected created_at column in shared_files")
	}
	if !columns["upload_count"] {
		t.Error("expected upload_count column in shared_files")
	}
}

// TestListFilesPaginated returns paginated results.
func TestListFilesPaginated(t *testing.T) {
	dbPath := t.TempDir() + "/chunks.db"
	store, err := NewSQLiteChunkStore(dbPath)
	if err != nil {
		t.Fatalf("create store failed: %v", err)
	}
	defer store.Close()

	// Insert 5 files
	for i := 0; i < 5; i++ {
		cid := fmt.Sprintf("QmTestFile%05dabcdefghijklmnopqrstuvwxyz123", i)
		err := store.StoreFile(cid, fmt.Sprintf("file%d.txt", i), int64(100*(i+1)), i+1, nil)
		if err != nil {
			t.Fatalf("StoreFile %d failed: %v", i, err)
		}
	}

	files, total, err := store.ListFilesPaginated(2, 0)
	if err != nil {
		t.Fatalf("ListFilesPaginated failed: %v", err)
	}
	if total != 5 {
		t.Errorf("expected total 5, got %d", total)
	}
	if len(files) != 2 {
		t.Errorf("expected 2 files on page, got %d", len(files))
	}

	// Page 2
	files2, total2, err := store.ListFilesPaginated(2, 2)
	if err != nil {
		t.Fatalf("ListFilesPaginated page 2 failed: %v", err)
	}
	if total2 != 5 {
		t.Errorf("expected total 5, got %d", total2)
	}
	if len(files2) != 2 {
		t.Errorf("expected 2 files on page 2, got %d", len(files2))
	}
}

// TestGetStorageSummary returns aggregate stats.
func TestGetStorageSummary(t *testing.T) {
	dbPath := t.TempDir() + "/chunks.db"
	store, err := NewSQLiteChunkStore(dbPath)
	if err != nil {
		t.Fatalf("create store failed: %v", err)
	}
	defer store.Close()

	_ = store.StoreFile("QmFileSummaryAabcdefghijklmnopqrstuvwxyz1234", "a.txt", 1000, 4, nil)
	_ = store.StoreFile("QmFileSummaryBabcdefghijklmnopqrstuvwxyz1234", "b.txt", 2000, 8, nil)

	usedBytes, fileCount, err := store.GetStorageSummary()
	if err != nil {
		t.Fatalf("GetStorageSummary failed: %v", err)
	}
	if usedBytes != 3000 {
		t.Errorf("expected 3000 bytes, got %d", usedBytes)
	}
	if fileCount != 2 {
		t.Errorf("expected 2 files, got %d", fileCount)
	}
}

// TestGetStorageSummary_Empty returns zeros when no files.
func TestGetStorageSummary_Empty(t *testing.T) {
	dbPath := t.TempDir() + "/chunks.db"
	store, err := NewSQLiteChunkStore(dbPath)
	if err != nil {
		t.Fatalf("create store failed: %v", err)
	}
	defer store.Close()

	usedBytes, fileCount, err := store.GetStorageSummary()
	if err != nil {
		t.Fatalf("GetStorageSummary failed: %v", err)
	}
	if usedBytes != 0 || fileCount != 0 {
		t.Errorf("expected 0/0 for empty store, got %d/%d", usedBytes, fileCount)
	}
}

// TestIncrementUploadCount increments counter.
func TestIncrementUploadCount(t *testing.T) {
	dbPath := t.TempDir() + "/chunks.db"
	store, err := NewSQLiteChunkStore(dbPath)
	if err != nil {
		t.Fatalf("create store failed: %v", err)
	}
	defer store.Close()

	cid := "QmUploadCountabcdefghijklmnopqrstuvwxyz12345678"
	_ = store.StoreFile(cid, "count.txt", 500, 2, nil)

	_ = store.IncrementUploadCount(cid)
	_ = store.IncrementUploadCount(cid)
	_ = store.IncrementUploadCount(cid)

	files, _, _ := store.ListFilesPaginated(10, 0)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].UploadCount != 3 {
		t.Errorf("expected upload_count=3, got %d", files[0].UploadCount)
	}
}
