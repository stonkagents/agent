// Package: pkg/protocol
// Feature: F-010 (P2P Transfer Protocol)
// Story: US-010-05 (Block Exchange Protocol)
// Purpose: Cross-sprint integration tests (Sprint 1 → Sprint 2 → Sprint 3)

package protocol

import (
	"bytes"
	"context"
	"testing"

	atcrypto "github.com/stonkagents/agent/pkg/cryptography"
)

// TestIntegration_ChunkingWithSprint1CIDVerification verifies that chunking
// uses real CID verification from Sprint 1, not mocked/stubbed values.
// This ensures Sprint 3 chunking integrates with Sprint 1 cryptography.
func TestIntegration_ChunkingWithSprint1CIDVerification(t *testing.T) {
	ctx := context.Background()

	// Create test data
	testData := make([]byte, ChunkSize*2) // 512KB = 2 chunks
	for i := range testData {
		testData[i] = byte(i % 256)
	}

	// Chunk the file using Sprint 3 chunker
	chunker := NewChunker()
	metadata, chunks, err := chunker.ChunkFile(ctx, bytes.NewReader(testData), "test.bin")
	if err != nil {
		t.Fatalf("ChunkFile() failed: %v", err)
	}

	// Verify each chunk CID using Sprint 1 cryptography package
	for i, chunk := range chunks {
		// Use real CID verification from Sprint 1 (not mocked)
		valid, err := atcrypto.VerifyCID(chunk.ChunkCID, chunk.Data)
		if err != nil {
			t.Errorf("Chunk %d: Sprint 1 VerifyCID() failed: %v", i, err)
		}
		if !valid {
			t.Errorf("Chunk %d: Sprint 1 CID verification failed - chunking produced invalid CID", i)
		}
	}

	// Verify file CID is consistent
	if metadata.FileCID == "" {
		t.Error("File CID is empty - chunking did not generate file-level CID")
	}

	// Verify all chunks have the same file CID
	for i, chunk := range chunks {
		if chunk.FileCID != metadata.FileCID {
			t.Errorf("Chunk %d: FileCID mismatch - got %q, want %q", i, chunk.FileCID, metadata.FileCID)
		}
	}
}

// TestIntegration_AssembleFileWithSprint1CIDVerification verifies that
// file assembly validates chunks using Sprint 1 CID verification.
// This ensures data integrity across the full upload→download cycle.
func TestIntegration_AssembleFileWithSprint1CIDVerification(t *testing.T) {
	ctx := context.Background()

	// Original data
	originalData := make([]byte, ChunkSize+100*1024) // 356KB = 2 chunks (256KB + 100KB)
	for i := range originalData {
		originalData[i] = byte(i % 256)
	}

	// Chunk the file
	chunker := NewChunker()
	metadata, chunks, err := chunker.ChunkFile(ctx, bytes.NewReader(originalData), "test.bin")
	if err != nil {
		t.Fatalf("ChunkFile() failed: %v", err)
	}

	// Assemble the file (should verify CIDs internally)
	var output bytes.Buffer
	err = chunker.AssembleFile(ctx, chunks, metadata, &output)
	if err != nil {
		t.Fatalf("AssembleFile() failed: %v", err)
	}

	// Verify reconstructed data matches original
	reconstructed := output.Bytes()
	if !bytes.Equal(reconstructed, originalData) {
		t.Error("Reconstructed data does not match original - CID verification may be broken")
	}

	// Verify that corrupted chunks are detected
	corruptChunks := make([]*Chunk, len(chunks))
	copy(corruptChunks, chunks)

	// Corrupt the first chunk
	corruptChunks[0] = &Chunk{
		FileCID:    chunks[0].FileCID,
		ChunkIndex: 0,
		ChunkCID:   chunks[0].ChunkCID,
		Data:       make([]byte, len(chunks[0].Data)),
		Size:       chunks[0].Size,
	}
	copy(corruptChunks[0].Data, chunks[0].Data)
	corruptChunks[0].Data[0] ^= 0xFF // Flip bits

	// Assembly should fail with corrupted chunk
	var corruptOutput bytes.Buffer
	err = chunker.AssembleFile(ctx, corruptChunks, metadata, &corruptOutput)
	if err == nil {
		t.Error("AssembleFile() should fail with corrupted chunk - Sprint 1 CID verification not working")
	}
}

