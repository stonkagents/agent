// Package main: the "command tools" setup job that runs post-install.ps1
// (npm install -g stonkagents, doctor, onboard) and then gateway-setup.exe.
// Compiled to stonkagents-tools.exe (a windowless GUI process, so it never opens
// a console) and installed next to the script in the StonkAgents install
// folder.
//
// The MSI does not run it. setuphelper.exe registers the scheduled task
// "<ProductName> command tools" that runs
//
//	stonkagents-tools.exe --env <env> --background --data-dir "<data folder>"
//
// as the installing user and starts it once the services are up, so the
// installer finishes in about a minute while npm works in the background.
// The controller re-runs the same task on POST /setup/command-tools/retry,
// and a double-click on the exe does the same job by hand.
//
// Progress goes to the status file <data folder>\command-tools.json
// (internal/commandtools): the phase the script reports, npm's download
// counter and the elapsed time, rewritten on every change and at least every
// five seconds, then a final ready or failed state with the error text. The
// controller serves that file to the portal.
//
// The environment (dev, stg, prd) selects the daemon port and the npm CLI
// profile: "--env <env>", else STONKAGENTS_ENV. The data folder comes from
// "--data-dir", else config.yaml next to the exe, else this environment's
// default under the user profile.
//
// Writes all output to %TEMP%\stonkagents-tools.log (production) or
// %TEMP%\stonkagents-tools-<env>.log so install issues can be diagnosed.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/stonkagents/agent/installer/msiprogress"
	"github.com/stonkagents/agent/internal/commandtools"
	"github.com/stonkagents/agent/internal/installenv"
)

