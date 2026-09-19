// Package: internal/daemon/tracker
// Feature: F-010 (P2P Transfer Protocol)
// Story: US-010-B3 (Circuit Breaker for Tracker Calls)
// Purpose: Circuit breaker prevents hammering a dead tracker, conserving daemon resources

package tracker

import (
	"errors"
	"sync"
	"time"
)

// ErrCircuitOpen is returned when the circuit breaker is open and requests are rejected.
var ErrCircuitOpen = errors.New("circuit breaker is open: tracker unavailable")

// CircuitState represents the current state of the circuit breaker.
type CircuitState int

const (
	// StateClosed allows all requests through (normal operation).
	StateClosed CircuitState = iota
	// StateOpen rejects all requests (tracker assumed down).
	StateOpen
	// StateHalfOpen allows one probe request to test recovery.
	StateHalfOpen
)

const (
	// DefaultFailureThreshold is the number of consecutive failures before tripping.
	DefaultFailureThreshold = 5
	// DefaultResetTimeout is how long to wait before probing a tripped circuit.
	DefaultResetTimeout = 30 * time.Second
)

// CircuitBreaker implements the circuit breaker pattern for tracker calls.
// It tracks consecutive failures and stops sending requests to an unavailable
// tracker, transitioning through closed -> open -> half-open -> closed states.
type CircuitBreaker struct {
	mu               sync.Mutex
	state            CircuitState
	failureCount     int
	failureThreshold int
	resetTimeout     time.Duration
	lastFailureTime  time.Time
}

// NewCircuitBreaker creates a circuit breaker with default settings
// (5 failure threshold, 30s reset timeout).
func NewCircuitBreaker() *CircuitBreaker {
	return &CircuitBreaker{
		state:            StateClosed,
		failureThreshold: DefaultFailureThreshold,
		resetTimeout:     DefaultResetTimeout,
	}
}

// NewCircuitBreakerWithConfig creates a circuit breaker with custom settings.
// Used in tests to avoid waiting 30s for timeout transitions.
func NewCircuitBreakerWithConfig(failureThreshold int, resetTimeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		state:            StateClosed,
		failureThreshold: failureThreshold,
		resetTimeout:     resetTimeout,
	}
}

// State returns the current circuit breaker state (thread-safe).
func (cb *CircuitBreaker) State() CircuitState {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	// Check if open state has timed out (should transition to half-open)
	if cb.state == StateOpen && time.Since(cb.lastFailureTime) >= cb.resetTimeout {
		return StateHalfOpen
	}

	return cb.state
}

// Allow checks whether a request is permitted through the circuit breaker.
// Returns nil if the request is allowed, ErrCircuitOpen if the circuit is open.
// When the reset timeout has elapsed, transitions from open to half-open and
// allows one probe request through.
func (cb *CircuitBreaker) Allow() error {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case StateClosed:
		return nil

	case StateOpen:
		// Check if reset timeout has elapsed
		if time.Since(cb.lastFailureTime) >= cb.resetTimeout {
			// Transition to half-open: allow one probe request
			cb.state = StateHalfOpen
			return nil
		}
		return ErrCircuitOpen

	case StateHalfOpen:
		// Already in half-open, allow the probe request
		return nil

	default:
		return ErrCircuitOpen
	}
}

// RecordSuccess records a successful tracker call. In closed state, resets the
// failure counter. In half-open state, transitions back to closed (recovery).
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failureCount = 0

	if cb.state == StateHalfOpen {
		cb.state = StateClosed
	}
}

// RecordFailure records a failed tracker call. Increments the consecutive
// failure counter. If the threshold is reached, transitions to open state.
// In half-open state, immediately re-trips to open.
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failureCount++
	cb.lastFailureTime = time.Now()

	switch cb.state {
	case StateClosed:
		if cb.failureCount >= cb.failureThreshold {
			cb.state = StateOpen
		}

	case StateHalfOpen:
		// Probe failed - re-trip immediately
		cb.state = StateOpen
	}
}
