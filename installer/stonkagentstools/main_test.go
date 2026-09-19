package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stonkagents/agent/installer/msiprogress"
	"github.com/stonkagents/agent/internal/installenv"
)

func TestArgs(t *testing.T) {
	if got := envArg([]string{"--env", "stg", "--background"}, "prd"); got != "stg" {
		t.Errorf("envArg = %q", got)
	}
	if got := envArg([]string{"--env=dev"}, "prd"); got != "dev" {
		t.Errorf("envArg = %q", got)
	}
	if got := envArg([]string{"--background"}, "stg"); got != "stg" {
		t.Errorf("envArg fallback = %q", got)
	}
	if !hasFlag([]string{"--env", "dev", "--background"}, "--background") || hasFlag([]string{"--env", "dev"}, "--background") {
		t.Error("hasFlag")
	}
	if got := flagValue([]string{"--env", "dev", "--data-dir", `C:\d\data`}, "--data-dir"); got != `C:\d\data` {
		t.Errorf("flagValue = %q", got)
	}
	if got := flagValue([]string{`--data-dir=C:\d\data`}, "--data-dir"); got != `C:\d\data` {
		t.Errorf("flagValue = %q", got)
	}
}

func TestLogName(t *testing.T) {
	if got := logName(installenv.ForEnv("prd")); got != "stonkagents-tools.log" {
		t.Errorf("prd log = %q", got)
	}
	if got := logName(installenv.ForEnv("stg")); got != "stonkagents-tools-stg.log" {
		t.Errorf("stg log = %q", got)
	}
}

func TestDataDirFor(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("data folder paths are Windows paths")
	}
	env := installenv.ForEnv("dev")
	dir := t.TempDir()
	config := filepath.Join(dir, "config.yaml")

	// The argument wins, quotes stripped.
	if got := dataDirFor(env, `"C:\Users\x\Documents\.stonkagents-dev\data"`, config); got != `C:\Users\x\Documents\.stonkagents-dev\data` {
		t.Errorf("flag: %q", got)
	}
	// Then config.yaml, in setuphelper's single-quoted form ('' for a quote, %% for a percent).
	if err := os.WriteFile(config, []byte("peer_id: \"\"\ndata_dir: 'C:\\Users\\o''brien\\Documents\\.stonkagents-dev\\data'\ngateway_url: \"http://127.0.0.1:19001\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := dataDirFor(env, "", config); got != `C:\Users\o'brien\Documents\.stonkagents-dev\data` {
		t.Errorf("config: %q", got)
	}
	// Then the profile default.
	t.Setenv("USERPROFILE", `C:\Users\x`)
	if got := dataDirFor(env, "", filepath.Join(dir, "missing.yaml")); got != `C:\Users\x\Documents\.stonkagents-dev\data` {
		t.Errorf("default: %q", got)
	}
}

func TestUnquoteYAML(t *testing.T) {
	cases := map[string]string{
		`'C:\a''b\100%%'`: `C:\a'b\100%`,
		`"C:\\a\"b"`:      `C:\a"b`,
		`C:\plain`:        `C:\plain`,
	}
	for in, want := range cases {
		if got := unquoteYAML(in); got != want {
			t.Errorf("unquoteYAML(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestLastLine(t *testing.T) {
	l := &lastLine{}
	for _, line := range []string{
		"[10:00:00 +0s] post-install starting",
		"npm http fetch GET 200 https://registry.npmjs.org/x.tgz",
		"[10:00:12 +12s] phase: Waiting for the StonkAgents service",
		"Daemon not yet listening. Retrying in 5s (1/30)...",
		"PROGRESS\tWaiting, 0:12 elapsed",
		"",
		"Daemon not ready after 150s.",
		"[10:02:42 +162s] daemon wait done",
	} {
		l.Observe(line)
	}
	if got := l.Text(); got != "daemon wait done" {
		t.Errorf("Text = %q", got)
	}
	l.Reset()
	l.Observe("npm WARN deprecated foo")
	if got := l.Text(); got != "" {
		t.Errorf("after reset: %q", got)
	}
}

func TestScriptError(t *testing.T) {
	exit := exitError(t, 3)
	if got := scriptError(exit, "StonkAgents onboard failed (exit 3)."); got != "exited 3: StonkAgents onboard failed (exit 3)." {
		t.Errorf("scriptError = %q", got)
	}
	if got := scriptError(errors.New("boom"), ""); got != "boom" {
		t.Errorf("scriptError = %q", got)
	}
	// A phase that hit its ceiling names the step it was on and the way out.
	if got := scriptError(msiprogress.ErrTimedOut, "npm install -g stonkagents"); got != "The setup took too long and was stopped. Retry from the portal (last step: npm install -g stonkagents)" {
		t.Errorf("scriptError timeout = %q", got)
	}
	if got := scriptError(msiprogress.ErrTimedOut, ""); got != "The setup took too long and was stopped. Retry from the portal" {
		t.Errorf("scriptError timeout = %q", got)
	}
}

// The ceilings sit above the script's own per-command caps (about 35 minutes
// end to end) and below the scheduled task's two hour limit.
func TestPhaseLimits(t *testing.T) {
	if scriptLimit < 40*time.Minute || scriptLimit > 2*time.Hour {
		t.Errorf("scriptLimit = %v", scriptLimit)
	}
	if gatewayLimit < 5*time.Minute || gatewayLimit > 30*time.Minute {
		t.Errorf("gatewayLimit = %v", gatewayLimit)
	}
}

// exitError builds a real *exec.ExitError with the given code by running the
// test binary itself.
func exitError(t *testing.T, code int) error {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperExit")
	cmd.Env = append(os.Environ(), "STONKAGENTS_TOOLS_HELPER_EXIT="+string(rune('0'+code)))
	err := cmd.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != code {
		t.Fatalf("helper exit: %v", err)
	}
	return err
}

func TestHelperExit(t *testing.T) {
	if v := os.Getenv("STONKAGENTS_TOOLS_HELPER_EXIT"); v != "" {
		os.Exit(int(v[0] - '0'))
	}
}
