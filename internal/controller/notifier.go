// Package: internal/controller
// Feature: F-025 (Auto-Update System)
// Story: US-025-05 (Controller Update Engine)
// Purpose: Notifier interface for platform-specific update notifications.
//          Concrete implementations: macOS (B.2) uses UNUserNotificationCenter,
//          Windows (C.2) uses portal-only. Updater nil-checks before calling.

package controller

// Notifier delivers update notifications to the user via platform-native mechanisms.
// Implementations must be safe for concurrent use.
type Notifier interface {
	// NotifyUpdateAvailable informs the user that a new version is available.
	NotifyUpdateAvailable(version, notes string) error

	// NotifyUpdateComplete informs the user that an update installed successfully.
	NotifyUpdateComplete(version string) error

	// NotifyUpdateFailed informs the user that an update failed.
	NotifyUpdateFailed(version, errMsg string) error
}
