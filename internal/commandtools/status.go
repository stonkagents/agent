// Package commandtools is the contract between the Windows installer, the
// background command tools job and the controller.
//
// The command tools (the stonkagents npm CLI, its onboarding and the OpenClaw
// gateway task) used to be installed inside the MSI, which kept the setup
// window open for 5 to 15 minutes on slow disks or behind an antivirus. Since
// 2.6.0 the MSI only registers and starts a one-shot scheduled task,
// "<ProductName> command tools", that runs stonkagents-tools.exe --background as
// the installing user once the installer has finished. The job reports through
// a status file in the environment's data folder (StatusFileName), which the
// controller serves to the portal (GET /setup/command-tools) and re-runs on
// request (POST /setup/command-tools/retry).
package commandtools

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// StatusFileName is the status file inside the environment's data folder.
const StatusFileName = "command-tools.json"

// Job states.
const (
	StateNotStarted = "not_started" // no status file: the job never ran
	StateRunning    = "running"
	StateReady      = "ready"
	StateFailed     = "failed"
)

// StaleAfter is how long a running job may go without touching the status
// file before it is reported as failed: the job writes at least every
// WriteInterval while it lives, so silence means it was killed.
const StaleAfter = 2 * time.Minute

// WriteInterval bounds the time between two writes of an unchanged status.
const WriteInterval = 5 * time.Second

// Status is the status file. Times are RFC 3339 strings so a missing one is
// simply absent from the JSON.
type Status struct {
	State  string `json:"state"`
	Phase  string `json:"phase,omitempty"`  // "Downloading the command tools"
	Detail string `json:"detail,omitempty"` // "213 package files ready"

	StartedAt  string `json:"started_at,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`

	Error   string `json:"error,omitempty"`    // final state only
	LogPath string `json:"log_path,omitempty"` // stonkagents-tools's own log
	Attempt int    `json:"attempt,omitempty"`  // 1 for the install's run, +1 per retry
	PID     int    `json:"pid,omitempty"`

	// Written by the job as it finds things: the CLI shim it installed and the
	// gateway task it registered, so a reader can check they still exist.
	CLIPath     string `json:"cli_path,omitempty"`
	GatewayTask string `json:"gateway_task,omitempty"`
}

// Path is the status file for a data folder.
func Path(dataDir string) string {
	return filepath.Join(dataDir, StatusFileName)
}

// Read loads the status file. A missing file is StateNotStarted, not an
// error; a file that cannot be parsed is StateFailed with the parse error, so
// a half-written or foreign file never hides the job from the portal.
func Read(dataDir string) Status {
	raw, err := os.ReadFile(Path(dataDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Status{State: StateNotStarted}
		}
		return Status{State: StateFailed, Error: "status file unreadable: " + err.Error()}
	}
	var st Status
	if err := json.Unmarshal(raw, &st); err != nil {
		return Status{State: StateFailed, Error: "status file unreadable: " + err.Error()}
	}
	switch st.State {
	case StateRunning, StateReady, StateFailed:
	default:
		st.State = StateFailed
		if st.Error == "" {
			st.Error = "status file has an unknown state"
		}
	}
	return st
}

// Write stores the status atomically: the JSON goes to a temp file next to
// the target, which then replaces it, so a reader never sees a partial file.
func Write(dataDir string, st Status) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	target := Path(dataDir)
	tmp, err := os.CreateTemp(dataDir, StatusFileName+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// Effective is the state a reader should show at now: a running job whose
// file has not moved for StaleAfter is reported failed, since the job writes
// at least every WriteInterval while it lives.
func Effective(st Status, now time.Time) Status {
	if st.State != StateRunning {
		return st
	}
	last := parseTime(st.UpdatedAt)
	if last.IsZero() {
		last = parseTime(st.StartedAt)
	}
	if last.IsZero() || now.Sub(last) <= StaleAfter {
		return st
	}
	st.State = StateFailed
	st.Error = fmt.Sprintf("The setup stopped before it finished (no progress since %s).", last.Local().Format("15:04"))
	st.FinishedAt = st.UpdatedAt
	return st
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// Tracker is what the background job writes through: it keeps the current
// status, rewrites the file whenever the phase or detail changes and at least
// every WriteInterval otherwise, and records the final state once.
type Tracker struct {
	mu      sync.Mutex
	dataDir string
	now     func() time.Time
	st      Status
	written time.Time
	done    bool
	err     error // the last write error, for the log
}

// Begin starts a new attempt: the previous file's attempt count plus one,
// state running. The first write happens here.
func Begin(dataDir, logPath string, pid int) *Tracker {
	return begin(dataDir, logPath, pid, time.Now)
}

func begin(dataDir, logPath string, pid int, now func() time.Time) *Tracker {
	prev := Read(dataDir)
	t := &Tracker{dataDir: dataDir, now: now}
	start := now().UTC().Format(time.RFC3339)
	t.st = Status{
		State:     StateRunning,
		Phase:     "Starting the command tools setup",
		StartedAt: start,
		UpdatedAt: start,
		LogPath:   logPath,
		Attempt:   prev.Attempt + 1,
		PID:       pid,
	}
	t.flush(true)
	return t
}

// Update sets the phase and detail; the file is rewritten when either changed
// or the last write is older than WriteInterval.
func (t *Tracker) Update(phase, detail string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return
	}
	changed := phase != t.st.Phase || detail != t.st.Detail
	t.st.Phase, t.st.Detail = phase, detail
	t.flush(changed)
}

// Found records the CLI shim and the gateway task the job set up (either may
// be empty to leave the previous value).
func (t *Tracker) Found(cliPath, gatewayTask string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if cliPath != "" {
		t.st.CLIPath = cliPath
	}
	if gatewayTask != "" {
		t.st.GatewayTask = gatewayTask
	}
	t.flush(true)
}

// Finish writes the final state: ready when err is empty, failed with err
// otherwise. Later Updates are ignored.
func (t *Tracker) Finish(phase, err string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return
	}
	t.done = true
	t.st.Phase = phase
	t.st.Detail = ""
	t.st.Error = err
	t.st.State = StateReady
	if err != "" {
		t.st.State = StateFailed
	}
	t.st.FinishedAt = t.now().UTC().Format(time.RFC3339)
	t.flush(true)
}

// Status is a copy of the current status.
func (t *Tracker) Status() Status {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.st
}

// Err is the last write error, nil when every write succeeded.
func (t *Tracker) Err() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

// flush writes when forced or when the last write is older than WriteInterval.
func (t *Tracker) flush(force bool) {
	now := t.now()
	if !force && now.Sub(t.written) < WriteInterval {
		return
	}
	t.st.UpdatedAt = now.UTC().Format(time.RFC3339)
	t.written = now
	if err := Write(t.dataDir, t.st); err != nil {
		t.err = err
	}
}
