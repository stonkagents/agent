// Package: internal/daemon/chunking
// Feature: F-010 (P2P Transfer Protocol)
// Story: US-010-07 (Upload Queue & Seeding)
// Purpose: TDD tests for file chunking logic

package chunking

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestChunker_SplitFile - RED test
// Acceptance Criterion: Split file into 256 KB chunks
func TestChunker_SplitFile(t *testing.T) {
	// Arrange - create 1 MB test file (should split into 4 chunks of 256 KB)
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.bin")

	// Create 1 MB file filled with pattern
	data := make([]byte, 1048576) // 1 MB
	for i := range data {
		data[i] = byte(i % 256)
	}
	if err := os.WriteFile(testFile, data, 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Act - split file into chunks
	chunker := NewChunker(262144) // 256 KB chunk size
	chunks, err := chunker.SplitFile(testFile)

	// Assert
	if err != nil {
		t.Fatalf("SplitFile() failed: %v", err)
	}

	// Verify 4 chunks created
	if len(chunks) != 4 {
		t.Errorf("Expected 4 chunks for 1 MB file, got %d", len(chunks))
	}

	// Verify chunk sizes (all 256 KB)
	for i, chunk := range chunks {
		if chunk.Size != 262144 {
			t.Errorf("Chunk %d expected size 262144, got %d", i, chunk.Size)
		}
	}
}

// TestChunker_ChunkCIDs - RED test
// Acceptance Criterion: Each chunk has unique CID for verification
func TestChunker_ChunkCIDs(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "cid-test.bin")

	// Create test file with distinct data for each chunk
	// First chunk: all zeros, Second chunk: all ones
	data := make([]byte, 524288) // 512 KB → 2 chunks
	for i := range data {
		if i < 262144 {
			data[i] = 0 // First chunk
		} else {
			data[i] = 1 // Second chunk (different data)
		}
	}
	os.WriteFile(testFile, data, 0644)

	// Act
	chunker := NewChunker(262144)
	chunks, err := chunker.SplitFile(testFile)

	// Assert
	if err != nil {
		t.Fatalf("SplitFile() failed: %v", err)
	}

	// Verify each chunk has a CID
	for i, chunk := range chunks {
		if chunk.CID == "" {
			t.Errorf("Chunk %d missing CID", i)
		}
	}

	// Verify CIDs are unique
	if chunks[0].CID == chunks[1].CID {
		t.Error("Chunks with different data should have different CIDs")
	}
}

// TestChunker_FileCID - RED test
// Acceptance Criterion: File CID = Merkle root of chunk CIDs
func TestChunker_FileCID(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "file-cid-test.bin")

	data := make([]byte, 524288) // 512 KB
	for i := range data {
		data[i] = byte(i % 256)
	}
	os.WriteFile(testFile, data, 0644)

	// Act
	chunker := NewChunker(262144)
	chunks, err := chunker.SplitFile(testFile)
	if err != nil {
		t.Fatalf("SplitFile() failed: %v", err)
	}

	// Calculate file CID from chunks
	fileCID := chunker.CalculateFileCID(chunks)

	// Assert - file CID should be non-empty
	if fileCID == "" {
		t.Error("File CID should be calculated from chunk CIDs")
	}

	// Verify file CID is deterministic (same input → same CID)
	fileCID2 := chunker.CalculateFileCID(chunks)
	if fileCID != fileCID2 {
		t.Error("File CID should be deterministic")
	}
}

