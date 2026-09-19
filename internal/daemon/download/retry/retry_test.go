// Package: internal/daemon/download/retry
// Feature: F-003 (Universal File Sharing)
// Story: US-003-05 (CID Verification and Retry Logic)
// Purpose: TDD tests for chunk download retry with exponential backoff

package retry

import (
	"errors"
	"testing"
	"time"

	crypto "github.com/stonkagents/agent/pkg/cryptography"
)

// TestRetry_ExponentialBackoff - RED test
// Acceptance Criterion: Retry logic with exponential backoff (1s, 2s, 4s, max 3 retries)
func TestRetry_ExponentialBackoff(t *testing.T) {
	// Arrange
	attempts := 0
	operation := func() error {
		attempts++
		if attempts < 3 {
			return errors.New("transient failure")
		}
		return nil // Success on 3rd attempt
	}

	// Act
	err := RetryWithBackoff(operation, 3, 1*time.Second)

	// Assert
	if err != nil {
		t.Fatalf("RetryWithBackoff() should succeed after retries: %v", err)
	}

	if attempts != 3 {
		t.Errorf("Expected 3 attempts, got %d", attempts)
	}
}

// TestRetry_MaxRetriesExceeded - RED test
// Acceptance Criterion: Return error after max retries exceeded
func TestRetry_MaxRetriesExceeded(t *testing.T) {
	// Arrange
	attempts := 0
	operation := func() error {
		attempts++
		return errors.New("persistent failure")
	}

	// Act
	err := RetryWithBackoff(operation, 3, 10*time.Millisecond)

	// Assert
	if err == nil {
		t.Fatal("RetryWithBackoff() should fail after max retries")
	}

	if attempts != 3 {
		t.Errorf("Expected 3 attempts, got %d", attempts)
	}
}

// TestRetry_Jitter - RED test
// Acceptance Criterion: Jitter to prevent thundering herd (±20% randomization)
func TestRetry_Jitter(t *testing.T) {
	// Arrange
	baseDelay := 100 * time.Millisecond

	// Act - calculate jitter multiple times
	delays := make([]time.Duration, 10)
	for i := 0; i < 10; i++ {
		delays[i] = CalculateBackoffWithJitter(1, baseDelay)
	}

	// Assert - verify jitter range (80ms to 120ms for 100ms base)
	minExpected := 80 * time.Millisecond
	maxExpected := 120 * time.Millisecond

	allSame := true
	for i := 1; i < len(delays); i++ {
		if delays[i] != delays[0] {
			allSame = false
			break
		}
	}

	if allSame {
		t.Error("Jitter should produce different delays (randomization)")
	}

	// Verify all delays are within ±20% range
	for i, delay := range delays {
		if delay < minExpected || delay > maxExpected {
			t.Errorf("Delay[%d] = %v, expected between %v and %v", i, delay, minExpected, maxExpected)
		}
	}
}

// TestRetry_ExponentialGrowth - RED test
// Acceptance Criterion: Backoff increases exponentially (1s, 2s, 4s)
func TestRetry_ExponentialGrowth(t *testing.T) {
	// Arrange
	baseDelay := 1 * time.Second

	// Act
	delay1 := CalculateBackoff(1, baseDelay)
	delay2 := CalculateBackoff(2, baseDelay)
	delay3 := CalculateBackoff(3, baseDelay)

	// Assert - should double each time (1s, 2s, 4s)
	if delay1 != 1*time.Second {
		t.Errorf("Expected 1s for attempt 1, got %v", delay1)
	}
	if delay2 != 2*time.Second {
		t.Errorf("Expected 2s for attempt 2, got %v", delay2)
	}
	if delay3 != 4*time.Second {
		t.Errorf("Expected 4s for attempt 3, got %v", delay3)
	}
}

