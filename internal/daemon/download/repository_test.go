// Package: internal/daemon/download
// Feature: F-010 (P2P Transfer Protocol)
// Story: US-010-06 (Chunked Download Manager)
// Purpose: TDD tests for download repository (metadata persistence)

package download

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestRepository_SaveMetadata - RED test
// Acceptance Criterion: Metadata persistence with chunk completion bitmap
func TestRepository_SaveMetadata(t *testing.T) {
	// Arrange - create temp directory
	tmpDir := t.TempDir()
	repo := NewRepository(tmpDir)

	metadata := &DownloadMetadata{
		CID:         "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi",
		Filename:    "test-file.bin",
		TotalSize:   1048576, // 1 MB
		TotalChunks: 4,
		ChunkSize:   262144, // 256 KB
		State:       StateActive,
		ChunksMap:   make([]bool, 4), // All chunks incomplete initially
	}

	// Act - save metadata
	err := repo.SaveMetadata(metadata)

	// Assert
	if err != nil {
		t.Fatalf("SaveMetadata() failed: %v", err)
	}

	// Verify metadata.json was created
	metadataPath := filepath.Join(tmpDir, metadata.CID, "metadata.json")
	if _, err := os.Stat(metadataPath); os.IsNotExist(err) {
		t.Errorf("metadata.json not created at %s", metadataPath)
	}
}

// TestRepository_LoadMetadata - RED test
// Acceptance Criterion: Resume downloads by loading metadata.json on startup
func TestRepository_LoadMetadata(t *testing.T) {
	// Arrange - create and save metadata first
	tmpDir := t.TempDir()
	repo := NewRepository(tmpDir)

	original := &DownloadMetadata{
		CID:         "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi",
		Filename:    "resume-test.bin",
		TotalSize:   524288, // 512 KB
		TotalChunks: 2,
		ChunkSize:   262144,
		State:       StatePaused,
		ChunksMap:   []bool{true, false}, // First chunk completed, second incomplete
	}
	repo.SaveMetadata(original)

	// Act - load metadata
	loaded, err := repo.LoadMetadata(original.CID)

	// Assert
	if err != nil {
		t.Fatalf("LoadMetadata() failed: %v", err)
	}

	if loaded.CID != original.CID {
		t.Errorf("CID mismatch: got %q, want %q", loaded.CID, original.CID)
	}
	if loaded.Filename != original.Filename {
		t.Errorf("Filename mismatch: got %q, want %q", loaded.Filename, original.Filename)
	}
	if loaded.State != original.State {
		t.Errorf("State mismatch: got %q, want %q", loaded.State, original.State)
	}
	if len(loaded.ChunksMap) != len(original.ChunksMap) {
		t.Errorf("ChunksMap length mismatch: got %d, want %d", len(loaded.ChunksMap), len(original.ChunksMap))
	}
	if !loaded.ChunksMap[0] || loaded.ChunksMap[1] {
		t.Errorf("ChunksMap incorrect: got %v, want [true, false]", loaded.ChunksMap)
	}
}

// TestRepository_UpdateChunkCompletion - RED test
// Acceptance Criterion: Track chunk completion for resume capability
func TestRepository_UpdateChunkCompletion(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	repo := NewRepository(tmpDir)

	metadata := &DownloadMetadata{
		CID:         "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi",
		Filename:    "chunk-test.bin",
		TotalSize:   1048576,
		TotalChunks: 4,
		ChunkSize:   262144,
		State:       StateActive,
		ChunksMap:   make([]bool, 4),
	}
	repo.SaveMetadata(metadata)

	// Act - mark chunk 0 and chunk 2 as completed
	err := repo.UpdateChunkCompletion(metadata.CID, 0, true)
	if err != nil {
		t.Fatalf("UpdateChunkCompletion(0) failed: %v", err)
	}
	err = repo.UpdateChunkCompletion(metadata.CID, 2, true)
	if err != nil {
		t.Fatalf("UpdateChunkCompletion(2) failed: %v", err)
	}

	// Assert - reload and verify
	loaded, err := repo.LoadMetadata(metadata.CID)
	if err != nil {
		t.Fatalf("LoadMetadata() failed: %v", err)
	}

	expected := []bool{true, false, true, false}
	for i, completed := range expected {
		if loaded.ChunksMap[i] != completed {
			t.Errorf("Chunk %d: got completed=%v, want %v", i, loaded.ChunksMap[i], completed)
		}
	}
}

