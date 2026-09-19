package main

import (
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stonkagents/agent/installer/msiprogress"
	"github.com/stonkagents/agent/internal/installenv"
)

func TestFindCLI(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows PATH and drive letters; filepath.SplitList splits on colons elsewhere")
	}
	appData := filepath.Join("C:", "Users", "me", "AppData", "Roaming")
	npmBin := filepath.Join(appData, "npm")
	pathEnv := filepath.Join("C:", "Windows") + string(filepath.ListSeparator) + filepath.Join("C:", "Program Files", "nodejs")
	has := func(paths ...string) func(string) bool {
		set := map[string]bool{}
		for _, p := range paths {
			set[p] = true
		}
		return func(p string) bool { return set[p] }
	}

	if got := findCLI(pathEnv, appData, has()); got != "" {
		t.Errorf("nothing installed: got %q", got)
	}
	// PATH wins over the npm folder; the current package name wins over the legacy one.
	onPath := filepath.Join("C:", "Program Files", "nodejs", "stonkagents.cmd")
	if got := findCLI(pathEnv, appData, has(onPath, filepath.Join(npmBin, "stonkagents.cmd"))); got != onPath {
		t.Errorf("PATH shim: got %q, want %q", got, onPath)
	}
	// Not on PATH yet (fresh Node install): npm's global bin folder is still found.
	inNpm := filepath.Join(npmBin, "stonkagents.cmd")
	if got := findCLI(pathEnv, appData, has(inNpm)); got != inNpm {
		t.Errorf("npm folder shim: got %q, want %q", got, inNpm)
	}
	if got := findCLI(pathEnv, "", has(inNpm)); got != "" {
		t.Errorf("no APPDATA: got %q", got)
	}
}

func TestArgs(t *testing.T) {
	if got := envArg([]string{"--env", "stg", "--progress"}, "prd"); got != "stg" {
		t.Errorf("envArg = %q", got)
	}
	if got := envArg([]string{"--env=dev"}, "prd"); got != "dev" {
		t.Errorf("envArg = %q", got)
	}
	if got := envArg([]string{"--progress"}, "stg"); got != "stg" {
		t.Errorf("envArg fallback = %q", got)
	}
	if !hasFlag([]string{"--env", "dev", "--progress"}, "--progress") || hasFlag([]string{"--env", "dev"}, "--progress") {
		t.Error("hasFlag")
	}
}

func TestLogName(t *testing.T) {
	if got := logName(installenv.ForEnv("prd")); got != "gateway-setup.log" {
		t.Errorf("prd log = %q", got)
	}
	if got := logName(installenv.ForEnv("dev")); got != "gateway-setup-dev.log" {
		t.Errorf("dev log = %q", got)
	}
}

func TestGatewayFailure(t *testing.T) {
	env := installenv.ForEnv("dev")
	got := gatewayFailure(errors.New("exit status 1"), errors.New("exit status 1"), env)
	if !strings.Contains(got, "port 19001 busy") || !strings.Contains(got, "retry from the portal") || !strings.Contains(got, "gateway-setup-dev.log") {
		t.Errorf("gatewayFailure = %q", got)
	}
	got = gatewayFailure(msiprogress.ErrTimedOut, errors.New("exit status 1"), env)
	if !strings.Contains(got, "took too long") || !strings.Contains(got, "retry from the portal") {
		t.Errorf("gatewayFailure timeout = %q", got)
	}
	if cliLimit < time.Minute || cliLimit > 30*time.Minute {
		t.Errorf("cliLimit = %v", cliLimit)
	}
}
