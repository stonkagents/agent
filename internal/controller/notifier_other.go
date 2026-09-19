//go:build !darwin && !windows

// Package: internal/controller
// Feature: F-025 (Auto-Update System)
// Story: US-025-07 (macOS Install Sequence)
// Purpose: Fallback notifier factory for platforms without native notifications.
//          Returns nil — Updater nil-checks before calling notifier methods.

package controller

import "log/slog"

// newPlatformNotifier returns nil on unsupported platforms.
// The Updater's nil-safety on notifier calls ensures this is safe.
func newPlatformNotifier(_ *slog.Logger) Notifier {
	return nil
}