// TestRepository_ListIncomplete - RED test
// Acceptance Criterion: Resume incomplete downloads on daemon startup
func TestRepository_ListIncomplete(t *testing.T) {
	// Arrange - create multiple downloads with different states
	tmpDir := t.TempDir()
	repo := NewRepository(tmpDir)

	completed := &DownloadMetadata{
		CID:         "bafybeigdyrzt5sfpcompleted",
		Filename:    "completed.bin",
		TotalSize:   262144,
		TotalChunks: 1,
		ChunkSize:   262144,
		State:       StateCompleted,
		ChunksMap:   []bool{true},
	}
	active := &DownloadMetadata{
		CID:         "bafybeigdyrzt5sfpactive",
		Filename:    "active.bin",
		TotalSize:   524288,
		TotalChunks: 2,
		ChunkSize:   262144,
		State:       StateActive,
		ChunksMap:   []bool{true, false},
	}
	paused := &DownloadMetadata{
		CID:         "bafybeigdyrzt5sfppaused",
		Filename:    "paused.bin",
		TotalSize:   786432,
		TotalChunks: 3,
		ChunkSize:   262144,
		State:       StatePaused,
		ChunksMap:   []bool{true, true, false},
	}

	repo.SaveMetadata(completed)
	repo.SaveMetadata(active)
	repo.SaveMetadata(paused)

	// Act - list incomplete downloads
	incomplete, err := repo.ListIncomplete()

	// Assert
	if err != nil {
		t.Fatalf("ListIncomplete() failed: %v", err)
	}

	// Should return active and paused, but NOT completed
	if len(incomplete) != 2 {
		t.Fatalf("Expected 2 incomplete downloads, got %d", len(incomplete))
	}

	cids := make(map[string]bool)
	for _, meta := range incomplete {
		cids[meta.CID] = true
	}

	if !cids[active.CID] {
		t.Error("Active download not in incomplete list")
	}
	if !cids[paused.CID] {
		t.Error("Paused download not in incomplete list")
	}
	if cids[completed.CID] {
		t.Error("Completed download should not be in incomplete list")
	}
}

// TestRepository_GetProgress - RED test
// Acceptance Criterion: Calculate progress percentage from ChunksMap
func TestRepository_GetProgress(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	repo := NewRepository(tmpDir)

	metadata := &DownloadMetadata{
		CID:         "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi",
		Filename:    "progress-test.bin",
		TotalSize:   1048576,
		TotalChunks: 4,
		ChunkSize:   262144,
		State:       StateActive,
		ChunksMap:   []bool{true, true, false, false}, // 50% complete
	}
	repo.SaveMetadata(metadata)

	// Act
	progress, err := repo.GetProgress(metadata.CID)

	// Assert
	if err != nil {
		t.Fatalf("GetProgress() failed: %v", err)
	}

	expectedProgress := 0.5 // 2 out of 4 chunks = 50%
	if progress != expectedProgress {
		t.Errorf("Progress mismatch: got %.2f, want %.2f", progress, expectedProgress)
	}
}

// TestRepository_Delete - RED test
// Acceptance Criterion: Clean up metadata after successful download
func TestRepository_Delete(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	repo := NewRepository(tmpDir)

	metadata := &DownloadMetadata{
		CID:         "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi",
		Filename:    "delete-test.bin",
		TotalSize:   262144,
		TotalChunks: 1,
		ChunkSize:   262144,
		State:       StateCompleted,
		ChunksMap:   []bool{true},
	}
	repo.SaveMetadata(metadata)

	// Verify it exists
	_, err := repo.LoadMetadata(metadata.CID)
	if err != nil {
		t.Fatalf("Metadata should exist before deletion: %v", err)
	}

	// Act - delete metadata
	err = repo.Delete(metadata.CID)

	// Assert
	if err != nil {
		t.Fatalf("Delete() failed: %v", err)
	}

	// Verify it no longer exists
	_, err = repo.LoadMetadata(metadata.CID)
	if err == nil {
		t.Error("Metadata should not exist after deletion")
	}
}

// === F-029 Parallelization: Thread-Safe Repository (US-029-P2) ===

