//go:build !windows

package msiprogress

import (
	"os"
	"os/exec"
)

// hideWindow is a no-op outside Windows.
func hideWindow(*exec.Cmd) {}

// KillTree ends the process; outside Windows the tools' children are not
// ours to walk, and the tests only need the direct child gone.
func KillTree(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}
