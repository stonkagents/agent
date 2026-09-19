// Package: tracker/internal/clock
// Feature: F-007 (Centralized Tracker)
// Story: US-007-02 (Peer Registry with Redis Presence Tracking)
// Purpose: Clock abstraction for deterministic time-based testing

package clock

import "time"

// Clock provides an abstraction over time for testability.
type Clock interface {
	Now() time.Time
}

// RealClock uses the actual system time.
type RealClock struct{}

// Now returns the current system time.
func (RealClock) Now() time.Time { return time.Now() }

// MockClock provides a controllable clock for testing.
type MockClock struct {
	current time.Time
}

// NewMockClock creates a MockClock set to the given time.
func NewMockClock(t time.Time) *MockClock {
	return &MockClock{current: t}
}

// Now returns the mock clock's current time.
func (m *MockClock) Now() time.Time { return m.current }

// Advance moves the mock clock forward by the given duration.
func (m *MockClock) Advance(d time.Duration) { m.current = m.current.Add(d) }
