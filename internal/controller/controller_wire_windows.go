//go:build windows

package controller

// wireUpdaterInstall sets the platform-specific install function for the updater.
// On Windows, install-based updates are not yet implemented; installFn remains nil.
func wireUpdaterInstall(u *Updater) {
	// No-op: installFn stays nil. Update notify/status work; install pipeline is not run.
}
