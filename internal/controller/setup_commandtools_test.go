package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stonkagents/agent/internal/commandtools"
	"github.com/stonkagents/agent/internal/installenv"
)

// schtasksFake records calls and keeps a set of registered task names.
type schtasksFake struct {
	calls   []string
	tasks   map[string]bool
	runErr  error
	xmlSeen string
}

func (f *schtasksFake) run(name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args[0]+" "+args[len(args)-1])
	switch args[0] {
	case "/Query":
		if !f.tasks[args[2]] {
			return []byte("ERROR: The system cannot find the file specified."), errors.New("exit status 1")
		}
	case "/Create":
		raw, err := os.ReadFile(args[len(args)-1])
		if err != nil {
			return nil, err
		}
		f.xmlSeen = string(raw)
		f.tasks[args[4]] = true
	case "/Run":
		if f.runErr != nil {
			return []byte("ERROR: The task will not run because the user is not logged on."), f.runErr
		}
	}
	return []byte("SUCCESS"), nil
}

func newCommandToolsServer(t *testing.T) (*Server, string, *schtasksFake) {
	t.Helper()
	s, err := NewServer("1.0.0-test")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	dataDir := filepath.Join(t.TempDir(), "data")
	fake := &schtasksFake{tasks: map[string]bool{}}
	s.commandTools = commandToolsDeps{
		dataDir:     func() string { return dataDir },
		installDir:  func() string { return `C:\Program Files\StonkAgents` },
		run:         fake.run,
		consoleUser: func() (string, string, error) { return "S-1-5-21-9-8-7-1001", `PC\owner`, nil },
		probe:       func(string) bool { return true },
		now:         func() time.Time { return time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC) },
		supported:   true,
	}
	return s, dataDir, fake
}

func getCommandTools(t *testing.T, s *Server) (int, commandToolsReport) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/setup/command-tools", nil)
	req.RemoteAddr = "127.0.0.1:50000"
	w := serve(s, req)
	var report commandToolsReport
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
			t.Fatalf("decode %s: %v", w.Body.String(), err)
		}
	}
	return w.Code, report
}

func TestCommandToolsStatus_NotStartedWithoutFile(t *testing.T) {
	s, _, _ := newCommandToolsServer(t)
	code, report := getCommandTools(t, s)
	if code != http.StatusOK || report.State != commandtools.StateNotStarted {
		t.Fatalf("code %d report %+v", code, report)
	}
	if report.TaskName != commandtools.TaskName(installenv.Current()) || !report.Supported {
		t.Fatalf("report %+v", report)
	}
	if report.GatewayRunning {
		t.Fatal("the gateway is not probed before the job ever ran")
	}
}

