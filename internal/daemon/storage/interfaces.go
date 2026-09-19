// Package: internal/daemon/storage
// Feature: F-010 (P2P Transfer Protocol)
// Story: US-010-07 (Upload Queue & Seeding)
// Purpose: Storage interfaces for chunk persistence

package storage

import (
	"context"
	"io"

	"github.com/stonkagents/agent/pkg/protocol"
)

// ChunkStore is the interface for persisting and retrieving file chunks.
// Both upload (seeding) and download managers use this interface.
// SQLite implementation for production, in-memory for testing.
type ChunkStore interface {
	// StoreChunk persists a single chunk to storage.
	// Returns an error if the write fails or storage is full.
	StoreChunk(ctx context.Context, chunk *protocol.Chunk) error

	// StoreChunks persists multiple chunks in a batch operation.
	// More efficient than calling StoreChunk repeatedly.
	StoreChunks(ctx context.Context, chunks []*protocol.Chunk) error

	// GetChunk retrieves a chunk by file CID and chunk index.
	// Returns nil if the chunk is not found.
	GetChunk(ctx context.Context, fileCID string, chunkIndex int) (*protocol.Chunk, error)

	// GetChunks retrieves multiple chunks efficiently.
	// Returns a map of chunkIndex -> Chunk. Missing chunks are omitted.
	GetChunks(ctx context.Context, fileCID string, chunkIndices []int) (map[int]*protocol.Chunk, error)

	// HasChunk checks if a chunk exists without loading it into memory.
	// Useful for quick availability checks.
	HasChunk(ctx context.Context, fileCID string, chunkIndex int) (bool, error)

	// DeleteChunk removes a single chunk from storage.
	DeleteChunk(ctx context.Context, fileCID string, chunkIndex int) error

	// DeleteFile removes all chunks for a given file CID.
	// Used after successful file assembly or for cleanup.
	DeleteFile(ctx context.Context, fileCID string) error

	// ListFiles returns all file CIDs stored in the chunk store.
	ListFiles(ctx context.Context) ([]string, error)

	// GetFileMetadata retrieves metadata for a complete file.
	GetFileMetadata(ctx context.Context, fileCID string) (*FileMetadata, error)

	// StoreFileMetadata persists file-level metadata.
	// Includes filename, size, chunk count, chunk CIDs.
	StoreFileMetadata(ctx context.Context, metadata *FileMetadata) error

	// Close releases resources and closes database connections.
	Close() error
}

// FileMetadata contains information about a complete file stored in chunks.
type FileMetadata struct {
	// FileCID is the Merkle root of all chunk CIDs
	FileCID string
	// Filename is the original filename
	Filename string
	// TotalSize is the complete file size in bytes
	TotalSize int64
	// TotalChunks is the number of 256KB chunks
	TotalChunks int
	// ChunkSize is typically 262144 (256KB)
	ChunkSize int
	// ChunkCIDs is the ordered list of chunk content identifiers
	ChunkCIDs []string
	// ManifestData is the serialized Protocol Buffer manifest (optional)
	ManifestData []byte
}

// ChunkStoreStats provides statistics about chunk storage.
type ChunkStoreStats struct {
	// TotalFiles is the number of distinct files stored
	TotalFiles int
	// TotalChunks is the total number of chunks across all files
	TotalChunks int
	// TotalBytes is the total storage used in bytes
	TotalBytes int64
	// AvailableBytes is the remaining storage capacity (0 = unlimited)
	AvailableBytes int64
}

// Chunker is the interface for splitting files into chunks.
// Used by the share flow to prepare files for P2P distribution.
type Chunker interface {
	// ChunkFile splits a file into 256KB chunks and returns metadata.
	// The file is read from the provided reader.
	ChunkFile(ctx context.Context, reader io.Reader, filename string) (*protocol.ChunkMetadata, []*protocol.Chunk, error)

	// ChunkFileFromPath is a convenience method that opens a file and chunks it.
	ChunkFileFromPath(ctx context.Context, filepath string) (*protocol.ChunkMetadata, []*protocol.Chunk, error)

	// AssembleFile reconstructs a complete file from chunks.
	// Verifies each chunk CID and the final file CID.
	// Writes the result to the provided writer.
	AssembleFile(ctx context.Context, chunks []*protocol.Chunk, metadata *protocol.ChunkMetadata, writer io.Writer) error

	// AssembleFileToPath is a convenience method that creates a file and writes to it.
	AssembleFileToPath(ctx context.Context, chunks []*protocol.Chunk, metadata *protocol.ChunkMetadata, outputPath string) error

	// VerifyChunk verifies a chunk's CID matches its data.
	// Returns an error if verification fails.
	VerifyChunk(ctx context.Context, chunk *protocol.Chunk) error

	// VerifyFile verifies all chunks and the final file CID.
	// Returns an error if any verification fails.
	VerifyFile(ctx context.Context, chunks []*protocol.Chunk, metadata *protocol.ChunkMetadata) error
}

// DownloadState represents the state of a download.
type DownloadState string

const (
	// DownloadStateQueued means download is waiting to start
	DownloadStateQueued DownloadState = "queued"
	// DownloadStateActive means download is currently transferring
	DownloadStateActive DownloadState = "active"
	// DownloadStatePaused means download is temporarily stopped
	DownloadStatePaused DownloadState = "paused"
	// DownloadStateCompleted means download finished successfully
	DownloadStateCompleted DownloadState = "completed"
	// DownloadStateFailed means download encountered unrecoverable error
	DownloadStateFailed DownloadState = "failed"
)

// DownloadMetadata tracks the state of an in-progress download.
type DownloadMetadata struct {
	// FileCID is the content identifier being downloaded
	FileCID string
	// Filename is the target filename
	Filename string
	// TotalSize is the complete file size
	TotalSize int64
	// TotalChunks is the number of chunks to download
	TotalChunks int
	// CompletedChunks is a bitmap of completed chunk indices
	CompletedChunks []bool
	// State is the current download state
	State DownloadState
	// Peers is the list of peer IDs providing chunks
	Peers []string
	// StartedAt is when the download began
	StartedAt int64 // Unix timestamp
	// CompletedAt is when the download finished (if completed)
	CompletedAt int64 // Unix timestamp
	// BytesDownloaded is the total bytes received so far
	BytesDownloaded int64
	// LastError is the most recent error message (if failed)
	LastError string
}

// DownloadRepository manages download metadata persistence.
// Used by download manager to track resume state across daemon restarts.
type DownloadRepository interface {
	// SaveDownloadMetadata persists download state to disk.
	SaveDownloadMetadata(ctx context.Context, metadata *DownloadMetadata) error

	// GetDownloadMetadata retrieves download state by file CID.
	GetDownloadMetadata(ctx context.Context, fileCID string) (*DownloadMetadata, error)

	// ListDownloads returns all downloads (optionally filtered by state).
	ListDownloads(ctx context.Context, state *DownloadState) ([]*DownloadMetadata, error)

	// DeleteDownloadMetadata removes download tracking data.
	DeleteDownloadMetadata(ctx context.Context, fileCID string) error

	// Close releases resources.
	Close() error
}
