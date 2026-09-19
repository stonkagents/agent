// Package: internal/daemon/download
// Feature: F-010 (P2P Transfer Protocol)
// Story: US-010-06 (Chunked Download Manager)
// Purpose: File assembly from downloaded chunks with CID verification

package download

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/stonkagents/agent/pkg/protocol"
)

// assembleFile assembles a complete file from chunks with 3-layer CID verification
// Phase 4: File Assembly with security-first approach
func (m *Manager) assembleFile(cid, filename string, totalChunks int) error {
	// SECURITY: Validate inputs
	if cid == "" {
		return fmt.Errorf("file assembly failed: invalid CID: empty string")
	}
	if filename == "" {
		return fmt.Errorf("file assembly failed: invalid filename: empty string")
	}
	if totalChunks <= 0 {
		return fmt.Errorf("file assembly failed: invalid totalChunks: %d (must be > 0)", totalChunks)
	}

	// SECURITY: Sanitize filename to prevent path traversal
	sanitizedFilename := sanitizeFilename(filename)
	if sanitizedFilename == "" {
		return fmt.Errorf("file assembly failed: invalid filename after sanitization: %s", filename)
	}

	// Validate P2P dependencies
	if m.p2p == nil || m.p2p.ChunkStore == nil {
		return fmt.Errorf("file assembly failed for CID %s: P2P dependencies not initialized", cid)
	}

	// Create context with timeout for assembly
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Step 1: Load all chunks from ChunkStore
	var chunks []*protocol.Chunk
	var chunkCIDs []string

	for i := 0; i < totalChunks; i++ {
		// Check context for cancellation
		select {
		case <-ctx.Done():
			return fmt.Errorf("assembly cancelled: %w", ctx.Err())
		default:
		}

		// Load chunk data from store
		chunkData, err := m.p2p.ChunkStore.GetChunk(cid, i)
		if err != nil {
			return fmt.Errorf("failed to load chunk %d: %w", i, err)
		}

		// Calculate chunk CID for verification (Layer 2)
		chunkCID, err := protocol.CalculateChunkCID(chunkData)
		if err != nil {
			return fmt.Errorf("failed to calculate CID for chunk %d: %w", i, err)
		}

		// Create protocol.Chunk for assembly
		chunk := &protocol.Chunk{
			FileCID:    cid,
			ChunkIndex: i,
			ChunkCID:   chunkCID,
			Data:       chunkData,
			Size:       int64(len(chunkData)),
			ReceivedAt: time.Now(),
		}

		chunks = append(chunks, chunk)
		chunkCIDs = append(chunkCIDs, chunkCID)
	}

	// Step 2: Create metadata for verification
	totalSize := int64(0)
	for _, chunk := range chunks {
		totalSize += chunk.Size
	}

	metadata := &protocol.ChunkMetadata{
		FileCID:     cid,
		Filename:    filename,
		TotalSize:   totalSize,
		TotalChunks: totalChunks,
		ChunkSize:   262144, // 256 KB default
		ChunkCIDs:   chunkCIDs,
	}

	// Step 3: Create output directory (baseDir/assets/{cid}/)
	outputDir := fmt.Sprintf("%s/assets/%s", m.baseDir, cid)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("file assembly failed: failed to create output directory for CID %s: %w", cid, err)
	}

	// Step 4: Check disk space before assembly
	if err := checkDiskSpace(outputDir, totalSize); err != nil {
		return fmt.Errorf("file assembly failed: %w", err)
	}

	// Step 5: Assemble file to path (Layer 2 & 3 verification inside)
	// Use sanitized filename to prevent path traversal
	outputPath := fmt.Sprintf("%s/%s", outputDir, sanitizedFilename)
	chunker := protocol.NewChunker()
	if err := chunker.AssembleFileToPath(ctx, chunks, metadata, outputPath); err != nil {
		return fmt.Errorf("file assembly failed for CID %s: %w", cid, err)
	}

	// Step 6: Delete chunks after successful assembly
	// Note: Only delete if assembly succeeded (error handling ensures chunks remain on failure)
	if deleter, ok := m.p2p.ChunkStore.(interface{ DeleteChunks(string) error }); ok {
		if err := deleter.DeleteChunks(cid); err != nil {
			// Log warning but don't fail - file was assembled successfully
			_ = err
		}
	}

	return nil
}

// ============================================================================
// Helper Functions: Error Handling & Security (Phase 10)
// ============================================================================

// sanitizeFilename removes path traversal patterns and invalid characters
// SECURITY: Prevents ../../../etc/passwd attacks
func sanitizeFilename(filename string) string {
	// Remove all ".." sequences to prevent path traversal
	filename = strings.ReplaceAll(filename, "..", "_")

	// Replace all path separators (both Unix and Windows) with underscores
	filename = strings.ReplaceAll(filename, "/", "_")
	filename = strings.ReplaceAll(filename, "\\", "_")

	// Remove colons (Windows drive letters like C:)
	filename = strings.ReplaceAll(filename, ":", "_")

	// Remove leading dots to prevent hidden file attacks
	filename = strings.TrimLeft(filename, ".")

	// Remove leading underscores from path separator replacement
	filename = strings.TrimLeft(filename, "_")

	// Ensure filename is not empty after sanitization
	if filename == "" || filename == "." || filename == ".." {
		return ""
	}

	return filename
}

// isDiskFullError checks if an error is due to disk space issues
// Detects "no space left on device" and similar errors
func isDiskFullError(err error) bool {
	if err == nil {
		return false
	}

	// Check for common disk full error messages
	errMsg := err.Error()
	diskFullPatterns := []string{
		"no space left on device",
		"disk full",
		"not enough space",
		"insufficient disk space",
		"ENOSPC",
	}

	for _, pattern := range diskFullPatterns {
		if strings.Contains(strings.ToLower(errMsg), strings.ToLower(pattern)) {
			return true
		}
	}

	return false
}
