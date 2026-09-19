// Package: pkg/protocol
// Feature: F-010 (P2P Transfer Protocol)
// Story: US-010-05 (Block Exchange Protocol)
// Purpose: TDD tests for chunking utilities

package protocol

import (
	"bytes"
	"context"
	"testing"
)

// TestChunkFile1MB tests splitting a 1MB file into 4 x 256KB chunks.
// TDD Step 3 (RED): This test will FAIL until we implement ChunkFile().
func TestChunkFile1MB(t *testing.T) {
	ctx := context.Background()

	// Create a 1MB test file
	fileSize := 1 * 1024 * 1024 // 1 MB
	testData := make([]byte, fileSize)
	for i := range testData {
		testData[i] = byte(i % 256)
	}
	reader := bytes.NewReader(testData)

	// Chunk the file
	chunker := NewChunker()
	metadata, chunks, err := chunker.ChunkFile(ctx, reader, "test-1mb.bin")
	if err != nil {
		t.Fatalf("ChunkFile() failed: %v", err)
	}

	// Verify metadata
	if metadata.Filename != "test-1mb.bin" {
		t.Errorf("Filename mismatch: got %q, want %q", metadata.Filename, "test-1mb.bin")
	}
	if metadata.TotalSize != int64(fileSize) {
		t.Errorf("TotalSize mismatch: got %d, want %d", metadata.TotalSize, fileSize)
	}
	if metadata.TotalChunks != 4 {
		t.Errorf("TotalChunks mismatch: got %d, want 4", metadata.TotalChunks)
	}
	if len(metadata.ChunkCIDs) != 4 {
		t.Errorf("ChunkCIDs length mismatch: got %d, want 4", len(metadata.ChunkCIDs))
	}

	// Verify chunks
	if len(chunks) != 4 {
		t.Fatalf("Chunks count mismatch: got %d, want 4", len(chunks))
	}

	// Each chunk should be 256KB (262,144 bytes)
	for i, chunk := range chunks {
		if chunk.ChunkIndex != i {
			t.Errorf("Chunk %d: index mismatch: got %d, want %d", i, chunk.ChunkIndex, i)
		}
		if chunk.Size != ChunkSize {
			t.Errorf("Chunk %d: size mismatch: got %d, want %d", i, chunk.Size, ChunkSize)
		}
		if len(chunk.Data) != ChunkSize {
			t.Errorf("Chunk %d: data length mismatch: got %d, want %d", i, len(chunk.Data), ChunkSize)
		}
		if chunk.ChunkCID == "" {
			t.Errorf("Chunk %d: ChunkCID is empty", i)
		}
		if chunk.FileCID == "" {
			t.Errorf("Chunk %d: FileCID is empty", i)
		}
	}

	// Verify file CID is set
	if metadata.FileCID == "" {
		t.Error("FileCID is empty")
	}

	// All chunks should have the same FileCID
	for i, chunk := range chunks {
		if chunk.FileCID != metadata.FileCID {
			t.Errorf("Chunk %d: FileCID mismatch: got %q, want %q", i, chunk.FileCID, metadata.FileCID)
		}
	}
}

// TestChunkFileSmallerThan256KB tests chunking a file smaller than one chunk.
func TestChunkFileSmallerThan256KB(t *testing.T) {
	ctx := context.Background()

	// Create a 100KB test file
	fileSize := 100 * 1024 // 100 KB
	testData := make([]byte, fileSize)
	for i := range testData {
		testData[i] = byte(i % 256)
	}
	reader := bytes.NewReader(testData)

	chunker := NewChunker()
	metadata, chunks, err := chunker.ChunkFile(ctx, reader, "test-100kb.bin")
	if err != nil {
		t.Fatalf("ChunkFile() failed: %v", err)
	}

	// Should create exactly 1 chunk
	if metadata.TotalChunks != 1 {
		t.Errorf("TotalChunks mismatch: got %d, want 1", metadata.TotalChunks)
	}
	if len(chunks) != 1 {
		t.Fatalf("Chunks count mismatch: got %d, want 1", len(chunks))
	}

	// Chunk should have the actual file size (not full 256KB)
	if chunks[0].Size != int64(fileSize) {
		t.Errorf("Chunk size mismatch: got %d, want %d", chunks[0].Size, fileSize)
	}
	if len(chunks[0].Data) != fileSize {
		t.Errorf("Chunk data length mismatch: got %d, want %d", len(chunks[0].Data), fileSize)
	}
}

// TestChunkFileExactly256KB tests chunking a file exactly one chunk size.
func TestChunkFileExactly256KB(t *testing.T) {
	ctx := context.Background()

	// Create exactly 256KB file
	fileSize := ChunkSize
	testData := make([]byte, fileSize)
	for i := range testData {
		testData[i] = byte(i % 256)
	}
	reader := bytes.NewReader(testData)

	chunker := NewChunker()
	metadata, chunks, err := chunker.ChunkFile(ctx, reader, "test-256kb.bin")
	if err != nil {
		t.Fatalf("ChunkFile() failed: %v", err)
	}

	// Should create exactly 1 chunk
	if metadata.TotalChunks != 1 {
		t.Errorf("TotalChunks mismatch: got %d, want 1", metadata.TotalChunks)
	}
	if len(chunks) != 1 {
		t.Fatalf("Chunks count mismatch: got %d, want 1", len(chunks))
	}

	if chunks[0].Size != int64(fileSize) {
		t.Errorf("Chunk size mismatch: got %d, want %d", chunks[0].Size, fileSize)
	}
}

