package msiprogress

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// helperEnv selects the role the test binary plays when re-run as a child:
// "child" starts a grandchild that inherits its stdout and stderr and exits
// at once; "fail" exits 3.
const helperEnv = "MSIPROGRESS_TEST_HELPER"

// grandchildCommand is a system process that lives about eight seconds while
// holding whatever stdout and stderr it was given (not the test binary, so
// go test can delete its own executable when it is done).
func grandchildCommand() *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "PING.EXE"), "-n", "9", "127.0.0.1")
	}
	return exec.Command("sleep", "8")
}

// TestHelperProcess is not a test; it is the child of the Wait tests.
func TestHelperProcess(t *testing.T) {
	switch os.Getenv(helperEnv) {
	case "child":
		grandchild := grandchildCommand()
		grandchild.Stdout = os.Stdout
		grandchild.Stderr = os.Stderr
		if err := grandchild.Start(); err != nil {
			fmt.Println("grandchild start failed:", err)
			os.Exit(2)
		}
		fmt.Printf("grandchild pid %d\n", grandchild.Process.Pid)
		fmt.Println("child done")
		os.Exit(0)
	case "fail":
		fmt.Println("failing on purpose")
		os.Exit(3)
	}
}

var grandchildPid = regexp.MustCompile(`grandchild pid (\d+)`)

// killGrandchild ends the grandchild the child reported in its output.
func killGrandchild(t *testing.T, out string) {
	t.Helper()
	m := grandchildPid.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no grandchild pid in child output: %q", out)
	}
	pid, _ := strconv.Atoi(m[1])
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}

func childCommand() (*exec.Cmd, *bytes.Buffer) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), helperEnv+"=child")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	return cmd, &out
}

// The setup tools must finish when their script finishes, even when a
// process the script started (the gateway) is still holding the script's
// stdout and stderr.
func TestWaitReturnsWhenGrandchildHoldsPipes(t *testing.T) {
	// Without a WaitDelay, Wait blocks for as long as the grandchild lives:
	// this is the installer hang.
	plain, plainOut := childCommand()
	if err := plain.Start(); err != nil {
		t.Fatal(err)
	}
	plainDone := make(chan error, 1)
	go func() { plainDone <- plain.Wait() }()
	select {
	case err := <-plainDone:
		t.Fatalf("plain Wait returned (%v) while the grandchild still held the pipes; the test setup does not reproduce the hang", err)
	case <-time.After(3 * time.Second):
	}
	defer func() {
		// The plain Wait returns once its grandchild is gone.
		killGrandchild(t, plainOut.String())
		<-plainDone
	}()

	// With Prepare and Wait, the tool is done shortly after the child exits.
	cmd, out := childCommand()
	Prepare(cmd)
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	leaked, err := Wait(cmd)
	took := time.Since(start)
	defer killGrandchild(t, out.String())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !leaked {
		t.Error("Wait did not report the leaked pipes")
	}
	if took > 5*time.Second {
		t.Errorf("Wait took %v, want under 5s", took)
	}
	if !strings.Contains(out.String(), "child done") {
		t.Errorf("child output lost: %q", out.String())
	}
	if !cmd.ProcessState.Success() {
		t.Errorf("child exit: %v", cmd.ProcessState)
	}
}

// A child that exits non-zero keeps its exit error.
func TestWaitKeepsExitError(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), helperEnv+"=fail")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	Prepare(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	leaked, err := Wait(cmd)
	if err == nil {
		t.Fatal("Wait: want an exit error for a failing child")
	}
	if leaked {
		t.Error("nothing held the pipes, leaked should be false")
	}
	if !strings.Contains(out.String(), "failing on purpose") {
		t.Errorf("child output lost: %q", out.String())
	}
}

// A child that outlives WaitFor's limit is ended and reported as timed out,
// well before its own natural end.
func TestWaitForEndsAHungChild(t *testing.T) {
	cmd := grandchildCommand() // lives about eight seconds on its own
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	Prepare(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	leaked, err := WaitFor(cmd, 1*time.Second)
	took := time.Since(start)
	if err != ErrTimedOut {
		t.Fatalf("WaitFor: err = %v, want ErrTimedOut", err)
	}
	if leaked {
		t.Error("a killed child is not a leak")
	}
	if took > 6*time.Second {
		t.Errorf("WaitFor took %v, want about the 1s limit", took)
	}
	if cmd.ProcessState == nil || cmd.ProcessState.Success() {
		t.Errorf("child should have been ended: %v", cmd.ProcessState)
	}
}

// A child that finishes inside the limit is reported exactly as Wait would.
func TestWaitForKeepsAQuickChildsResult(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), helperEnv+"=fail")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	Prepare(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_, err := WaitFor(cmd, 30*time.Second)
	if err == nil || err == ErrTimedOut {
		t.Fatalf("WaitFor: err = %v, want the child's exit error", err)
	}
}
