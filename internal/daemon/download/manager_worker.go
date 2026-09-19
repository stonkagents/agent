// Package: internal/daemon/download
// Feature: F-029 (Transfer Page E2E)
// Story: US-029-21 (Worker Lifecycle Redesign)
// Purpose: Per-download worker goroutines with context cancellation

package download

import (
	"context"
	"errors"
	"time"
)

// StartWorker starts the background coordinator that spawns per-download goroutines.
// The coordinator only launches workers — it does NOT execute downloads itself.
func (m *Manager) StartWorker() {
	m.workerDone = make(chan struct{})

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-m.workerDone:
				return
			case <-ticker.C:
				m.processQueuedDownloads()
			}
		}
	}()
}

// StopWorker stops the coordinator, cancels all active worker goroutines, and waits for exit.
// Safe to call multiple times (uses sync.Once internally).
func (m *Manager) StopWorker() {
	m.stopOnce.Do(func() {
		if m.workerDone != nil {
			close(m.workerDone)
		}
		// Cancel all active per-download worker contexts
		m.mu.Lock()
		for cid, wh := range m.workers {
			wh.cancel(errUserCancel)
			delete(m.workers, cid)
		}
		m.mu.Unlock()
	})
	m.wg.Wait()
}

// processQueuedDownloads finds the next queued download and spawns a worker goroutine.
// The coordinator ONLY launches workers — it does NOT execute downloads itself.
// Each worker owns its full lifecycle: state transition → execute → complete/fail.
func (m *Manager) processQueuedDownloads() {
	m.mu.Lock()

	if m.p2p == nil {
		m.mu.Unlock()
		return // P2P not initialized
	}

	// Find next queued download that doesn't already have a worker
	var nextCID string
	var nextStatus *DownloadStatus

	for cid, status := range m.downloads {
		if status.State == StateQueued {
			if _, hasWorker := m.workers[cid]; hasWorker {
				continue // Already has a running worker — skip
			}
			activeCount := m.countActiveDownloadsLocked()
			if activeCount < m.maxConcurrent {
				nextCID = cid
				nextStatus = status
				break
			}
		}
	}

	if nextCID == "" {
		m.mu.Unlock()
		return
	}

	// Transition to active state
	nextStatus.State = StateActive

	// Update persisted metadata
	metadata, err := m.repo.LoadMetadata(nextCID)
	if err != nil {
		m.mu.Unlock()
		return
	}
	metadata.State = StateActive
	_ = m.repo.SaveMetadata(metadata)

	// Create cancellable context for this download
	ctx, cancel := context.WithCancelCause(context.Background())
	done := make(chan struct{})
	m.workers[nextCID] = &workerHandle{cancel: cancel, done: done}

	// Capture values for goroutine (avoid closure over loop variables)
	cid := nextCID
	filename := nextStatus.Filename
	totalChunks := nextStatus.TotalChunks

	m.mu.Unlock()

	// Spawn per-download worker goroutine
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer close(done)
		m.runDownloadWorker(ctx, cid, filename, totalChunks)
	}()
}

// runDownloadWorker executes a single download and handles completion/failure/cancellation.
// This runs in its own goroutine — one per active download.
func (m *Manager) runDownloadWorker(ctx context.Context, cid, filename string, totalChunks int) {
	// Execute P2P download with context (respects cancel causes)
	err := m.executeDownload(ctx, cid, filename, totalChunks)

	// Check cancel cause for typed handling
	if err != nil {
		cause := context.Cause(ctx)
		switch {
		case errors.Is(cause, errUserPause):
			// Worker exits — PauseDownload already set state to paused
			return
		case errors.Is(cause, errUserCancel):
			// Worker exits — CancelDownload handles cleanup (Task 22)
			return
		case errors.Is(cause, errInactivityTimeout):
			m.FailDownload(cid, "Download timed out due to inactivity")
		default:
			m.FailDownload(cid, err.Error())
		}

		// Clean up worker handle on failure
		m.mu.Lock()
		delete(m.workers, cid)
		m.mu.Unlock()
		return
	}

	m.CompleteDownload(cid)

	// Clean up worker handle on success
	m.mu.Lock()
	delete(m.workers, cid)
	m.mu.Unlock()
}
