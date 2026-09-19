//go:build darwin

package controller

// wireUpdaterInstall sets the platform-specific install function for the updater.
func wireUpdaterInstall(u *Updater) {
	u.installFn = u.installDarwin
}
