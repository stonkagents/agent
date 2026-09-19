//go:build windows

package msiprogress

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"

	"golang.org/x/sys/windows"
)

// hideWindow starts the child without a console window of its own. The tools
// run from a scheduled task on the user's desktop (stonkagents-tools.exe is built
// as a windowless GUI process), where every console child (powershell, npm,
// node, schtasks) would otherwise open a black window.
func hideWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
}

// KillTree ends the process and every descendant. taskkill /T is the one
// tool that walks the tree: powershell -> npm -> node -> nested npm would
// otherwise survive the parent and keep running unwatched.
func KillTree(pid int) {
	cmd := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "taskkill.exe"), "/T", "/F", "/PID", strconv.Itoa(pid))
	hideWindow(cmd)
	_ = cmd.Run()
}