func TestCommandToolsStatus_ReportsFileAndPresence(t *testing.T) {
	s, dataDir, _ := newCommandToolsServer(t)
	cli := filepath.Join(t.TempDir(), "stonkagents.cmd")
	if err := os.WriteFile(cli, []byte("@echo off"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := commandtools.Write(dataDir, commandtools.Status{State: commandtools.StateReady, Phase: "Command tools ready", CLIPath: cli, Attempt: 1, FinishedAt: "2026-09-17T11:59:00Z"}); err != nil {
		t.Fatal(err)
	}
	code, report := getCommandTools(t, s)
	if code != http.StatusOK || report.State != commandtools.StateReady || !report.CLIPresent || !report.GatewayRunning {
		t.Fatalf("code %d report %+v", code, report)
	}
	// JSON keys are snake_case for the portal.
	req := httptest.NewRequest(http.MethodGet, "/setup/command-tools", nil)
	req.RemoteAddr = "127.0.0.1:50000"
	body := serve(s, req).Body.String()
	for _, key := range []string{`"state"`, `"cli_path"`, `"cli_present"`, `"gateway_running"`, `"task_name"`, `"finished_at"`} {
		if !strings.Contains(body, key) {
			t.Errorf("body lacks %s: %s", key, body)
		}
	}
}

func TestCommandToolsStatus_StaleRunningIsFailed(t *testing.T) {
	s, dataDir, _ := newCommandToolsServer(t)
	if err := commandtools.Write(dataDir, commandtools.Status{State: commandtools.StateRunning, Phase: "Downloading the command tools", UpdatedAt: "2026-09-17T11:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	_, report := getCommandTools(t, s)
	if report.State != commandtools.StateFailed || !strings.Contains(report.Error, "stopped before it finished") {
		t.Fatalf("report %+v", report)
	}
}

func TestCommandToolsStatus_LoopbackOnly(t *testing.T) {
	s, _, _ := newCommandToolsServer(t)
	req := httptest.NewRequest(http.MethodGet, "/setup/command-tools", nil)
	req.RemoteAddr = "10.0.0.5:50000"
	if w := serve(s, req); w.Code != http.StatusForbidden {
		t.Fatalf("code %d", w.Code)
	}
}

func TestCommandToolsRetry_RunsExistingTask(t *testing.T) {
	s, dataDir, fake := newCommandToolsServer(t)
	name := commandtools.TaskName(installenv.Current())
	fake.tasks[name] = true
	if err := commandtools.Write(dataDir, commandtools.Status{State: commandtools.StateFailed, Error: "npm exited 1", Attempt: 2, LogPath: `C:\t\stonkagents-tools.log`}); err != nil {
		t.Fatal(err)
	}
	w := serve(s, setupPost("/setup/command-tools/retry", ""))
	if w.Code != http.StatusAccepted {
		t.Fatalf("code %d body %s", w.Code, w.Body.String())
	}
	var resp commandToolsRetryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != "started" || resp.TaskName != name || resp.Attempt != 3 {
		t.Fatalf("resp %+v", resp)
	}
	if got := strings.Join(fake.calls, "; "); got != "/Query "+name+"; /Run "+name {
		t.Fatalf("calls: %s", got)
	}
	// Readers see the job as running at once; the attempt is left for the job to bump.
	st := commandtools.Read(dataDir)
	if st.State != commandtools.StateRunning || st.Attempt != 2 || st.Error != "" || st.LogPath != `C:\t\stonkagents-tools.log` || st.UpdatedAt != "2026-09-17T12:00:00Z" {
		t.Fatalf("placeholder %+v", st)
	}
}

func TestCommandToolsRetry_RegistersMissingTaskForConsoleUser(t *testing.T) {
	s, dataDir, fake := newCommandToolsServer(t)
	w := serve(s, setupPost("/setup/command-tools/retry", "{}"))
	if w.Code != http.StatusAccepted {
		t.Fatalf("code %d body %s", w.Code, w.Body.String())
	}
	name := commandtools.TaskName(installenv.Current())
	if got := strings.Join(fake.calls, "; "); !strings.HasPrefix(got, "/Query "+name+"; /Create ") || !strings.HasSuffix(got, "; /Run "+name) {
		t.Fatalf("calls: %s", got)
	}
	for _, want := range []string{
		"<UserId>S-1-5-21-9-8-7-1001</UserId>",
		`<Command>C:\Program Files\StonkAgents\stonkagents-tools.exe</Command>`,
		"--background --data-dir &#34;" + dataDir + "&#34;",
	} {
		if !strings.Contains(fake.xmlSeen, want) {
			t.Errorf("task xml lacks %s:\n%s", want, fake.xmlSeen)
		}
	}
}

func TestCommandToolsRetry_RefusedWhileRunning(t *testing.T) {
	s, dataDir, fake := newCommandToolsServer(t)
	if err := commandtools.Write(dataDir, commandtools.Status{State: commandtools.StateRunning, Phase: "Downloading the command tools", UpdatedAt: "2026-09-17T11:59:30Z"}); err != nil {
		t.Fatal(err)
	}
	w := serve(s, setupPost("/setup/command-tools/retry", ""))
	if w.Code != http.StatusConflict || decodeErr(t, w).Code != "ALREADY_RUNNING" {
		t.Fatalf("code %d body %s", w.Code, w.Body.String())
	}
	if len(fake.calls) != 0 {
		t.Fatalf("schtasks called: %v", fake.calls)
	}
}

func TestCommandToolsRetry_Errors(t *testing.T) {
	// No console user and no task: nothing to run it as.
	s, _, _ := newCommandToolsServer(t)
	s.commandTools.consoleUser = func() (string, string, error) { return "", "", errors.New("no console session") }
	if w := serve(s, setupPost("/setup/command-tools/retry", "")); w.Code != http.StatusInternalServerError || decodeErr(t, w).Code != "NO_USER" {
		t.Fatalf("no user: %d %s", w.Code, w.Body.String())
	}

	// schtasks /Run failing (user logged off) is relayed with its output.
	s, _, fake := newCommandToolsServer(t)
	fake.tasks[commandtools.TaskName(installenv.Current())] = true
	fake.runErr = errors.New("exit status 1")
	w := serve(s, setupPost("/setup/command-tools/retry", ""))
	if w.Code != http.StatusInternalServerError || decodeErr(t, w).Code != "RETRY_FAILED" || !strings.Contains(decodeErr(t, w).Message, "not logged on") {
		t.Fatalf("run failure: %d %s", w.Code, w.Body.String())
	}

	// Not Windows: 501.
	s, _, _ = newCommandToolsServer(t)
	s.commandTools.supported = false
	if w := serve(s, setupPost("/setup/command-tools/retry", "")); w.Code != http.StatusNotImplemented {
		t.Fatalf("unsupported: %d %s", w.Code, w.Body.String())
	}

	// The setup gate applies: no mutation headers, no retry.
	s, _, fake = newCommandToolsServer(t)
	req := httptest.NewRequest(http.MethodPost, "/setup/command-tools/retry", nil)
	req.RemoteAddr = "127.0.0.1:50000"
	if w := serve(s, req); w.Code != http.StatusForbidden || len(fake.calls) != 0 {
		t.Fatalf("no headers: %d %s", w.Code, w.Body.String())
	}
}