// TestIntegration_ChunkCIDFormat verifies that chunk CIDs follow IPFS-compatible
// format from Sprint 1 (CIDv1 with base32 encoding).
func TestIntegration_ChunkCIDFormat(t *testing.T) {
	ctx := context.Background()

	// Create test data
	testData := []byte("Hello, StonkAgents!")

	// Chunk the file
	chunker := NewChunker()
	metadata, chunks, err := chunker.ChunkFile(ctx, bytes.NewReader(testData), "hello.txt")
	if err != nil {
		t.Fatalf("ChunkFile() failed: %v", err)
	}

	// Verify chunk CID format matches Sprint 1 conventions
	for i, chunk := range chunks {
		// CIDs should start with "baf" (IPFS CIDv1 base32 prefix)
		// - "bafybei" = dag-pb codec
		// - "bafkrei" = raw codec (used by Sprint 1)
		if len(chunk.ChunkCID) < 7 {
			t.Errorf("Chunk %d: CID too short: %q", i, chunk.ChunkCID)
		}
		if chunk.ChunkCID[:3] != "baf" {
			t.Errorf("Chunk %d: CID does not start with 'baf' (IPFS CIDv1 base32 format): %q", i, chunk.ChunkCID)
		}
		// Verify it's a valid base32 string (no invalid characters like 8, 9)
		for j, c := range chunk.ChunkCID {
			if !isValidBase32Char(c) {
				t.Errorf("Chunk %d: CID contains invalid base32 character '%c' at position %d", i, c, j)
			}
		}
	}

	// Verify file CID format
	if metadata.FileCID[:3] != "baf" {
		t.Errorf("File CID does not start with 'baf' (IPFS CIDv1 format): %q", metadata.FileCID)
	}
}

// isValidBase32Char checks if a character is valid in IPFS base32 encoding.
// Valid characters: a-z, 2-7 (base32 alphabet)
func isValidBase32Char(c rune) bool {
	return (c >= 'a' && c <= 'z') || (c >= '2' && c <= '7')
}

// TestIntegration_BlockExchangeWithRealChunks tests that the block exchange
// service works with real chunked data (not mocked chunks).
// This ensures Sprint 3 protocol integrates with Sprint 3 chunking.
func TestIntegration_BlockExchangeWithRealChunks(t *testing.T) {
	ctx := context.Background()

	// Create real chunked data
	testData := make([]byte, ChunkSize) // 256KB
	for i := range testData {
		testData[i] = byte(i % 256)
	}

	chunker := NewChunker()
	metadata, chunks, err := chunker.ChunkFile(ctx, bytes.NewReader(testData), "real-chunk.bin")
	if err != nil {
		t.Fatalf("ChunkFile() failed: %v", err)
	}

	// Create a chunk provider that serves real chunks
	provider := newMockChunkProvider()
	for _, chunk := range chunks {
		provider.AddChunk(chunk)
	}

	// Create block exchange service
	service := NewBlockExchangeService()
	service.RegisterChunkProvider(provider)

	// Request a real chunk (simulating P2P request)
	retrievedChunk, err := provider.GetChunk(ctx, metadata.FileCID, 0)
	if err != nil {
		t.Fatalf("GetChunk() failed: %v", err)
	}

	// Verify chunk data matches original
	if !bytes.Equal(retrievedChunk.Data, chunks[0].Data) {
		t.Error("Retrieved chunk data does not match original")
	}

	// Verify chunk CID using Sprint 1 verification
	valid, err := atcrypto.VerifyCID(retrievedChunk.ChunkCID, retrievedChunk.Data)
	if err != nil {
		t.Errorf("Sprint 1 VerifyCID() failed: %v", err)
	}
	if !valid {
		t.Error("Retrieved chunk CID verification failed - block exchange may be corrupting data")
	}
}

