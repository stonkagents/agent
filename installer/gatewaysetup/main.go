// Package main: the gateway setup tool that installs and starts the
// StonkAgents gateway scheduled task. Compiled to gateway-setup.exe, installed
// in the StonkAgents install folder and run by stonkagents-tools.exe --background
// (the "<ProductName> command tools" scheduled task the MSI registers) right
// after the command tools, in the installing user's context. It can also be
// run by hand from the install folder. Separate from stonkagents-tools.exe to
// avoid OOM: this binary is lightweight.
//
// After stonkagents gateway install creates the task, this binary patches it to
// run via a VBS hidden wrapper so the gateway has no visible console window.
//
// With --progress it prints progress lines (msiprogress.Prefix) to stdout;
// stonkagents-tools.exe reads them and shows them as the phase of the background
// job in its status file.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/stonkagents/agent/installer/msiprogress"
	"github.com/stonkagents/agent/internal/installenv"
)

func main() {
	args := os.Args[1:]
	// The environment ("--env <env>" from stonkagents-tools.exe) selects the CLI
	// profile whose gateway task this step installs; production keeps the CLI's
	// default profile.
	env := installenv.ForEnv(envArg(args, os.Getenv(installenv.EnvVar)))
	printProgress := hasFlag(args, "--progress")
	logPath := filepath.Join(os.TempDir(), logName(env))
	logFile, _ := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	defer func() {
		if logFile != nil {
			logFile.Close()
		}
	}()

	var progressOut io.Writer
	if printProgress {
		progressOut = os.Stdout
	}
	progress := msiprogress.New(progressOut)
	progress.Start()
	defer progress.Stop()
	progress.SetPhase("Starting the gateway setup")

	writers := []io.Writer{msiprogress.Swallow{W: os.Stdout}}
	if logFile != nil {
		writers = append(writers, logFile)
	}
	mw := io.MultiWriter(writers...)

	log := func(format string, args ...interface{}) {
		msg := fmt.Sprintf("[%s] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
		io.WriteString(mw, msg)
	}
	// os.Exit skips the deferred cleanup; failure paths call this instead.
	fail := func(final string) {
		progress.SetPhase(final)
		progress.Stop()
		if logFile != nil {
			logFile.Close()
		}
		os.Exit(1)
	}

	log("gateway-setup starting")
	log("environment=%s cli profile=%q gateway port=%d task=%q", env.Env, env.CLIProfile, env.GatewayPort, env.GatewayTaskName())
	log("log file: %s", logPath)
	log("USERNAME=%s USERPROFILE=%s", os.Getenv("USERNAME"), os.Getenv("USERPROFILE"))

	stonkagents := findStonkAgents()
	if stonkagents == "" {
		log("ERROR: stonkagents not found in PATH")
		fail("Gateway setup skipped: the stonkagents command tools are missing, see " + logName(env))
	}
	log("using stonkagents: %s", stonkagents)

	// OPENCLAW_PROFILE / OPENCLAW_GATEWAY_PORT (dev and stg only): the CLI keeps
	// this environment's config, state dir and gateway task apart from the others'.
	cliEnv := append(os.Environ(), env.CLIEnv()...)
	// The CLI starts the gateway through the task scheduler, which gives it
	// no handles of ours. Its Startup-folder fallback (task creation denied)
	// starts the gateway as a grandchild instead, and that one inherits the
	// pipes below; Prepare and Wait keep this tool from waiting on it.
	// Each CLI call has a ceiling (cliLimit): a CLI waiting on a prompt it
	// cannot get, or a task scheduler call that never returns, would otherwise
	// keep the whole command tools job "running" until its two hour limit.
	run := func(name string, args ...string) error {
		cmd := exec.Command(name, args...)
		cmd.Env = cliEnv
		cmd.Stdout = mw
		cmd.Stderr = mw
		msiprogress.Prepare(cmd)
		if err := cmd.Start(); err != nil {
			return err
		}
		leaked, err := msiprogress.WaitFor(cmd, cliLimit)
		if leaked {
			log("%s %s has exited; a process it started (the gateway) keeps its output open, not waiting for it", name, strings.Join(args, " "))
		}
		return err
	}

	progress.SetPhase("Installing the gateway task")
	log("running: %s gateway install", stonkagents)
	if err := run(stonkagents, "gateway", "install"); err != nil {
		log("gateway install failed: %v; trying gateway start", err)
		progress.SetPhase("Starting the gateway")
		if err2 := run(stonkagents, "gateway", "start"); err2 != nil {
			log("gateway start also failed: %v", err2)
			fail(gatewayFailure(err, err2, env))
		}
	}

	// Patch the scheduled task to run hidden (no visible console window).
	// stonkagents gateway install creates gateway.cmd + "OpenClaw Gateway" task.
	// We write a VBS wrapper and re-register the task to use it.
	progress.SetPhase("Starting the gateway")
	patchGatewayTaskHidden(log, env)

	log("gateway-setup completed")
	progress.SetPhase("Gateway running, your agent is connected")
}

// patchGatewayTaskHidden creates a VBS wrapper alongside gateway.cmd and
// re-registers the scheduled task to launch via wscript.exe (hidden window).
func patchGatewayTaskHidden(log func(string, ...interface{}), env installenv.Profile) {
	userProfile := os.Getenv("USERPROFILE")
	if userProfile == "" {
		log("WARN: USERPROFILE not set, skipping hidden-window patch")
		return
	}

	stateDir := filepath.Join(userProfile, env.CLIStateDirName())
	cmdPath := filepath.Join(stateDir, "gateway.cmd")
	if _, err := os.Stat(cmdPath); err != nil {
		log("WARN: gateway.cmd not found at %s, skipping hidden-window patch", cmdPath)
		return
	}

	// Write VBS wrapper: WScript.Shell.Run with window style 0 (vbHide)
	vbsPath := filepath.Join(stateDir, "gateway-hidden.vbs")
	escaped := strings.ReplaceAll(cmdPath, `\`, `\\`)
	vbsContent := fmt.Sprintf("Set WshShell = CreateObject(\"WScript.Shell\")\r\nWshShell.Run \"\"\"%s\"\"\", 0, False\r\n", escaped)
	if err := os.WriteFile(vbsPath, []byte(vbsContent), 0644); err != nil {
		log("WARN: failed to write VBS wrapper: %v", err)
		return
	}
	log("wrote hidden launcher: %s", vbsPath)

	taskName := env.GatewayTaskName()

	// Stop the currently running (visible) task
	stopCmd := exec.Command("schtasks", "/End", "/TN", taskName)
	msiprogress.Prepare(stopCmd)
	stopCmd.Run() // ignore error; may not be running

	// Re-register the task to use the VBS wrapper
	taskCommand := fmt.Sprintf(`wscript.exe //B "%s"`, vbsPath)
	username := os.Getenv("USERNAME")
	domain := os.Getenv("USERDOMAIN")
	runAs := username
	if domain != "" {
		runAs = domain + `\` + username
	}

	args := []string{
		"/Create", "/F",
		"/SC", "ONLOGON",
		"/RL", "LIMITED",
		"/TN", taskName,
		"/TR", taskCommand,
	}
	if runAs != "" {
		args = append(args, "/RU", runAs, "/NP", "/IT")
	}

	log("re-registering task with hidden launcher: schtasks %s", strings.Join(args, " "))
	regCmd := exec.Command("schtasks", args...)
	msiprogress.Prepare(regCmd)
	out, err := regCmd.CombinedOutput()
	if err != nil {
		log("WARN: schtasks re-register failed: %v (%s)", err, string(out))
		return
	}
	log("task re-registered: %s", strings.TrimSpace(string(out)))

	// Start the task (now hidden)
	runCmd := exec.Command("schtasks", "/Run", "/TN", taskName)
	msiprogress.Prepare(runCmd)
	runOut, err := runCmd.CombinedOutput()
	if err != nil {
		log("WARN: schtasks /Run failed: %v (%s)", err, string(runOut))
		return
	}
	log("gateway started (hidden): %s", strings.TrimSpace(string(runOut)))
}

// cliLimit is the ceiling on one CLI call (gateway install, gateway start).
const cliLimit = 5 * time.Minute

// gatewayFailure is the final phase (the portal shows it as the job's error)
// when neither gateway install nor gateway start worked: the reason the
// person can act on, then where the CLI's own output is. A port already in
// use is the one failure the CLI names that the person can fix alone.
func gatewayFailure(installErr, startErr error, env installenv.Profile) string {
	switch {
	case errors.Is(installErr, msiprogress.ErrTimedOut) || errors.Is(startErr, msiprogress.ErrTimedOut):
		return fmt.Sprintf("Gateway setup took too long and was stopped; retry from the portal, see %s", logName(env))
	default:
		return fmt.Sprintf("Gateway setup ended with errors (port %d busy, or the command tools did not install cleanly); retry from the portal, see %s", env.GatewayPort, logName(env))
	}
}

// findStonkAgents locates the stonkagents CLI: on PATH, then in npm's global
// bin folder (%APPDATA%\npm), which the Node.js installer adds to the user's
// PATH but a process started before that install may not see yet.
func findStonkAgents() string {
	return findCLI(os.Getenv("PATH"), os.Getenv("APPDATA"), func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	})
}

// cliNames are the CLI's bin shims, current package first.
var cliNames = []string{"stonkagents.cmd", "stonkagents"}

// findCLI walks the PATH folders and then %APPDATA%\npm for the first CLI
// shim that exists.
func findCLI(pathEnv, appData string, exists func(string) bool) string {
	dirs := filepath.SplitList(pathEnv)
	if appData != "" {
		dirs = append(dirs, filepath.Join(appData, "npm"))
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		for _, name := range cliNames {
			candidate := filepath.Join(dir, name)
			if exists(candidate) {
				return candidate
			}
		}
	}
	return ""
}

// hasFlag reports whether the bare flag is among the arguments.
func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// envArg reads "--env <name>" or "--env=<name>" from the arguments, falling
// back to the given value (STONKAGENTS_ENV, then production).
func envArg(args []string, fallback string) string {
	for i, a := range args {
		if strings.HasPrefix(a, "--env=") {
			return strings.TrimPrefix(a, "--env=")
		}
		if a == "--env" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return fallback
}

// logName keeps production's log file name and gives dev and stg their own.
func logName(env installenv.Profile) string {
	if env.Env == installenv.Prd {
		return "gateway-setup.log"
	}
	return "gateway-setup-" + env.Env + ".log"
}