func main() {
	args := os.Args[1:]
	env := installenv.ForEnv(envArg(args, os.Getenv(installenv.EnvVar)))
	background := hasFlag(args, "--background")
	installDir := installDirFor(env)
	script := filepath.Join(installDir, "post-install.ps1")
	logPath := filepath.Join(os.Getenv("TEMP"), logName(env))
	dataDir := dataDirFor(env, flagValue(args, "--data-dir"), filepath.Join(installDir, "config.yaml"))

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		logFile = nil
	}
	defer func() {
		if logFile != nil {
			logFile.Close()
		}
	}()

	// The status file the portal shows. Every phase change and npm counter
	// tick the Reporter sees lands there.
	status := commandtools.Begin(dataDir, logPath, os.Getpid())
	progress := msiprogress.New(nil)
	progress.OnChange(func(phase, detail string, _ time.Duration) { status.Update(phase, detail) })
	progress.Start()
	defer progress.Stop()
	progress.SetPhase("Starting the command tools setup")

	// Every line the script prints feeds the progress line (phase markers,
	// npm's --loglevel=http download lines); the last plain line is kept for
	// the error text should the script fail.
	last := &lastLine{}
	lines := &msiprogress.LineWriter{Fn: func(line string) {
		progress.Observe(line)
		last.Observe(line)
	}}
	defer lines.Flush()

	writers := []io.Writer{msiprogress.Swallow{W: os.Stdout}, lines}
	if logFile != nil {
		writers = append(writers, logFile)
	}
	mw := io.MultiWriter(writers...)

	log := func(format string, args ...interface{}) {
		msg := fmt.Sprintf("[%s] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
		io.WriteString(mw, msg)
	}
	// os.Exit skips the deferred cleanup; failure paths call this instead.
	fail := func(phase, reason string) {
		log("ERROR: %s", reason)
		progress.Stop()
		lines.Flush()
		status.Finish(phase, reason)
		if err := status.Err(); err != nil {
			log("status file: %v", err)
		}
		if logFile != nil {
			logFile.Close()
		}
		os.Exit(1)
	}

	log("stonkagents-tools starting")
	log("environment=%s daemon=%s cli profile=%q background=%v", env.Env, env.DaemonURL(), env.CLIProfile, background)
	log("log file: %s", logPath)
	log("status file: %s", commandtools.Path(dataDir))
	log("installDir=%s", installDir)
	log("PATH=%s", os.Getenv("PATH"))
	log("USERPROFILE=%s", os.Getenv("USERPROFILE"))
	log("USERNAME=%s", os.Getenv("USERNAME"))
	if err := status.Err(); err != nil {
		log("status file (continuing without it): %v", err)
	}

	if _, err := os.Stat(script); err != nil {
		fail("Command tools setup failed", "post-install script not found: "+script)
	}

	cmd := exec.Command(powershellPath(),
		"-ExecutionPolicy", "Bypass",
		"-NoProfile",
		"-NonInteractive",
		"-File", script,
	)
	cmd.Dir = installDir
	// STONKAGENTS_ENV and STONKAGENTS_DAEMON_URL tell the script which daemon to
	// onboard against; OPENCLAW_PROFILE and OPENCLAW_GATEWAY_PORT (dev and stg
	// only) keep the shared npm CLI's config, state and gateway task per environment.
	cmd.Env = append(os.Environ(), installenv.EnvVar+"="+env.Env, "STONKAGENTS_DAEMON_URL="+env.DaemonURL())
	cmd.Env = append(cmd.Env, env.CLIEnv()...)
	cmd.Stdout = mw
	cmd.Stderr = mw
	// The script's children inherit these pipes. Should one of them outlive
	// the script (a gateway the CLI started inline), the job must still
	// finish with the script instead of waiting for the pipes to close.
	// Prepare also keeps powershell from opening a console window on the
	// user's desktop.
	msiprogress.Prepare(cmd)

	log("running: %s", strings.Join(cmd.Args, " "))
	if err := cmd.Start(); err != nil {
		fail("Command tools setup failed", "PowerShell did not start: "+err.Error())
	}
	leaked, err := msiprogress.WaitFor(cmd, scriptLimit)
	if err != nil {
		fail("Command tools setup ended with errors", scriptError(err, last.Text()))
	}
	if leaked {
		log("the script has exited but a process it started still holds its output; not waiting for it")
	}
	if cli := findCLI(); cli != "" {
		log("command tools installed: %s", cli)
		status.Found(cli, "")
	} else {
		log("WARN: no stonkagents shim found under %%APPDATA%%\\npm after the script")
	}

	// The gateway: gateway-setup.exe next to this exe installs the CLI's
	// gateway task and starts it. Its progress lines become this job's phase.
	gateway := filepath.Join(installDir, "gateway-setup.exe")
	if _, err := os.Stat(gateway); err != nil {
		fail("Gateway setup failed", "gateway-setup.exe not found: "+gateway)
	}
	progress.SetPhase("Setting up the gateway")
	last.Reset()
	gw := exec.Command(gateway, "--env", env.Env, "--progress")
	gw.Dir = installDir
	gw.Env = append(os.Environ(), env.CLIEnv()...)
	// Its progress lines (every second) become this job's phase and stay out
	// of the log; everything else it prints is logged like the script's output.
	gwLines := &msiprogress.LineWriter{Fn: func(line string) {
		if phase, ok := msiprogress.ParseLine(line); ok {
			progress.SetPhase(phase)
			return
		}
		io.WriteString(mw, line+"\n") // mw feeds `last` through `lines`
	}}
	defer gwLines.Flush()
	gw.Stdout = gwLines
	gw.Stderr = gwLines
	msiprogress.Prepare(gw)
	log("running: %s", strings.Join(gw.Args, " "))
	if err := gw.Start(); err != nil {
		fail("Gateway setup failed", "gateway-setup.exe did not start: "+err.Error())
	}
	if leaked, err := msiprogress.WaitFor(gw, gatewayLimit); err != nil {
		fail("Gateway setup ended with errors", scriptError(err, last.Text()))
	} else if leaked {
		log("gateway-setup.exe has exited but a process it started (the gateway) still holds its output; not waiting for it")
	}
	status.Found("", env.GatewayTaskName())

	log("stonkagents-tools completed successfully")
	progress.Stop()
	status.Finish("Command tools ready", "")
	if err := status.Err(); err != nil {
		log("status file: %v", err)
	}
}

// Ceilings on the two phases, on top of the script's own per-command caps
// (daemon wait 160 s, npm 600 s twice, cache clean 120 s, doctor 180 s,
// onboard 300 s: about 35 minutes end to end) and the task's two hour limit.
// A phase that overruns is ended with its process tree and the status file
// says so, instead of "running" until the task scheduler gives up.
const (
	scriptLimit  = 45 * time.Minute
	gatewayLimit = 10 * time.Minute
)

// scriptError is the error text for a failed child: its exit status plus the
// last plain line it printed, which is the script's own reason ("Daemon not
// ready after 150s.", "StonkAgents onboard failed (exit 1)."). A phase that
// hit its ceiling says so and what to do, since its last line is whatever the
// hung step printed.
func scriptError(err error, lastLine string) string {
	if errors.Is(err, msiprogress.ErrTimedOut) {
		text := "The setup took too long and was stopped. Retry from the portal"
		if lastLine != "" {
			text += " (last step: " + lastLine + ")"
		}
		return text
	}
	text := err.Error()
	if ee, ok := err.(*exec.ExitError); ok {
		text = fmt.Sprintf("exited %d", ee.ExitCode())
	}
	if lastLine != "" {
		text += ": " + lastLine
	}
	return text
}

