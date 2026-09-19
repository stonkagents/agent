package main

import (
	"os/exec"
	"strings"
	"time"
)

// serviceWait bounds every wait on the service control manager below. An upgrade
// deletes the previous release's services while their processes may still be
// shutting down; until the last handle closes the name stays "marked for
// deletion" and CreateService fails with 1072.
const serviceWait = 45 * time.Second

// serviceState runs "sc query name" and reports whether the service exists and,
// if so, its STATE word (STOPPED, RUNNING, STOP_PENDING, ...).
func serviceState(scExe, name string) (exists bool, state string) {
	out, _ := exec.Command(scExe, "query", name).CombinedOutput()
	text := string(out)
	// 1060: the specified service does not exist as an installed service.
	if strings.Contains(text, "1060") {
		return false, ""
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "STATE") {
			fields := strings.Fields(line)
			return true, fields[len(fields)-1]
		}
	}
	return true, ""
}

// removeServiceAndWait stops the service, waits for it to report STOPPED, deletes
// it and waits until the name is free again, each wait bounded by timeout. A
// service that does not exist returns at once.
func removeServiceAndWait(scExe, name string, timeout time.Duration) {
	exists, state := serviceState(scExe, name)
	if !exists {
		return
	}
	if state != "STOPPED" {
		_ = exec.Command(scExe, "stop", name).Run()
		waitFor(timeout, func() bool {
			exists, state := serviceState(scExe, name)
			return !exists || state == "STOPPED"
		})
	}
	_ = exec.Command(scExe, "delete", name).Run()
	waitFor(timeout, func() bool {
		exists, _ := serviceState(scExe, name)
		return !exists
	})
}

// createServiceRetry runs "sc create" and retries while the name is still marked
// for deletion (1072), for at most timeout. Other errors return at once.
func createServiceRetry(scExe string, timeout time.Duration, name string, args ...string) ([]byte, error) {
	deadline := time.Now().Add(timeout)
	for {
		out, err := exec.Command(scExe, append([]string{"create", name}, args...)...).CombinedOutput()
		if err == nil || !strings.Contains(string(out), "1072") || time.Now().After(deadline) {
			return out, err
		}
		time.Sleep(time.Second)
	}
}

func waitFor(timeout time.Duration, done func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if done() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(500 * time.Millisecond)
	}
}