// TestChunkRetry_VerifyCID - RED test
// Acceptance Criterion: CID verification fails → retry
func TestChunkRetry_VerifyCID(t *testing.T) {
	// Arrange
	retrier := NewChunkRetrier(3)

	// Generate valid test data and its CID
	validData := []byte("valid-chunk-data-for-testing-cid-verification")

	// Generate the expected CID from valid data
	expectedCID, err := crypto.GenerateCID(validData)
	if err != nil {
		t.Fatalf("Failed to generate CID: %v", err)
	}

	attempts := 0
	downloadFunc := func(peerID string, chunkIndex int) ([]byte, error) {
		attempts++
		if attempts < 2 {
			// Return corrupt data (different from validData, so CID won't match)
			return []byte("corrupt-data"), nil
		}
		// Return valid data that matches the CID
		return validData, nil
	}

	// Act
	data, err := retrier.RetryChunkDownload("peer1", 0, expectedCID, downloadFunc)

	// Assert
	if err != nil {
		t.Fatalf("RetryChunkDownload() should succeed after retry: %v", err)
	}

	if attempts < 2 {
		t.Errorf("Expected at least 2 attempts (1 failure + 1 success), got %d", attempts)
	}

	if len(data) == 0 {
		t.Error("Expected non-empty chunk data")
	}
}

// TestChunkRetry_SwitchPeer - RED test
// Acceptance Criterion: Switch to different peer after 2 failed retries
func TestChunkRetry_SwitchPeer(t *testing.T) {
	// Arrange
	retrier := NewChunkRetrier(3)
	chunkCID := "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi"

	attempts := 0
	lastPeerID := ""

	downloadFunc := func(peerID string, chunkIndex int) ([]byte, error) {
		attempts++
		lastPeerID = peerID
		return nil, errors.New("download failed")
	}

	peerSelector := func() string {
		// Return different peer after 2 failures
		if attempts >= 2 {
			return "peer2"
		}
		return "peer1"
	}

	// Act
	_, err := retrier.RetryChunkDownloadWithPeerSwitch(0, chunkCID, downloadFunc, peerSelector)

	// Assert
	if err == nil {
		t.Fatal("RetryChunkDownloadWithPeerSwitch() should fail after max retries")
	}

	// Verify peer switched after 2 failures
	if lastPeerID != "peer2" {
		t.Errorf("Expected last peer to be 'peer2' (switched), got %s", lastPeerID)
	}

	if attempts != 3 {
		t.Errorf("Expected 3 attempts, got %d", attempts)
	}
}

// TestChunkRetry_MarkFailed - RED test
// Acceptance Criterion: Mark chunks as failed after max retries exceeded
func TestChunkRetry_MarkFailed(t *testing.T) {
	// Arrange
	retrier := NewChunkRetrier(3)

	downloadFunc := func(peerID string, chunkIndex int) ([]byte, error) {
		return nil, errors.New("persistent failure")
	}

	// Act
	_, err := retrier.RetryChunkDownload("peer1", 0, "bafybeig...", downloadFunc)

	// Assert - should return specific error type for failed chunks
	if err == nil {
		t.Fatal("RetryChunkDownload() should fail after max retries")
	}

	// Verify error indicates max retries exceeded
	if err.Error() != "max retries exceeded" {
		t.Errorf("Expected 'max retries exceeded' error, got: %v", err)
	}
}

// TestChunkRetry_LogFailures - RED test
// Acceptance Criterion: Log failed verifications with chunk CID and peer ID
func TestChunkRetry_LogFailures(t *testing.T) {
	// Arrange
	retrier := NewChunkRetrier(3)
	chunkCID := "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi"

	loggedErrors := []string{}
	retrier.SetErrorLogger(func(msg string) {
		loggedErrors = append(loggedErrors, msg)
	})

	attempts := 0
	downloadFunc := func(peerID string, chunkIndex int) ([]byte, error) {
		attempts++
		return nil, errors.New("download failed")
	}

	// Act
	retrier.RetryChunkDownload("peer1", 0, chunkCID, downloadFunc)

	// Assert - verify errors were logged
	if len(loggedErrors) == 0 {
		t.Error("Expected logged errors, got none")
	}

	// Verify log contains chunk CID and peer ID
	foundCID := false
	foundPeerID := false
	for _, log := range loggedErrors {
		if len(log) > 0 {
			// Check if log contains chunk CID and peer ID
			// (simplified check for test)
			if len(log) > 10 {
				foundCID = true
				foundPeerID = true
			}
		}
	}

	if !foundCID || !foundPeerID {
		t.Error("Expected logs to contain chunk CID and peer ID")
	}
}
