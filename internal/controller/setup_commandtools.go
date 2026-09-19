// Package: internal/controller
// Purpose: The background command tools job (internal/commandtools) as the
// portal sees it. The MSI registers and starts the "<ProductName> command
// tools" scheduled task; the job writes <data folder>\command-tools.json; the
// controller serves that file and re-runs the task on request.
//
//	GET  /setup/command-tools        → 200 {state, phase, detail, ..., cli_present, gateway_running, task_name}
//	POST /setup/command-tools/retry  → 202 {status: "started", task_name, attempt}
//	                                   409 ALREADY_RUNNING, 500 RETRY_FAILED / NO_USER,
//	                                   501 NOT_SUPPORTED (not Windows), plus setupGate's
//	                                   403 / 400 / 429
//
// Both are loopback only and reachable through the daemon's controller proxy
// (/api/v1/controller/setup/command-tools[/retry]) as well as directly. The
// retry carries the setup mutation headers like every other setup POST.
//
// The controller runs as LocalSystem, which is what makes the retry work: it
// may read the console session's token (commandtools.ConsoleUser) to register
// the task for the user in front of the machine when the task is missing, and
// `schtasks /Run` on a task whose principal is that user's interactive token
// starts the job on the user's desktop, with the user's profile, from
// session 0.

package controller

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/stonkagents/agent/internal/commandtools"
	"github.com/stonkagents/agent/internal/config"
	"github.com/stonkagents/agent/internal/installenv"
)

// commandToolsDeps are the pieces the handlers touch; tests replace them.
type commandToolsDeps struct {
	dataDir     func() string                        // the environment's data folder, "" when unknown
	installDir  func() string                        // the install folder (stonkagents-tools.exe lives there)
	run         commandtools.Runner                  // schtasks
	consoleUser func() (sid, name string, err error) // commandtools.ConsoleUser
	probe       func(url string) bool                // is the gateway answering
	now         func() time.Time
	supported   bool // Windows only
}

func defaultCommandToolsDeps() commandToolsDeps {
	return commandToolsDeps{
		dataDir:     defaultDataDir,
		installDir:  defaultInstallDir,
		run:         func(name string, args ...string) ([]byte, error) { return exec.Command(name, args...).CombinedOutput() },
		consoleUser: commandtools.ConsoleUser,
		probe:       probeURL,
		now:         time.Now,
		supported:   runtime.GOOS == "windows",
	}
}

// defaultDataDir is data_dir from the install's config.yaml (STONKAGENTS_CONFIG_PATH
// from daemon.env), else the folder next to the secrets file, else nothing.
func defaultDataDir() string {
	if cfg, err := config.Load(); err == nil && cfg.DataDir != "" {
		return cfg.DataDir
	}
	if secrets := os.Getenv("STONKAGENTS_SECRETS_PATH"); secrets != "" {
		return filepath.Join(filepath.Dir(secrets), "data")
	}
	return ""
}

// defaultInstallDir is the folder this executable runs from.
func defaultInstallDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(exe)
}

// probeURL reports whether something answers an HTTP GET on url.
func probeURL(url string) bool {
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get(strings.TrimRight(url, "/") + "/")
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return true
}

// commandToolsReport is the GET answer: the status file plus what the
// controller can see for itself.
type commandToolsReport struct {
	commandtools.Status
	CLIPresent     bool   `json:"cli_present"`     // the shim the job recorded still exists
	GatewayRunning bool   `json:"gateway_running"` // this environment's gateway answers
	TaskName       string `json:"task_name"`
	Supported      bool   `json:"supported"` // false outside Windows
}

func (s *Server) commandToolsReport() commandToolsReport {
	env := installenv.Current()
	d := s.commandTools
	report := commandToolsReport{
		Status:    commandtools.Status{State: commandtools.StateNotStarted},
		TaskName:  commandtools.TaskName(env),
		Supported: d.supported,
	}
	dataDir := d.dataDir()
	if dataDir != "" {
		report.Status = commandtools.Effective(commandtools.Read(dataDir), d.now())
	}
	if report.CLIPath != "" {
		if info, err := os.Stat(report.CLIPath); err == nil && !info.IsDir() {
			report.CLIPresent = true
		}
	}
	if report.State != commandtools.StateNotStarted {
		report.GatewayRunning = d.probe(env.GatewayURL())
	}
	return report
}

// handleCommandToolsStatus handles GET /setup/command-tools.
func (s *Server) handleCommandToolsStatus(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r) {
		writeJSON(w, http.StatusForbidden, errorResponse{Error: errorPayload{Code: "FORBIDDEN", Message: "only localhost requests allowed"}})
		return
	}
	writeJSON(w, http.StatusOK, s.commandToolsReport())
}

type commandToolsRetryResponse struct {
	Status   string `json:"status"` // "started"
	TaskName string `json:"task_name"`
	Attempt  int    `json:"attempt"` // the attempt the job will record
}

// handleCommandToolsRetry handles POST /setup/command-tools/retry: it starts
// the scheduled task, registering it first when it is missing (an install
// whose registration failed, or a task the user deleted).
func (s *Server) handleCommandToolsRetry(w http.ResponseWriter, r *http.Request) {
	if !s.setupGate(w, r) {
		return
	}
	d := s.commandTools
	if !d.supported {
		writeJSON(w, http.StatusNotImplemented, errorResponse{Error: errorPayload{Code: "NOT_SUPPORTED", Message: "the command tools job exists on Windows only"}})
		return
	}
	dataDir := d.dataDir()
	if dataDir == "" {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: errorPayload{Code: "RETRY_FAILED", Message: "the agent's data folder is unknown; is config.yaml in place?"}})
		return
	}
	env := installenv.Current()
	name := commandtools.TaskName(env)
	prev := commandtools.Effective(commandtools.Read(dataDir), d.now())
	if prev.State == commandtools.StateRunning {
		writeJSON(w, http.StatusConflict, errorResponse{Error: errorPayload{Code: "ALREADY_RUNNING", Message: "the command tools setup is still running: " + prev.Phase}})
		return
	}
	if !commandtools.Exists(d.run, name) {
		sid, _, err := d.consoleUser()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: errorPayload{Code: "NO_USER",
				Message: "the scheduled task is missing and no one is logged on at the console to run it as: " + err.Error()}})
			return
		}
		if err := commandtools.Register(d.run, name, commandtools.ToolExe(d.installDir()), commandtools.ToolArgs(env.Env, dataDir), sid); err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: errorPayload{Code: "RETRY_FAILED", Message: err.Error()}})
			return
		}
	}
	if err := commandtools.Run(d.run, name); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: errorPayload{Code: "RETRY_FAILED", Message: err.Error()}})
		return
	}
	// The job rewrites the file within a second or two; until then readers
	// see it as running so the portal does not show the old failure again.
	// Attempt stays: the job adds one itself when it begins.
	now := d.now().UTC().Format(time.RFC3339)
	_ = commandtools.Write(dataDir, commandtools.Status{
		State:     commandtools.StateRunning,
		Phase:     "Starting the command tools setup",
		StartedAt: now,
		UpdatedAt: now,
		LogPath:   prev.LogPath,
		Attempt:   prev.Attempt,
	})
	writeJSON(w, http.StatusAccepted, commandToolsRetryResponse{Status: "started", TaskName: name, Attempt: prev.Attempt + 1})
}
