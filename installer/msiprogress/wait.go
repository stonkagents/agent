package msiprogress

import (
	"errors"
	"os/exec"
	"time"
)

// WaitDelay is how long Wait keeps reading a child's stdout and stderr pipes
// after the child has exited. The setup tools give their children (powershell,
// the npm CLI) pipes for both, and a plain cmd.Wait only returns once every
// holder of those pipes has closed them. A grandchild that inherited them and
// outlives the child (the gateway, when the CLI starts it inline rather than
// through the task scheduler) then keeps the tool, and with it the installer's
// custom action, waiting forever. With a WaitDelay, Wait closes the pipes
// itself once the child is gone and the delay has passed.
const WaitDelay = 5 * time.Second

// Prepare sets WaitDelay on a command that is about to be started. Call it
// before cmd.Start.
func Prepare(cmd *exec.Cmd) {
	cmd.WaitDelay = WaitDelay
}

// Wait waits for a started command. It returns leaked = true when the
// child exited successfully but something else still held its stdout or
// stderr open when WaitDelay ran out; that is not a failure of the child, so
// err is nil then. Everything the child itself wrote has been delivered by
// the time Wait returns, whether or not the pipes leaked.
func Wait(cmd *exec.Cmd) (leaked bool, err error) {
	err = cmd.Wait()
	if errors.Is(err, exec.ErrWaitDelay) {
		return true, nil
	}
	return false, err
}

// ErrTimedOut is WaitFor's error once its limit has passed.
var ErrTimedOut = errors.New("timed out")

// WaitFor is Wait with a ceiling: when the child has not exited after limit
// it is ended together with every descendant (KillTree) and err is
// ErrTimedOut. The scheduled task the setup tools run under has its own two
// hour limit, but the status file kept reading "running" for all of it; a
// ceiling here turns a hung npm or CLI into a failed status with a reason
// and a Retry in the portal.
func WaitFor(cmd *exec.Cmd, limit time.Duration) (leaked bool, err error) {
	done := make(chan struct{})
	go func() {
		leaked, err = Wait(cmd)
		close(done)
	}()
	select {
	case <-done:
		return leaked, err
	case <-time.After(limit):
		KillTree(cmd.Process.Pid)
		<-done
		return false, ErrTimedOut
	}
}
