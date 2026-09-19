// Package: internal/daemon/download
// Feature: F-010 (P2P Transfer Protocol)
// Story: US-010-06 (Chunked Download Manager)
// Purpose: Download repository for metadata persistence

package download

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// DownloadState represents the state of a download
type DownloadState string

const (
	StateQueued    DownloadState = "queued"
	StateActive    DownloadState = "active"
	StatePaused    DownloadState = "paused"
	StateCompleted DownloadState = "completed"
	StateFailed    DownloadState = "failed"
)

// DownloadMetadata stores persistent metadata for a download
type DownloadMetadata struct {
	CID         string        `json:"cid"`
	Filename    string        `json:"filename"`
	TotalSize   int64         `json:"total_size"`
	TotalChunks int           `json:"total_chunks"`
	ChunkSize   int           `json:"chunk_size"`
	State       DownloadState `json:"state"`
	ChunksMap   []bool        `json:"chunks_map"` // Chunk completion bitmap
}

// Repository manages download metadata persistence
type Repository struct {
	baseDir string

	// Per-download locking: prevents load-modify-save race conditions
	mu            sync.Mutex
	downloadLocks map[string]*sync.Mutex
}

// NewRepository creates a new download repository
func NewRepository(baseDir string) *Repository {
	return &Repository{
		baseDir:       baseDir,
		downloadLocks: make(map[string]*sync.Mutex),
	}
}

// getDownloadLock returns the mutex for a specific download (creates if needed)
func (r *Repository) getDownloadLock(cid string) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()

	lock, exists := r.downloadLocks[cid]
	if !exists {
		lock = &sync.Mutex{}
		r.downloadLocks[cid] = lock
	}

	return lock
}

// CleanupDownloadLock removes the lock for a download from the map.
// Call this when download completes or is cancelled to prevent memory leak.
func (r *Repository) CleanupDownloadLock(cid string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.downloadLocks, cid)
}

// UpdateMetadata atomically loads, modifies, and saves metadata.
// This prevents load-modify-save race conditions from concurrent workers.
func (r *Repository) UpdateMetadata(cid string, updateFn func(*DownloadMetadata) error) error {
	lock := r.getDownloadLock(cid)
	lock.Lock()
	defer lock.Unlock()

	metadata, err := r.LoadMetadata(cid)
	if err != nil {
		return err
	}

	if err := updateFn(metadata); err != nil {
		return err
	}

	return r.SaveMetadata(metadata)
}

// SaveMetadata saves download metadata to disk
// Storage layout: baseDir/{cid}/metadata.json
func (r *Repository) SaveMetadata(metadata *DownloadMetadata) error {
	// Create download directory
	downloadDir := filepath.Join(r.baseDir, metadata.CID)
	if err := os.MkdirAll(downloadDir, 0755); err != nil {
		return fmt.Errorf("failed to create download directory: %w", err)
	}

	// Marshal metadata to JSON
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	// Write metadata.json
	metadataPath := filepath.Join(downloadDir, "metadata.json")
	if err := os.WriteFile(metadataPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write metadata: %w", err)
	}

	return nil
}

// LoadMetadata loads download metadata from disk
func (r *Repository) LoadMetadata(cid string) (*DownloadMetadata, error) {
	metadataPath := filepath.Join(r.baseDir, cid, "metadata.json")

	// Read metadata.json
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("metadata not found for CID %s", cid)
		}
		return nil, fmt.Errorf("failed to read metadata: %w", err)
	}

	// Unmarshal JSON
	var metadata DownloadMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("failed to unmarshal metadata: %w", err)
	}

	return &metadata, nil
}

// UpdateChunkCompletion updates the completion status of a specific chunk.
// Thread-safe: uses per-download locking via UpdateMetadata.
func (r *Repository) UpdateChunkCompletion(cid string, chunkIndex int, completed bool) error {
	return r.UpdateMetadata(cid, func(metadata *DownloadMetadata) error {
		// Validate chunk index
		if chunkIndex < 0 || chunkIndex >= len(metadata.ChunksMap) {
			return fmt.Errorf("invalid chunk index: %d (total chunks: %d)", chunkIndex, len(metadata.ChunksMap))
		}
		metadata.ChunksMap[chunkIndex] = completed
		return nil
	})
}

// ListIncomplete returns all downloads that are not completed (queued, active, paused)
// This is used to resume downloads on daemon startup
func (r *Repository) ListIncomplete() ([]*DownloadMetadata, error) {
	// Read all download directories
	entries, err := os.ReadDir(r.baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			// Base directory doesn't exist yet - no downloads
			return []*DownloadMetadata{}, nil
		}
		return nil, fmt.Errorf("failed to read download directory: %w", err)
	}

	var incomplete []*DownloadMetadata

	// Load metadata for each download
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		cid := entry.Name()
		metadata, err := r.LoadMetadata(cid)
		if err != nil {
			// Skip downloads with invalid metadata
			continue
		}

		// Include only incomplete downloads (not completed or failed)
		if metadata.State != StateCompleted && metadata.State != StateFailed {
			incomplete = append(incomplete, metadata)
		}
	}

	return incomplete, nil
}

// GetProgress calculates the download progress as a percentage (0.0 to 1.0)
func (r *Repository) GetProgress(cid string) (float64, error) {
	metadata, err := r.LoadMetadata(cid)
	if err != nil {
		return 0, fmt.Errorf("failed to load metadata: %w", err)
	}

	if metadata.TotalChunks == 0 {
		return 0, nil
	}

	completedChunks := 0
	for _, completed := range metadata.ChunksMap {
		if completed {
			completedChunks++
		}
	}

	return float64(completedChunks) / float64(metadata.TotalChunks), nil
}

// Delete removes download metadata from disk
// This is called after successful file assembly or cancel cleanup.
// SECURITY: filepath.Base() defense-in-depth against path traversal.
func (r *Repository) Delete(cid string) error {
	downloadDir := filepath.Join(r.baseDir, filepath.Base(cid))

	// Remove entire download directory
	if err := os.RemoveAll(downloadDir); err != nil {
		return fmt.Errorf("failed to delete download directory: %w", err)
	}

	return nil
}