// TestChunkFileLargeFile tests chunking a file larger than 1MB.
func TestChunkFileLargeFile(t *testing.T) {
	ctx := context.Background()

	// Create a 1.5MB test file (should create 6 chunks: 5 full + 1 partial)
	fileSize := 1536 * 1024 // 1.5 MB
	testData := make([]byte, fileSize)
	for i := range testData {
		testData[i] = byte(i % 256)
	}
	reader := bytes.NewReader(testData)

	chunker := NewChunker()
	metadata, chunks, err := chunker.ChunkFile(ctx, reader, "test-1.5mb.bin")
	if err != nil {
		t.Fatalf("ChunkFile() failed: %v", err)
	}

	// Should create 6 chunks (1.5MB / 256KB = 5.859... → 6 chunks)
	expectedChunks := 6
	if metadata.TotalChunks != expectedChunks {
		t.Errorf("TotalChunks mismatch: got %d, want %d", metadata.TotalChunks, expectedChunks)
	}
	if len(chunks) != expectedChunks {
		t.Fatalf("Chunks count mismatch: got %d, want %d", len(chunks), expectedChunks)
	}

	// First 5 chunks should be full 256KB
	for i := 0; i < 5; i++ {
		if chunks[i].Size != ChunkSize {
			t.Errorf("Chunk %d: size mismatch: got %d, want %d", i, chunks[i].Size, ChunkSize)
		}
	}

	// Last chunk should be partial (1.5MB - 5*256KB = 256KB)
	// 1536KB - 1280KB (5 chunks) = 256KB
	lastChunkSize := int64(fileSize - 5*ChunkSize)
	if chunks[5].Size != lastChunkSize {
		t.Errorf("Last chunk size mismatch: got %d, want %d", chunks[5].Size, lastChunkSize)
	}
}

// TestAssembleFile tests reconstructing a file from chunks.
// TDD: This test will FAIL until we implement AssembleFile().
func TestAssembleFile(t *testing.T) {
	ctx := context.Background()

	// Create original data
	originalData := make([]byte, 1024*1024) // 1MB
	for i := range originalData {
		originalData[i] = byte(i % 256)
	}

	// Chunk the file
	chunker := NewChunker()
	metadata, chunks, err := chunker.ChunkFile(ctx, bytes.NewReader(originalData), "test.bin")
	if err != nil {
		t.Fatalf("ChunkFile() failed: %v", err)
	}

	// Assemble the file back
	var output bytes.Buffer
	err = chunker.AssembleFile(ctx, chunks, metadata, &output)
	if err != nil {
		t.Fatalf("AssembleFile() failed: %v", err)
	}

	// Verify reconstructed data matches original
	reconstructed := output.Bytes()
	if len(reconstructed) != len(originalData) {
		t.Fatalf("Reconstructed size mismatch: got %d, want %d", len(reconstructed), len(originalData))
	}

	if !bytes.Equal(reconstructed, originalData) {
		t.Error("Reconstructed data does not match original")
	}
}

// TestVerifyChunk tests chunk CID verification.
// TDD: This test will FAIL until we implement VerifyChunk().
func TestVerifyChunk(t *testing.T) {
	ctx := context.Background()

	// Create test data and chunk it
	testData := make([]byte, ChunkSize)
	for i := range testData {
		testData[i] = byte(i % 256)
	}

	chunker := NewChunker()
	metadata, chunks, err := chunker.ChunkFile(ctx, bytes.NewReader(testData), "test.bin")
	if err != nil {
		t.Fatalf("ChunkFile() failed: %v", err)
	}

	// Verify the chunk - should pass
	err = chunker.VerifyChunk(ctx, chunks[0])
	if err != nil {
		t.Errorf("VerifyChunk() failed for valid chunk: %v", err)
	}

	// Corrupt the chunk data - verification should fail
	corruptChunk := &Chunk{
		FileCID:    metadata.FileCID,
		ChunkIndex: 0,
		ChunkCID:   chunks[0].ChunkCID,
		Data:       make([]byte, len(chunks[0].Data)),
		Size:       chunks[0].Size,
	}
	copy(corruptChunk.Data, chunks[0].Data)
	corruptChunk.Data[0] ^= 0xFF // Flip bits

	err = chunker.VerifyChunk(ctx, corruptChunk)
	if err == nil {
		t.Error("VerifyChunk() should fail for corrupted chunk data")
	}
}

// TestEmptyFile tests chunking an empty file.
func TestEmptyFile(t *testing.T) {
	ctx := context.Background()

	chunker := NewChunker()
	metadata, chunks, err := chunker.ChunkFile(ctx, bytes.NewReader([]byte{}), "empty.bin")
	if err != nil {
		t.Fatalf("ChunkFile() failed for empty file: %v", err)
	}

	// Empty file should create 0 chunks
	if metadata.TotalChunks != 0 {
		t.Errorf("TotalChunks mismatch for empty file: got %d, want 0", metadata.TotalChunks)
	}
	if len(chunks) != 0 {
		t.Errorf("Chunks count mismatch for empty file: got %d, want 0", len(chunks))
	}
	if metadata.TotalSize != 0 {
		t.Errorf("TotalSize mismatch for empty file: got %d, want 0", metadata.TotalSize)
	}
}
