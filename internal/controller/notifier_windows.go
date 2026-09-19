//go:build windows

// Package: internal/controller
// Feature: F-025 (Auto-Update System)
// Story: US-025-08 (Windows Install Sequence)
// Purpose: Windows notifier no-op factory. SYSTEM service Session 0 cannot show UI;
//          portal handles all update notifications via the useUpdateStatus hook.

package controller

import "log/slog"

// newPlatformNotifier returns nil on Windows — SYSTEM service Session 0 can't show UI.
// The Updater's nil-safety on notifier calls ensures this is safe.
// Portal handles all update notifications via the useUpdateStatus hook.
func newPlatformNotifier(_ *slog.Logger) Notifier {
	return nil
}