var (
	// Lines that carry no reason: npm's per-request log, the tools' progress
	// lines, the script's step markers and this tool's own log lines.
	noisyLine = regexp.MustCompile(`^(npm (http|WARN|notice) |PROGRESS\t|\[\d\d:\d\d:\d\d( \+\d+s)?\] )`)
	// A "[HH:mm:ss +Ns] " step prefix in front of a line that does carry one.
	stepPrefix = regexp.MustCompile(`^\[\d\d:\d\d:\d\d \+\d+s\] `)
)

// lastLine keeps the last informative line a child printed.
type lastLine struct {
	mu   sync.Mutex
	text string
}

// Observe feeds one output line.
func (l *lastLine) Observe(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	if noisyLine.MatchString(line) {
		// Step markers with a message after the prefix still count, phase
		// markers and the rest do not.
		if !stepPrefix.MatchString(line) || strings.Contains(line, msiprogress.PhaseMarker) {
			return
		}
		line = stepPrefix.ReplaceAllString(line, "")
	}
	l.mu.Lock()
	l.text = line
	l.mu.Unlock()
}

// Text is the last line seen, shortened to fit a status line.
func (l *lastLine) Text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.text) > 160 {
		return l.text[:157] + "..."
	}
	return l.text
}

// Reset forgets the last line (between the script and the gateway tool).
func (l *lastLine) Reset() {
	l.mu.Lock()
	l.text = ""
	l.mu.Unlock()
}

// installDirFor is the folder holding post-install.ps1: the tool's own
// folder when it lives in an install, else this environment's default.
func installDirFor(env installenv.Profile) string {
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		if _, err := os.Stat(filepath.Join(dir, "post-install.ps1")); err == nil {
			return dir
		}
	}
	return filepath.Join(os.Getenv("ProgramFiles"), env.InstallDirName)
}

var configDataDir = regexp.MustCompile(`(?m)^data_dir:\s*(.+?)\s*$`)

// dataDirFor is the data folder the status file lives in: the --data-dir
// argument (what setuphelper passed to the task), else data_dir from the
// install's config.yaml (setuphelper wrote the MSI's DATADIR there), else
// the environment's default under the user profile.
func dataDirFor(env installenv.Profile, flag, configPath string) string {
	if flag = strings.Trim(strings.TrimSpace(flag), `"`); flag != "" {
		return filepath.Clean(flag)
	}
	if raw, err := os.ReadFile(configPath); err == nil {
		if m := configDataDir.FindSubmatch(raw); m != nil {
			if v := unquoteYAML(string(m[1])); v != "" {
				return v
			}
		}
	}
	home := os.Getenv("USERPROFILE")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, "Documents", env.DataDirName, "data")
}

// unquoteYAML undoes the quoting setuphelper uses for data_dir: single
// quotes with ” for a quote and %% for a percent sign, or double quotes.
func unquoteYAML(v string) string {
	v = strings.TrimSpace(v)
	switch {
	case len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'':
		v = strings.ReplaceAll(v[1:len(v)-1], "''", "'")
		v = strings.ReplaceAll(v, "%%", "%")
	case len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"':
		v = strings.ReplaceAll(v[1:len(v)-1], `\"`, `"`)
		v = strings.ReplaceAll(v, `\\`, `\`)
	}
	return v
}

// findCLI is the npm shim the script installed, empty when missing.
func findCLI() string {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return ""
	}
	for _, name := range []string{"stonkagents.cmd"} {
		p := filepath.Join(appData, "npm", name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// powershellPath is Windows PowerShell by full path (a full path never
// depends on the environment the job was started with).
func powershellPath() string {
	if root := os.Getenv("SystemRoot"); root != "" {
		return filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	}
	return "powershell.exe"
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

// flagValue reads "--name <value>" or "--name=<value>" from the arguments.
func flagValue(args []string, flag string) string {
	for i, a := range args {
		if strings.HasPrefix(a, flag+"=") {
			return strings.TrimPrefix(a, flag+"=")
		}
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// envArg reads "--env <name>" or "--env=<name>" from the arguments, falling
// back to the given value (STONKAGENTS_ENV, then production).
func envArg(args []string, fallback string) string {
	if v := flagValue(args, "--env"); v != "" {
		return v
	}
	return fallback
}

// logName keeps production's log file name and gives dev and stg their own so
// two installs on one machine do not overwrite each other's log.
func logName(env installenv.Profile) string {
	if env.Env == installenv.Prd {
		return "stonkagents-tools.log"
	}
	return "stonkagents-tools-" + env.Env + ".log"
}
