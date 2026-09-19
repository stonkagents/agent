//go:build darwin

package controller

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/stonkagents/agent/internal/installenv"
)

// daemonHealthURL is the daemon's /health on this environment's port (macOS installs
// are production only today, so this is 7841 unless STONKAGENTS_ENV says otherwise).
func daemonHealthURL() string { return installenv.Current().DaemonHealthURL() }

const healthTimeout = 3 * time.Second

// daemonLabel is the launchd label of the daemon LaunchAgent.
const daemonLabel = "com.stonkagents.daemon"

func launchAgentsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents")
}

func plistPath() string {
	return filepath.Join(launchAgentsDir(), daemonLabel+".plist")
}

// runLaunchctl executes launchctl with args and returns combined output.
// Package-level var allows test injection.
var runLaunchctl = func(args ...string) ([]byte, error) {
	return exec.Command("launchctl", args...).CombinedOutput()
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// StartDaemon loads the daemon LaunchAgent via launchctl.
func StartDaemon() error {
	path := plistPath()
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("daemon plist not found at %s: %w", path, err)
	}
	if out, err := runLaunchctl("load", path); err != nil {
		return fmt.Errorf("launchctl load: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// StopDaemon unloads the daemon LaunchAgent via launchctl (bypasses KeepAlive).
func StopDaemon() error {
	path := plistPath()
	if out, err := runLaunchctl("unload", path); err != nil {
		return fmt.Errorf("launchctl unload: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// IsDaemonRunning returns true if the daemon job is
// loaded in launchctl.
func IsDaemonRunning() bool {
	out, err := runLaunchctl("list")
	if err != nil {
		return false
	}
	return strings.Contains(string(out), daemonLabel)
}

// IsDaemonHealthy returns true if GET http://localhost:7841/health returns 200.
func IsDaemonHealthy() bool {
	client := &http.Client{Timeout: healthTimeout}
	resp, err := client.Get(daemonHealthURL())
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
