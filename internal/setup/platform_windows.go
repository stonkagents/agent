//go:build windows

package setup

import (
	"os"
	"os/exec"
	"path/filepath"
)

// system32 returns the full path of a System32 tool so the controller (a
// SYSTEM service with a minimal PATH) and the MSI custom action can run it.
func system32(tool string) string {
	if root := os.Getenv("SystemRoot"); root != "" {
		return filepath.Join(root, "System32", tool)
	}
	return tool
}

// execRunner runs netsh/sc/powershell from System32 and returns combined output.
func execRunner(name string, args ...string) ([]byte, error) {
	switch name {
	case "netsh", "sc":
		name = system32(name + ".exe")
	case "powershell":
		name = system32(filepath.Join("WindowsPowerShell", "v1.0", "powershell.exe"))
	}
	return exec.Command(name, args...).CombinedOutput()
}

// Firewall reports whether the program-scoped inbound rule exists for exePath.
func Firewall(exePath string) Check { return CheckFirewallRule(execRunner, exePath) }

// FixFirewall adds the rule (idempotent) and returns the resulting check.
func FixFirewall(exePath string) Check { return AddFirewallRule(execRunner, exePath) }

// Autostart reports whether both services are set to start automatically.
func Autostart() Check {
	return CheckAutostart(execRunner, DaemonServiceName(), ControllerServiceName())
}

// FixAutostart sets both services to start automatically.
func FixAutostart() Check {
	return SetAutostart(execRunner, DaemonServiceName(), ControllerServiceName())
}

// platformStorageCandidates returns the Windows candidate data directories:
// the current dir, %USERPROFILE%\StonkAgents and D:\StonkAgents when a D:
// drive exists.
func platformStorageCandidates(current string) []string {
	var out []string
	add := func(p string) {
		if p == "" {
			return
		}
		for _, existing := range out {
			if samePath(existing, p) {
				return
			}
		}
		out = append(out, p)
	}
	add(current)
	if profile := os.Getenv("USERPROFILE"); profile != "" {
		add(filepath.Join(profile, "StonkAgents"))
	}
	if info, err := os.Stat(`D:\`); err == nil && info.IsDir() {
		add(`D:\StonkAgents`)
	}
	return out
}

// DefaultDaemonExePath is the daemon binary next to the calling executable
// (the MSI installs every binary into one directory).
func DefaultDaemonExePath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(exe), "stonkagents-daemon.exe")
}