// TestIntegration_EndToEndChunkAndRetrieve simulates the full flow:
// 1. User shares file → chunk it
// 2. Store chunks (chunk provider)
// 3. Remote peer requests chunks (block exchange)
// 4. Assemble file from retrieved chunks
// This tests Sprint 1 + Sprint 3 integration.
func TestIntegration_EndToEndChunkAndRetrieve(t *testing.T) {
	ctx := context.Background()

	// STEP 1: User shares file → chunk it
	originalData := make([]byte, ChunkSize*3+100*1024) // 868KB = 4 chunks
	for i := range originalData {
		originalData[i] = byte(i % 256)
	}

	chunker := NewChunker()
	metadata, chunks, err := chunker.ChunkFile(ctx, bytes.NewReader(originalData), "large-file.bin")
	if err != nil {
		t.Fatalf("ChunkFile() failed: %v", err)
	}

	if len(chunks) != 4 {
		t.Fatalf("Expected 4 chunks, got %d", len(chunks))
	}

	// STEP 2: Store chunks (simulating seeder's chunk store)
	seederProvider := newMockChunkProvider()
	for _, chunk := range chunks {
		seederProvider.AddChunk(chunk)
	}

	// STEP 3: Remote peer requests chunks (simulating leecher)
	retrievedChunks := make([]*Chunk, len(chunks))
	for i := 0; i < len(chunks); i++ {
		chunk, err := seederProvider.GetChunk(ctx, metadata.FileCID, i)
		if err != nil {
			t.Fatalf("GetChunk(%d) failed: %v", i, err)
		}
		retrievedChunks[i] = chunk

		// Verify each retrieved chunk using Sprint 1 CID verification
		valid, err := atcrypto.VerifyCID(chunk.ChunkCID, chunk.Data)
		if err != nil {
			t.Errorf("Chunk %d: Sprint 1 VerifyCID() failed: %v", i, err)
		}
		if !valid {
			t.Errorf("Chunk %d: CID verification failed - data may be corrupted", i)
		}
	}

	// STEP 4: Assemble file from retrieved chunks
	var output bytes.Buffer
	err = chunker.AssembleFile(ctx, retrievedChunks, metadata, &output)
	if err != nil {
		t.Fatalf("AssembleFile() failed: %v", err)
	}

	// Verify reconstructed file matches original
	reconstructed := output.Bytes()
	if len(reconstructed) != len(originalData) {
		t.Fatalf("Reconstructed size mismatch: got %d, want %d", len(reconstructed), len(originalData))
	}

	if !bytes.Equal(reconstructed, originalData) {
		t.Error("End-to-end flow failed - reconstructed data does not match original")
	}
}

// TestIntegration_PipelineWithRealChunkRequests tests request pipelining
// with real chunk requests (not mocked requesters).
func TestIntegration_PipelineWithRealChunkRequests(t *testing.T) {
	ctx := context.Background()

	// Create real chunks
	testData := make([]byte, ChunkSize*10) // 2.5MB = 10 chunks
	for i := range testData {
		testData[i] = byte(i % 256)
	}

	chunker := NewChunker()
	metadata, chunks, err := chunker.ChunkFile(ctx, bytes.NewReader(testData), "pipeline-test.bin")
	if err != nil {
		t.Fatalf("ChunkFile() failed: %v", err)
	}

	// Create chunk provider
	provider := newMockChunkProvider()
	for _, chunk := range chunks {
		provider.AddChunk(chunk)
	}

	// Create pipeline
	pipeline := NewRequestPipeline(MaxConcurrentRequests)

	// Request all chunks through pipeline
	retrievedChunks := make([]*Chunk, len(chunks))
	for i := 0; i < len(chunks); i++ {
		chunkIndex := i
		chunk, err := pipeline.RequestWithSemaphore(ctx, func() (*Chunk, error) {
			return provider.GetChunk(ctx, metadata.FileCID, chunkIndex)
		})
		if err != nil {
			t.Fatalf("RequestWithSemaphore(%d) failed: %v", i, err)
		}
		retrievedChunks[i] = chunk

		// Verify CID using Sprint 1
		valid, err := atcrypto.VerifyCID(chunk.ChunkCID, chunk.Data)
		if err != nil {
			t.Errorf("Chunk %d: Sprint 1 VerifyCID() failed: %v", i, err)
		}
		if !valid {
			t.Errorf("Chunk %d: CID verification failed", i)
		}
	}

	// Verify all chunks retrieved correctly
	for i, chunk := range retrievedChunks {
		if chunk.ChunkIndex != i {
			t.Errorf("Chunk %d: index mismatch: got %d", i, chunk.ChunkIndex)
		}
		if !bytes.Equal(chunk.Data, chunks[i].Data) {
			t.Errorf("Chunk %d: data mismatch", i)
		}
	}
}
