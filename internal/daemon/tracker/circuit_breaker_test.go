// Package: internal/daemon/tracker
// Feature: F-010 (P2P Transfer Protocol)
// Story: US-010-B3 (Circuit Breaker for Tracker Calls)
// Purpose: TDD tests for circuit breaker - stops hammering dead tracker

package tracker

import (
	"testing"
	"time"
)

// TestCircuitBreaker_StartsInClosedState - RED test
// Acceptance Criterion: New circuit breaker starts in closed (allowing) state
func TestCircuitBreaker_StartsInClosedState(t *testing.T) {
	// Arrange
	cb := NewCircuitBreaker()

	// Assert
	if cb.State() != StateClosed {
		t.Errorf("Expected initial state to be StateClosed, got %v", cb.State())
	}

	// Closed state should allow requests
	err := cb.Allow()
	if err != nil {
		t.Errorf("Expected Allow() to return nil in closed state, got %v", err)
	}
}

// TestCircuitBreaker_OpensAfterConsecutiveFailures - RED test
// Acceptance Criterion: Trip after 5 consecutive failures
func TestCircuitBreaker_OpensAfterConsecutiveFailures(t *testing.T) {
	// Arrange
	cb := NewCircuitBreaker()

	// Act - record 4 failures (should still be closed)
	for i := 0; i < DefaultFailureThreshold-1; i++ {
		cb.RecordFailure()
	}

	if cb.State() != StateClosed {
		t.Errorf("Expected StateClosed after %d failures, got %v", DefaultFailureThreshold-1, cb.State())
	}

	// 5th failure should trip the breaker
	cb.RecordFailure()

	// Assert
	if cb.State() != StateOpen {
		t.Errorf("Expected StateOpen after %d failures, got %v", DefaultFailureThreshold, cb.State())
	}
}

// TestCircuitBreaker_RejectsInOpenState - RED test
// Acceptance Criterion: Open circuit returns ErrCircuitOpen
func TestCircuitBreaker_RejectsInOpenState(t *testing.T) {
	// Arrange - trip the breaker
	cb := NewCircuitBreaker()
	for i := 0; i < DefaultFailureThreshold; i++ {
		cb.RecordFailure()
	}

	// Act
	err := cb.Allow()

	// Assert
	if err != ErrCircuitOpen {
		t.Errorf("Expected ErrCircuitOpen in open state, got %v", err)
	}
}

// TestCircuitBreaker_TransitionsToHalfOpen - RED test
// Acceptance Criterion: After reset timeout (30s), transitions to half-open
func TestCircuitBreaker_TransitionsToHalfOpen(t *testing.T) {
	// Arrange - use short timeout for testing
	cb := NewCircuitBreakerWithConfig(DefaultFailureThreshold, 50*time.Millisecond)

	// Trip the breaker
	for i := 0; i < DefaultFailureThreshold; i++ {
		cb.RecordFailure()
	}

	if cb.State() != StateOpen {
		t.Fatalf("Expected StateOpen, got %v", cb.State())
	}

	// Wait for reset timeout to elapse
	time.Sleep(100 * time.Millisecond)

	// Act - Allow() should transition to half-open and permit the request
	err := cb.Allow()

	// Assert
	if err != nil {
		t.Errorf("Expected Allow() to succeed in half-open state, got %v", err)
	}
	if cb.State() != StateHalfOpen {
		t.Errorf("Expected StateHalfOpen after timeout, got %v", cb.State())
	}
}

// TestCircuitBreaker_ClosesOnHalfOpenSuccess - RED test
// Acceptance Criterion: Successful call in half-open state transitions to closed
func TestCircuitBreaker_ClosesOnHalfOpenSuccess(t *testing.T) {
	// Arrange - get to half-open state
	cb := NewCircuitBreakerWithConfig(DefaultFailureThreshold, 50*time.Millisecond)

	for i := 0; i < DefaultFailureThreshold; i++ {
		cb.RecordFailure()
	}

	// Wait for timeout and trigger half-open
	time.Sleep(100 * time.Millisecond)
	err := cb.Allow()
	if err != nil {
		t.Fatalf("Expected Allow() to succeed for half-open transition, got %v", err)
	}

	// Act - record success in half-open state
	cb.RecordSuccess()

	// Assert - should be back to closed
	if cb.State() != StateClosed {
		t.Errorf("Expected StateClosed after half-open success, got %v", cb.State())
	}

	// Failure count should be reset
	err = cb.Allow()
	if err != nil {
		t.Errorf("Expected Allow() to succeed after recovery, got %v", err)
	}
}

// TestCircuitBreaker_ReOpensOnHalfOpenFailure - RED test
// Acceptance Criterion: Failed call in half-open state transitions back to open
func TestCircuitBreaker_ReOpensOnHalfOpenFailure(t *testing.T) {
	// Arrange - get to half-open state
	cb := NewCircuitBreakerWithConfig(DefaultFailureThreshold, 50*time.Millisecond)

	for i := 0; i < DefaultFailureThreshold; i++ {
		cb.RecordFailure()
	}

	// Wait for timeout and trigger half-open
	time.Sleep(100 * time.Millisecond)
	err := cb.Allow()
	if err != nil {
		t.Fatalf("Expected Allow() to succeed for half-open transition, got %v", err)
	}

	if cb.State() != StateHalfOpen {
		t.Fatalf("Expected StateHalfOpen, got %v", cb.State())
	}

	// Act - record failure in half-open state
	cb.RecordFailure()

	// Assert - should be back to open
	if cb.State() != StateOpen {
		t.Errorf("Expected StateOpen after half-open failure, got %v", cb.State())
	}

	// Should reject requests again
	err = cb.Allow()
	if err != ErrCircuitOpen {
		t.Errorf("Expected ErrCircuitOpen after re-trip, got %v", err)
	}
}

// TestCircuitBreaker_SuccessResetsFailureCount - RED test
// Acceptance Criterion: A success in closed state resets the failure counter
func TestCircuitBreaker_SuccessResetsFailureCount(t *testing.T) {
	// Arrange
	cb := NewCircuitBreaker()

	// Record 4 failures (just below threshold)
	for i := 0; i < DefaultFailureThreshold-1; i++ {
		cb.RecordFailure()
	}

	// Act - record a success, which should reset the counter
	cb.RecordSuccess()

	// Now record 4 more failures - should NOT trip because counter was reset
	for i := 0; i < DefaultFailureThreshold-1; i++ {
		cb.RecordFailure()
	}

	// Assert - should still be closed
	if cb.State() != StateClosed {
		t.Errorf("Expected StateClosed (success should reset failure count), got %v", cb.State())
	}
}