// TestChunker_ReassembleFile - RED test
// Acceptance Criterion: Reassemble chunks back into original file
func TestChunker_ReassembleFile(t *testing.T) {
	// Arrange - create original file
	tmpDir := t.TempDir()
	originalFile := filepath.Join(tmpDir, "original.bin")
	reassembledFile := filepath.Join(tmpDir, "reassembled.bin")

	originalData := make([]byte, 1048576) // 1 MB
	for i := range originalData {
		originalData[i] = byte(i % 256)
	}
	os.WriteFile(originalFile, originalData, 0644)

	// Act - split then reassemble
	chunker := NewChunker(262144)
	chunks, err := chunker.SplitFile(originalFile)
	if err != nil {
		t.Fatalf("SplitFile() failed: %v", err)
	}

	err = chunker.ReassembleFile(chunks, reassembledFile)
	if err != nil {
		t.Fatalf("ReassembleFile() failed: %v", err)
	}

	// Assert - reassembled file should match original
	reassembledData, err := os.ReadFile(reassembledFile)
	if err != nil {
		t.Fatalf("Failed to read reassembled file: %v", err)
	}

	if !bytes.Equal(originalData, reassembledData) {
		t.Error("Reassembled file does not match original")
	}
}

// TestChunker_HandleSmallFile - RED test
// Acceptance Criterion: Files smaller than chunk size = 1 chunk
func TestChunker_HandleSmallFile(t *testing.T) {
	// Arrange - create 100 KB file (smaller than 256 KB chunk)
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "small.bin")

	data := make([]byte, 102400) // 100 KB
	for i := range data {
		data[i] = byte(i % 256)
	}
	os.WriteFile(testFile, data, 0644)

	// Act
	chunker := NewChunker(262144)
	chunks, err := chunker.SplitFile(testFile)

	// Assert
	if err != nil {
		t.Fatalf("SplitFile() failed: %v", err)
	}

	// Should create exactly 1 chunk
	if len(chunks) != 1 {
		t.Errorf("Expected 1 chunk for small file, got %d", len(chunks))
	}

	// Chunk size should match file size
	if chunks[0].Size != 102400 {
		t.Errorf("Expected chunk size 102400, got %d", chunks[0].Size)
	}
}

// TestChunker_HandleLastChunk - RED test
// Acceptance Criterion: Last chunk can be smaller than chunk size
func TestChunker_HandleLastChunk(t *testing.T) {
	// Arrange - create 300 KB file (1 full chunk + 1 partial chunk)
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "partial.bin")

	data := make([]byte, 307200) // 300 KB
	for i := range data {
		data[i] = byte(i % 256)
	}
	os.WriteFile(testFile, data, 0644)

	// Act
	chunker := NewChunker(262144) // 256 KB chunks
	chunks, err := chunker.SplitFile(testFile)

	// Assert
	if err != nil {
		t.Fatalf("SplitFile() failed: %v", err)
	}

	// Should create 2 chunks
	if len(chunks) != 2 {
		t.Errorf("Expected 2 chunks, got %d", len(chunks))
	}

	// First chunk = 256 KB
	if chunks[0].Size != 262144 {
		t.Errorf("First chunk expected 262144 bytes, got %d", chunks[0].Size)
	}

	// Last chunk = 44 KB (300 KB - 256 KB)
	expectedLastSize := 307200 - 262144 // 45056 bytes
	if chunks[1].Size != expectedLastSize {
		t.Errorf("Last chunk expected %d bytes, got %d", expectedLastSize, chunks[1].Size)
	}
}

// TestChunker_VerifyChunk - RED test
// Acceptance Criterion: Verify chunk data matches CID
func TestChunker_VerifyChunk(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "verify.bin")

	data := make([]byte, 262144) // 256 KB
	for i := range data {
		data[i] = byte(i % 256)
	}
	os.WriteFile(testFile, data, 0644)

	chunker := NewChunker(262144)
	chunks, _ := chunker.SplitFile(testFile)
	chunk := chunks[0]

	// Act - verify chunk with correct data
	chunkData, _ := os.ReadFile(testFile)
	valid := chunker.VerifyChunk(chunk.CID, chunkData)

	// Assert - should be valid
	if !valid {
		t.Error("Chunk verification failed for correct data")
	}

	// Act - verify chunk with corrupted data
	corruptedData := make([]byte, len(chunkData))
	copy(corruptedData, chunkData)
	corruptedData[0] = ^corruptedData[0] // Flip bits
	valid = chunker.VerifyChunk(chunk.CID, corruptedData)

	// Assert - should be invalid
	if valid {
		t.Error("Chunk verification should fail for corrupted data")
	}
}
