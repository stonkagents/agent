//go:build !windows

package setup

import (
	"os"
	"path/filepath"
)

// Firewall is Windows only.
func Firewall(_ string) Check { return WindowsOnly(IDFirewall) }

// FixFirewall is Windows only.
func FixFirewall(_ string) Check { return WindowsOnly(IDFirewall) }

// Autostart is Windows only (launchd keeps the macOS daemon alive).
func Autostart() Check { return WindowsOnly(IDAutostart) }

// FixAutostart is Windows only.
func FixAutostart() Check { return WindowsOnly(IDAutostart) }

// platformStorageCandidates returns the current dir and ~/StonkAgents.
func platformStorageCandidates(current string) []string {
	out := []string{}
	if current != "" {
		out = append(out, current)
	}
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, "StonkAgents")
		if p != current {
			out = append(out, p)
		}
	}
	return out
}

// DefaultDaemonExePath is the daemon binary next to the calling executable.
func DefaultDaemonExePath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(exe), "stonkagents-daemon")
}
