// Package: pkg/protocol
// Feature: F-010 (P2P Transfer Protocol)
// Story: US-010-05 (Block Exchange Protocol)
// Purpose: File chunking and assembly utilities

package protocol

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	atcrypto "github.com/stonkagents/agent/pkg/cryptography"
)

// chunker implements the Chunker interface for splitting files into 256KB chunks.
type chunker struct {
	chunkSize int
}

// NewChunker creates a new Chunker with default 256KB chunk size.
func NewChunker() *chunker {
	return &chunker{
		chunkSize: ChunkSize,
	}
}

// ChunkFile splits a file into 256KB chunks and returns metadata.
// Implements TDD Step 4 (GREEN): Make TestChunkFile1MB pass.
func (c *chunker) ChunkFile(ctx context.Context, reader io.Reader, filename string) (*ChunkMetadata, []*Chunk, error) {
	var chunks []*Chunk
	var chunkCIDs []string
	var totalSize int64
	chunkIndex := 0

	// Read file in chunk-sized blocks
	buffer := make([]byte, c.chunkSize)
	for {
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		default:
		}

		n, err := io.ReadFull(reader, buffer)
		if err == io.EOF {
			// No more data to read
			break
		}
		if err != nil && err != io.ErrUnexpectedEOF {
			return nil, nil, fmt.Errorf("failed to read chunk %d: %w", chunkIndex, err)
		}

		// Handle last chunk (may be smaller than chunkSize)
		chunkData := buffer[:n]
		totalSize += int64(n)

		// Calculate chunk CID using SHA-256 hash
		chunkCID, err := c.calculateChunkCID(chunkData)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to calculate CID for chunk %d: %w", chunkIndex, err)
		}

		chunk := &Chunk{
			FileCID:    "", // Will be set after calculating file CID
			ChunkIndex: chunkIndex,
			ChunkCID:   chunkCID,
			Data:       make([]byte, n),
			Size:       int64(n),
			ReceivedAt: time.Now(),
		}
		copy(chunk.Data, chunkData)

		chunks = append(chunks, chunk)
		chunkCIDs = append(chunkCIDs, chunkCID)
		chunkIndex++

		// If we read less than full buffer, we're done
		if n < c.chunkSize {
			break
		}
	}

	// Calculate file CID as Merkle root of chunk CIDs
	fileCID, err := c.calculateFileCID(chunkCIDs)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to calculate file CID: %w", err)
	}

	// Update all chunks with the file CID
	for _, chunk := range chunks {
		chunk.FileCID = fileCID
	}

	metadata := &ChunkMetadata{
		FileCID:     fileCID,
		Filename:    filename,
		TotalSize:   totalSize,
		TotalChunks: len(chunks),
		ChunkSize:   c.chunkSize,
		ChunkCIDs:   chunkCIDs,
	}

	return metadata, chunks, nil
}

// ChunkFileFromPath is a convenience method that opens a file and chunks it.
func (c *chunker) ChunkFileFromPath(ctx context.Context, filepath string) (*ChunkMetadata, []*Chunk, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open file %s: %w", filepath, err)
	}
	defer file.Close()

	// Get filename from path
	filename := filepath
	if idx := len(filepath) - 1; idx >= 0 {
		for i := idx; i >= 0; i-- {
			if filepath[i] == '/' || filepath[i] == '\\' {
				filename = filepath[i+1:]
				break
			}
		}
	}

	return c.ChunkFile(ctx, file, filename)
}

// AssembleFile reconstructs a complete file from chunks.
// Verifies each chunk CID and the final file CID.
func (c *chunker) AssembleFile(ctx context.Context, chunks []*Chunk, metadata *ChunkMetadata, writer io.Writer) error {
	if len(chunks) != metadata.TotalChunks {
		return fmt.Errorf("chunk count mismatch: got %d, expected %d", len(chunks), metadata.TotalChunks)
	}

	// Sort chunks by index (in case they arrived out of order)
	// For now, assume they're already sorted - will add sorting later if needed

	// Write chunks in order
	for i, chunk := range chunks {
		if chunk.ChunkIndex != i {
			return fmt.Errorf("chunk index mismatch: got %d at position %d", chunk.ChunkIndex, i)
		}

		// Verify chunk CID before writing
		if err := c.VerifyChunk(ctx, chunk); err != nil {
			return fmt.Errorf("chunk %d verification failed: %w", i, err)
		}

		// Write chunk data
		n, err := writer.Write(chunk.Data)
		if err != nil {
			return fmt.Errorf("failed to write chunk %d: %w", i, err)
		}
		if n != len(chunk.Data) {
			return fmt.Errorf("incomplete write for chunk %d: wrote %d bytes, expected %d", i, n, len(chunk.Data))
		}
	}

	// Verify file CID matches
	calculatedFileCID, err := c.calculateFileCID(metadata.ChunkCIDs)
	if err != nil {
		return fmt.Errorf("failed to calculate file CID: %w", err)
	}
	if calculatedFileCID != metadata.FileCID {
		return fmt.Errorf("file CID mismatch: got %s, expected %s", calculatedFileCID, metadata.FileCID)
	}

	return nil
}

