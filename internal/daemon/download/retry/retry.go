// Package: internal/daemon/download/retry
// Feature: F-003 (Universal File Sharing)
// Story: US-003-05 (CID Verification and Retry Logic)
// Purpose: Chunk download retry with exponential backoff and CID verification

package retry

import (
	"errors"
	"fmt"
	"math/rand"
	"time"

	crypto "github.com/stonkagents/agent/pkg/cryptography"
)

// ChunkRetrier manages retry logic for chunk downloads with CID verification
type ChunkRetrier struct {
	maxRetries  int
	baseDelay   time.Duration
	errorLogger func(string)
}

// DownloadFunc is a function that downloads a chunk from a peer
type DownloadFunc func(peerID string, chunkIndex int) ([]byte, error)

// PeerSelectorFunc is a function that selects the next peer to try
type PeerSelectorFunc func() string

// NewChunkRetrier creates a new chunk retrier
func NewChunkRetrier(maxRetries int) *ChunkRetrier {
	return &ChunkRetrier{
		maxRetries:  maxRetries,
		baseDelay:   1 * time.Second, // Default base delay
		errorLogger: nil,
	}
}

// SetErrorLogger sets a custom error logger
func (r *ChunkRetrier) SetErrorLogger(logger func(string)) {
	r.errorLogger = logger
}

// logError logs an error if a logger is configured
func (r *ChunkRetrier) logError(msg string) {
	if r.errorLogger != nil {
		r.errorLogger(msg)
	}
}

// RetryWithBackoff executes an operation with exponential backoff
func RetryWithBackoff(operation func() error, maxRetries int, baseDelay time.Duration) error {
	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		// Execute operation
		err := operation()
		if err == nil {
			return nil // Success
		}

		lastErr = err

		// If not last attempt, wait with exponential backoff
		if attempt < maxRetries {
			delay := CalculateBackoff(attempt, baseDelay)
			time.Sleep(delay)
		}
	}

	return lastErr
}

// CalculateBackoff calculates exponential backoff delay without jitter
func CalculateBackoff(attempt int, baseDelay time.Duration) time.Duration {
	// Exponential: 1s, 2s, 4s, 8s, ...
	multiplier := 1 << (attempt - 1) // 2^(attempt-1)
	return time.Duration(multiplier) * baseDelay
}

// CalculateBackoffWithJitter calculates exponential backoff with ±20% jitter
func CalculateBackoffWithJitter(attempt int, baseDelay time.Duration) time.Duration {
	// Base exponential delay
	delay := CalculateBackoff(attempt, baseDelay)

	// Add ±20% jitter
	jitterRange := float64(delay) * 0.2
	jitter := (rand.Float64() * 2 * jitterRange) - jitterRange // Random value in [-20%, +20%]

	finalDelay := time.Duration(float64(delay) + jitter)

	// Ensure non-negative
	if finalDelay < 0 {
		finalDelay = delay
	}

	return finalDelay
}

// RetryChunkDownload retries a chunk download with CID verification
func (r *ChunkRetrier) RetryChunkDownload(
	peerID string,
	chunkIndex int,
	expectedCID string,
	downloadFunc DownloadFunc,
) ([]byte, error) {
	var lastErr error

	for attempt := 1; attempt <= r.maxRetries; attempt++ {
		// Download chunk
		data, err := downloadFunc(peerID, chunkIndex)
		if err != nil {
			lastErr = err
			r.logError(fmt.Sprintf("Chunk download failed (attempt %d/%d): peer=%s, chunk=%d, cid=%s, error=%v",
				attempt, r.maxRetries, peerID, chunkIndex, expectedCID, err))

			// Wait before retry (except on last attempt)
			if attempt < r.maxRetries {
				delay := CalculateBackoffWithJitter(attempt, r.baseDelay)
				time.Sleep(delay)
			}
			continue
		}

		// Verify CID
		valid, err := crypto.VerifyCID(expectedCID, data)
		if err != nil {
			lastErr = fmt.Errorf("CID verification error: %w", err)
			r.logError(fmt.Sprintf("CID verification error (attempt %d/%d): peer=%s, chunk=%d, expected=%s, error=%v",
				attempt, r.maxRetries, peerID, chunkIndex, expectedCID, err))

			// Wait before retry
			if attempt < r.maxRetries {
				delay := CalculateBackoffWithJitter(attempt, r.baseDelay)
				time.Sleep(delay)
			}
			continue
		}

		if !valid {
			// CID verification failed
			lastErr = fmt.Errorf("CID verification failed")
			r.logError(fmt.Sprintf("CID verification failed (attempt %d/%d): peer=%s, chunk=%d, expected=%s",
				attempt, r.maxRetries, peerID, chunkIndex, expectedCID))

			// Wait before retry
			if attempt < r.maxRetries {
				delay := CalculateBackoffWithJitter(attempt, r.baseDelay)
				time.Sleep(delay)
			}
			continue
		}

		// CID verified successfully
		return data, nil
	}

	// Max retries exceeded
	if lastErr == nil {
		lastErr = errors.New("max retries exceeded")
	}

	return nil, errors.New("max retries exceeded")
}

// RetryChunkDownloadWithPeerSwitch retries with ability to switch peers after failures
func (r *ChunkRetrier) RetryChunkDownloadWithPeerSwitch(
	chunkIndex int,
	expectedCID string,
	downloadFunc DownloadFunc,
	peerSelector PeerSelectorFunc,
) ([]byte, error) {
	for attempt := 1; attempt <= r.maxRetries; attempt++ {
		// Select peer (may switch after 2 failures)
		peerID := peerSelector()

		// Download chunk
		data, err := downloadFunc(peerID, chunkIndex)
		if err != nil {
			r.logError(fmt.Sprintf("Chunk download failed (attempt %d/%d): peer=%s, chunk=%d, cid=%s, error=%v",
				attempt, r.maxRetries, peerID, chunkIndex, expectedCID, err))

			// Wait before retry (except on last attempt)
			if attempt < r.maxRetries {
				delay := CalculateBackoffWithJitter(attempt, r.baseDelay)
				time.Sleep(delay)
			}
			continue
		}

		// Verify CID
		valid, err := crypto.VerifyCID(expectedCID, data)
		if err != nil {
			r.logError(fmt.Sprintf("CID verification error (attempt %d/%d): peer=%s, chunk=%d, expected=%s, error=%v",
				attempt, r.maxRetries, peerID, chunkIndex, expectedCID, err))

			// Wait before retry
			if attempt < r.maxRetries {
				delay := CalculateBackoffWithJitter(attempt, r.baseDelay)
				time.Sleep(delay)
			}
			continue
		}

		if !valid {
			// CID verification failed
			r.logError(fmt.Sprintf("CID verification failed (attempt %d/%d): peer=%s, chunk=%d, expected=%s",
				attempt, r.maxRetries, peerID, chunkIndex, expectedCID))

			// Wait before retry
			if attempt < r.maxRetries {
				delay := CalculateBackoffWithJitter(attempt, r.baseDelay)
				time.Sleep(delay)
			}
			continue
		}

		// CID verified successfully
		return data, nil
	}

	// Max retries exceeded
	return nil, errors.New("max retries exceeded")
}
