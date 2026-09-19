// Package: internal/daemon/chunking
// Feature: F-010 (P2P Transfer Protocol)
// Story: US-010-07 (Upload Queue & Seeding)
// Purpose: File chunking logic for P2P transfer

package chunking

import (
	"fmt"
	"os"

	"github.com/stonkagents/agent/pkg/cryptography"
)

// Chunk represents a file chunk with its CID
type Chunk struct {
	Index      int    // Chunk index (0-based)
	CID        string // Content ID of chunk data
	Size       int    // Chunk size in bytes
	Offset     int64  // Offset in source file
	Data       []byte // Optional: chunk data (for in-memory operations)
	SourceFile string // Optional: source file path for reassembly
}

// Chunker splits files into fixed-size chunks
type Chunker struct {
	chunkSize int // Chunk size in bytes
}

// NewChunker creates a new file chunker
// chunkSize: size of each chunk in bytes (e.g., 262144 for 256 KB)
func NewChunker(chunkSize int) *Chunker {
	return &Chunker{
		chunkSize: chunkSize,
	}
}

// SplitFile splits a file into chunks and calculates CID for each chunk
// Returns array of Chunk metadata (CID, Size, Index)
func (c *Chunker) SplitFile(filePath string) ([]*Chunk, error) {
	// Open file
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Get file size
	fileInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}
	fileSize := fileInfo.Size()

	// Calculate number of chunks
	numChunks := int(fileSize) / c.chunkSize
	if int(fileSize)%c.chunkSize != 0 {
		numChunks++ // Add one more chunk for remainder
	}

	chunks := make([]*Chunk, 0, numChunks)

	// Read and process each chunk
	buffer := make([]byte, c.chunkSize)
	offset := int64(0)

	for i := 0; i < numChunks; i++ {
		// Read chunk data
		n, err := file.Read(buffer)
		if err != nil && n == 0 {
			return nil, fmt.Errorf("failed to read chunk %d: %w", i, err)
		}

		// Get actual chunk data (might be less than chunkSize for last chunk)
		chunkData := buffer[:n]

		// Calculate CID for chunk
		chunkCID, err := crypto.GenerateCID(chunkData)
		if err != nil {
			return nil, fmt.Errorf("failed to generate CID for chunk %d: %w", i, err)
		}

		// Create chunk metadata
		chunk := &Chunk{
			Index:      i,
			CID:        chunkCID,
			Size:       n,
			Offset:     offset,
			SourceFile: filePath,
			Data:       nil, // Don't store data in memory by default
		}

		chunks = append(chunks, chunk)
		offset += int64(n)
	}

	return chunks, nil
}

// CalculateFileCID calculates file CID from chunk CIDs (Merkle root)
// This creates a deterministic identifier for the entire file
func (c *Chunker) CalculateFileCID(chunks []*Chunk) string {
	// Concatenate all chunk CIDs
	var concatenated []byte
	for _, chunk := range chunks {
		concatenated = append(concatenated, []byte(chunk.CID)...)
	}

	// Calculate CID of concatenated chunk CIDs (Merkle root)
	fileCID, err := crypto.GenerateCID(concatenated)
	if err != nil {
		// Should never happen with valid chunk CIDs
		return ""
	}

	return fileCID
}

// ReassembleFile reassembles chunks back into a complete file
// chunks: array of Chunk metadata (reads from SourceFile using Offset)
// outputPath: where to write the reassembled file
func (c *Chunker) ReassembleFile(chunks []*Chunk, outputPath string) error {
	// Create output file
	outFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer outFile.Close()

	// Determine source file from first chunk
	if len(chunks) == 0 {
		return fmt.Errorf("no chunks provided")
	}
	sourceFile := chunks[0].SourceFile
	if sourceFile == "" {
		return fmt.Errorf("chunks missing source file information")
	}

	// Open source file
	srcFile, err := os.Open(sourceFile)
	if err != nil {
		return fmt.Errorf("failed to open source file: %w", err)
	}
	defer srcFile.Close()

	// Write chunks in order
	for i := 0; i < len(chunks); i++ {
		// Find chunk with matching index
		var chunk *Chunk
		for _, c := range chunks {
			if c.Index == i {
				chunk = c
				break
			}
		}

		if chunk == nil {
			return fmt.Errorf("missing chunk %d", i)
		}

		// Read chunk data from source file
		buffer := make([]byte, chunk.Size)
		_, err := srcFile.ReadAt(buffer, chunk.Offset)
		if err != nil {
			return fmt.Errorf("failed to read chunk %d: %w", i, err)
		}

		// Write to output file
		_, err = outFile.Write(buffer)
		if err != nil {
			return fmt.Errorf("failed to write chunk %d: %w", i, err)
		}
	}

	return nil
}

// VerifyChunk verifies that chunk data matches the provided CID
// Returns true if valid, false if corrupted
func (c *Chunker) VerifyChunk(chunkCID string, data []byte) bool {
	valid, err := crypto.VerifyCID(chunkCID, data)
	if err != nil {
		return false
	}

	return valid
}