// TestRepository_CleanupDownloadLock - RED test
// Acceptance Criterion: Lock cleanup prevents memory leak for completed/cancelled downloads
func TestRepository_CleanupDownloadLock(t *testing.T) {
	tmpDir := t.TempDir()
	repo := NewRepository(tmpDir)

	cid := "test-cleanup-cid"
	metadata := &DownloadMetadata{
		CID:         cid,
		Filename:    "test.bin",
		TotalChunks: 1,
		ChunksMap:   make([]bool, 1),
	}
	repo.SaveMetadata(metadata)

	// Trigger lock creation via UpdateChunkCompletion
	repo.UpdateChunkCompletion(cid, 0, true)

	// Verify lock exists
	repo.mu.Lock()
	if _, exists := repo.downloadLocks[cid]; !exists {
		t.Fatal("Lock should exist after update")
	}
	repo.mu.Unlock()

	// Clean up
	repo.CleanupDownloadLock(cid)

	// Verify lock removed
	repo.mu.Lock()
	if _, exists := repo.downloadLocks[cid]; exists {
		t.Error("Lock should be removed after cleanup")
	}
	repo.mu.Unlock()
}

// TestRepository_ConcurrentMetadataUpdates - RED test
// Acceptance Criterion: Concurrent chunk completion updates must not lose writes
func TestRepository_ConcurrentMetadataUpdates(t *testing.T) {
	tmpDir := t.TempDir()
	repo := NewRepository(tmpDir)

	cid := "test-concurrent-cid"
	totalChunks := 10

	// Create initial metadata (ChunksMap is []bool per repository.go:34)
	metadata := &DownloadMetadata{
		CID:         cid,
		Filename:    "test.bin",
		TotalChunks: totalChunks,
		ChunksMap:   make([]bool, totalChunks),
		State:       StateQueued,
	}
	err := repo.SaveMetadata(metadata)
	if err != nil {
		t.Fatalf("SaveMetadata failed: %v", err)
	}

	// 10 goroutines update different chunks concurrently
	var wg sync.WaitGroup
	for i := 0; i < totalChunks; i++ {
		wg.Add(1)
		chunkIndex := i
		go func() {
			defer wg.Done()
			err := repo.UpdateChunkCompletion(cid, chunkIndex, true)
			if err != nil {
				t.Errorf("UpdateChunkCompletion(%d) failed: %v", chunkIndex, err)
			}
		}()
	}

	wg.Wait()

	// Verify all chunks were persisted
	loaded, err := repo.LoadMetadata(cid)
	if err != nil {
		t.Fatalf("LoadMetadata failed: %v", err)
	}

	for i := 0; i < totalChunks; i++ {
		if !loaded.ChunksMap[i] {
			t.Errorf("Chunk %d was not persisted (lost update)", i)
		}
	}
}

// TestRepository_ConcurrentChunkAndStateUpdates - RED test
// Acceptance Criterion: Concurrent chunk + state updates must not lose either
func TestRepository_ConcurrentChunkAndStateUpdates(t *testing.T) {
	tmpDir := t.TempDir()
	repo := NewRepository(tmpDir)

	cid := "test-mixed-cid"
	metadata := &DownloadMetadata{
		CID:         cid,
		Filename:    "test.bin",
		TotalChunks: 5,
		ChunksMap:   make([]bool, 5),
		State:       StateQueued,
	}
	repo.SaveMetadata(metadata)

	var wg sync.WaitGroup

	// 5 workers updating chunks
	for i := 0; i < 5; i++ {
		wg.Add(1)
		chunkIndex := i
		go func() {
			defer wg.Done()
			repo.UpdateChunkCompletion(cid, chunkIndex, true)
		}()
	}

	// 1 worker updating state via UpdateMetadata
	wg.Add(1)
	go func() {
		defer wg.Done()
		repo.UpdateMetadata(cid, func(m *DownloadMetadata) error {
			m.State = StateActive
			return nil
		})
	}()

	wg.Wait()

	// Verify both chunks AND state persisted
	loaded, _ := repo.LoadMetadata(cid)

	for i := 0; i < 5; i++ {
		if !loaded.ChunksMap[i] {
			t.Errorf("Chunk %d lost", i)
		}
	}

	if loaded.State != StateActive {
		t.Errorf("State update lost, got %s want %s", loaded.State, StateActive)
	}
}
