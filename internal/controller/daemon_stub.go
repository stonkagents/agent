//go:build !windows && !darwin

package controller

import "fmt"

// StartDaemon is a stub on non-Windows (e.g. darwin); use daemon_darwin.go for macOS.
func StartDaemon() error {
	return fmt.Errorf("daemon control not implemented on this platform")
}

// StopDaemon is a stub on non-Windows.
func StopDaemon() error {
	return fmt.Errorf("daemon control not implemented on this platform")
}

// IsDaemonRunning is a stub on non-Windows.
func IsDaemonRunning() bool {
	return false
}

// IsDaemonHealthy is a stub on non-Windows.
func IsDaemonHealthy() bool {
	return false
}
