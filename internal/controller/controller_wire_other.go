//go:build !darwin && !windows

package controller

// wireUpdaterInstall sets the platform-specific install function for the updater.
// On non-Darwin, non-Windows platforms, install-based updates are not yet implemented.
func wireUpdaterInstall(u *Updater) {
	// No-op: installFn stays nil.
}
