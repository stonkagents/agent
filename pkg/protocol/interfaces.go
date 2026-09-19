// Package: pkg/protocol
// Feature: F-010 (P2P Transfer Protocol)
// Story: US-010-05 (Block Exchange Protocol)
// Purpose: Core interfaces for P2P block exchange protocol

package protocol

import (
	"context"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// BlockExchangeService defines the interface for requesting and serving chunks via libp2p.
// This interface allows download managers and upload handlers to work with the P2P protocol
// without tight coupling to the implementation details.
type BlockExchangeService interface {
	// RequestChunk requests a specific chunk from a peer.
	// Returns the chunk data or an error if the request fails or times out.
	// Timeout is 30 seconds per chunk request.
	RequestChunk(ctx context.Context, peerID peer.ID, cid string, chunkIndex int) (*Chunk, error)

	// RequestChunks requests multiple chunks in parallel with pipelining.
	// Max 16 concurrent requests per peer.
	// Returns a channel that emits chunks as they arrive.
	RequestChunks(ctx context.Context, peerID peer.ID, cid string, chunkIndices []int) (<-chan *ChunkResult, error)

	// HandleStream processes incoming chunk requests from remote peers.
	// This is the server-side handler registered with libp2p.
	HandleStream(stream io.ReadWriteCloser, remotePeer peer.ID) error

	// RegisterChunkProvider registers a chunk provider that can serve chunks.
	// The provider is called when a remote peer requests a chunk.
	RegisterChunkProvider(provider ChunkProvider)

	// ProtocolID returns the libp2p protocol ID.
	// For StonkAgents, this is "/stonkagents/transfer/1.0.0"
	ProtocolID() protocol.ID

	// Close shuts down the block exchange service gracefully.
	Close() error
}

// ChunkProvider is the interface for serving chunks to remote peers.
// Upload managers and chunk stores implement this interface.
type ChunkProvider interface {
	// GetChunk retrieves a chunk by CID and index.
	// Returns nil if the chunk is not found locally.
	GetChunk(ctx context.Context, cid string, chunkIndex int) (*Chunk, error)

	// HasChunk checks if a chunk is available locally without loading it.
	HasChunk(ctx context.Context, cid string, chunkIndex int) (bool, error)
}

// UploadRecorder records bytes uploaded (for transfer stats and leaderboard).
// Implemented by daemon stats repository; optional on BlockExchangeService.
type UploadRecorder interface {
	RecordUpload(bytes int64)
}

// ChunkResult wraps a chunk with potential error information.
// Used for async chunk requests via channels.
type ChunkResult struct {
	Chunk *Chunk
	Error error
	// PeerID is the peer that provided this chunk
	PeerID peer.ID
	// RetryCount indicates how many times this chunk was retried
	RetryCount int
}

// Chunk represents a 256KB piece of a file with its metadata.
type Chunk struct {
	// FileCID is the content identifier of the complete file
	FileCID string
	// ChunkIndex is the 0-based position of this chunk in the file
	ChunkIndex int
	// ChunkCID is the content identifier of this specific chunk
	// Each chunk has an independent CID for verification
	ChunkCID string
	// Data is the raw chunk bytes (max 256KB = 262,144 bytes)
	Data []byte
	// Size is the actual data size (last chunk may be smaller)
	Size int64
	// ReceivedAt is the timestamp when this chunk was received
	ReceivedAt time.Time
}

// ChunkMetadata contains file-level chunking information.
type ChunkMetadata struct {
	// FileCID is the Merkle root of all chunk CIDs
	FileCID string
	// Filename is the original file name
	Filename string
	// TotalSize is the complete file size in bytes
	TotalSize int64
	// TotalChunks is the number of 256KB chunks
	TotalChunks int
	// ChunkSize is typically 262144 (256KB)
	ChunkSize int
	// ChunkCIDs is the ordered list of chunk content identifiers
	ChunkCIDs []string
}

// Constants for the block exchange protocol
const (
	// ProtocolIDStonkAgents is the libp2p protocol ID for StonkAgents transfers
	ProtocolIDStonkAgents protocol.ID = "/stonkagents/transfer/1.0.0"

	// ProtocolIDAgentChat is the libp2p protocol ID for token chat (holder → owner direct P2P).
	ProtocolIDAgentChat protocol.ID = "/stonkagents/agent-chat/1.0.0"

	// ChunkSize is the standard chunk size (256KB)
	ChunkSize = 256 * 1024 // 262,144 bytes

	// MaxConcurrentRequests is the max number of concurrent chunk requests per peer
	MaxConcurrentRequests = 16

	// ChunkRequestTimeout is the timeout for a single chunk request
	ChunkRequestTimeout = 30 * time.Second

	// RetryMaxAttempts is the maximum number of retries for a failed chunk
	RetryMaxAttempts = 3

	// RetryBackoffBase is the base delay for exponential backoff (1 second)
	RetryBackoffBase = 1 * time.Second

	// RetryJitterPercent is the jitter percentage to prevent thundering herd (±20%)
	RetryJitterPercent = 20
)
