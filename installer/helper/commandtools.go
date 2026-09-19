package main

import (
	"os/exec"
	"strings"

	"github.com/stonkagents/agent/internal/commandtools"
	"github.com/stonkagents/agent/internal/installenv"
)

// startCommandTools registers the "<ProductName> command tools" scheduled
// task and starts it, so npm install, onboarding and the gateway run in the
// background as the installing user while the installer finishes. The MSI
// custom action runs this helper as LocalSystem; the task's principal is the
// user of the console session (whoever is sitting at the machine, resolved
// from the session token, which LocalSystem may read), else the MSI's
// LogonUser property (the account that launched the install; a bare name
// that Task Scheduler resolves locally). Non-fatal: the portal offers a
// retry through the controller, which registers the task itself when it is
// missing.
func startCommandTools(run commandtools.Runner, env installenv.Profile, installDir, dataDir, logonUser string, logf installenv.Logf) {
	name := commandtools.TaskName(env)
	userID, display, err := commandtools.ConsoleUser()
	if err != nil {
		logf("command tools task: console user unknown (%v); falling back to LogonUser %q", err, logonUser)
		userID = strings.TrimSpace(logonUser)
		display = userID
	}
	if userID == "" {
		logf("command tools task: no user to run it as; skipping (retry from the portal)")
		return
	}
	logf("command tools task %q runs as %s (%s)", name, display, userID)
	if err := commandtools.Register(run, name, commandtools.ToolExe(installDir), commandtools.ToolArgs(env.Env, dataDir), userID); err != nil {
		logf("command tools task (non-fatal): %v", err)
		return
	}
	if err := commandtools.Run(run, name); err != nil {
		logf("command tools task start (non-fatal): %v", err)
		return
	}
	logf("command tools task started")
}

// commandRunner runs a command and returns its combined output.
func commandRunner(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}
