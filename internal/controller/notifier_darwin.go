//go:build darwin

// Package: internal/controller
// Feature: F-025 (Auto-Update System)
// Story: US-025-07 (macOS Install Sequence)
// Purpose: macOS notifier that shells out to stonkagents-notify tool for native
//          notifications via osascript. Implements Notifier interface.

package controller

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
)

// DarwinNotifier sends native macOS notifications via the stonkagents-notify helper tool.
type DarwinNotifier struct {
	logger *slog.Logger
}

// newPlatformNotifier returns the macOS-specific notifier.
// Called from NewServer() — shared code never references DarwinNotifier directly.
func newPlatformNotifier(logger *slog.Logger) Notifier {
	return &DarwinNotifier{logger: logger}
}

// runNotifyCmd executes the notification tool with args and returns combined output.
// Package-level var allows test injection (same pattern as runCodesign).
var runNotifyCmd = defaultRunNotifyCmd

func defaultRunNotifyCmd(tool string, args ...string) ([]byte, error) {
	cmd := exec.Command(tool, args...)
	return cmd.CombinedOutput()
}

// notifyToolPathFrom searches for stonkagents-notify in known locations.
// homeDir is the user home directory; controllerDir is the directory containing
// the controller binary. Returns error if not found — never falls back to PATH.
func notifyToolPathFrom(homeDir, controllerDir string) (string, error) {
	candidates := []string{
		filepath.Join(homeDir, ".local", "bin", "stonkagents-notify"),
		filepath.Join(controllerDir, "stonkagents-notify"),
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("stonkagents-notify not found in %v", candidates)
}

// findNotifyTool returns the path to the stonkagents-notify binary.
// Package-level var allows test injection (same pattern as runCodesign).
var findNotifyTool = defaultFindNotifyTool

func defaultFindNotifyTool() (string, error) {
	home, _ := os.UserHomeDir()
	controllerDir := filepath.Dir(os.Args[0])
	return notifyToolPathFrom(home, controllerDir)
}

func (n *DarwinNotifier) NotifyUpdateAvailable(version, notes string) error {
	return n.send("StonkAgents Update", "v"+version+" available", notes)
}

func (n *DarwinNotifier) NotifyUpdateComplete(version string) error {
	return n.send("StonkAgents Updated", "v"+version+" installed", "Restart to use the latest version.")
}

func (n *DarwinNotifier) NotifyUpdateFailed(version, errMsg string) error {
	return n.send("StonkAgents Update Failed", "v"+version, errMsg)
}

// send invokes the stonkagents-notify tool with title, subtitle, and body.
func (n *DarwinNotifier) send(title, subtitle, body string) error {
	tool, err := findNotifyTool()
	if err != nil {
		if n.logger != nil {
			n.logger.Warn("[DarwinNotifier] notify tool not found", "err", err)
		}
		return fmt.Errorf("notify tool: %w", err)
	}
	out, runErr := runNotifyCmd(tool, title, subtitle, body)
	if runErr != nil {
		if n.logger != nil {
			n.logger.Warn("[DarwinNotifier] notification failed",
				"tool", tool, "err", runErr, "output", string(out))
		}
		return fmt.Errorf("notify: %w", runErr)
	}
	return nil
}