// AssembleFileToPath is a convenience method that creates a file and writes to it.
func (c *chunker) AssembleFileToPath(ctx context.Context, chunks []*Chunk, metadata *ChunkMetadata, outputPath string) error {
	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create file %s: %w", outputPath, err)
	}
	defer file.Close()

	return c.AssembleFile(ctx, chunks, metadata, file)
}

// VerifyChunk verifies a chunk's CID matches its data.
func (c *chunker) VerifyChunk(ctx context.Context, chunk *Chunk) error {
	calculatedCID, err := c.calculateChunkCID(chunk.Data)
	if err != nil {
		return fmt.Errorf("failed to calculate chunk CID: %w", err)
	}

	if calculatedCID != chunk.ChunkCID {
		return fmt.Errorf("chunk CID mismatch: calculated %s, expected %s", calculatedCID, chunk.ChunkCID)
	}

	return nil
}

// VerifyFile verifies all chunks and the final file CID.
func (c *chunker) VerifyFile(ctx context.Context, chunks []*Chunk, metadata *ChunkMetadata) error {
	// Verify each chunk
	for i, chunk := range chunks {
		if err := c.VerifyChunk(ctx, chunk); err != nil {
			return fmt.Errorf("chunk %d verification failed: %w", i, err)
		}
	}

	// Verify file CID
	calculatedFileCID, err := c.calculateFileCID(metadata.ChunkCIDs)
	if err != nil {
		return fmt.Errorf("failed to calculate file CID: %w", err)
	}
	if calculatedFileCID != metadata.FileCID {
		return fmt.Errorf("file CID mismatch: calculated %s, expected %s", calculatedFileCID, metadata.FileCID)
	}

	return nil
}

// calculateChunkCID calculates the CID for a chunk using Sprint 1 cryptography.
// Uses proper IPFS CIDv1 format with multihash (not a placeholder).
func (c *chunker) calculateChunkCID(data []byte) (string, error) {
	// Use Sprint 1 CID generation (IPFS CIDv1 compatible)
	return atcrypto.GenerateCID(data)
}

// calculateFileCID calculates the Merkle root of chunk CIDs.
// This is the file's content identifier using Sprint 1 cryptography.
func (c *chunker) calculateFileCID(chunkCIDs []string) (string, error) {
	if len(chunkCIDs) == 0 {
		// Empty file: CID of empty byte array
		return atcrypto.GenerateCID([]byte{})
	}

	// Concatenate all chunk CIDs and hash them using Sprint 1 CID generation
	combined := ""
	for _, cid := range chunkCIDs {
		combined += cid
	}

	// Use Sprint 1 CID generation for file-level CID
	return atcrypto.GenerateCID([]byte(combined))
}

// VerifyCIDWrapper wraps the CID verification from pkg/cryptography.
// Uses the existing VerifyCID function that was implemented in Sprint 1.
func VerifyCIDWrapper(cid string, data []byte) error {
	// Use existing CID verification from pkg/cryptography
	valid, err := atcrypto.VerifyCID(cid, data)
	if err != nil {
		return fmt.Errorf("CID verification error: %w", err)
	}
	if !valid {
		return fmt.Errorf("CID mismatch: verification failed")
	}
	return nil
}

// CalculateChunkCID is a public wrapper for calculating chunk CIDs
// Used by download manager for Phase 4 file assembly
func CalculateChunkCID(data []byte) (string, error) {
	c := NewChunker()
	return c.calculateChunkCID(data)
}
