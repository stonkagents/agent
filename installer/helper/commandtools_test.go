package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/stonkagents/agent/internal/installenv"
)

// recordingRunner records schtasks calls; the XML file handed to /Create is
// read while it still exists.
type recordingRunner struct {
	calls []string
	xml   string
	fail  string // verb that fails
}

func (r *recordingRunner) run(name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, args[0])
	if args[0] == "/Create" {
		raw, err := os.ReadFile(args[len(args)-1])
		if err != nil {
			return nil, err
		}
		r.xml = string(raw)
	}
	if args[0] == r.fail {
		return []byte("ERROR: nope"), errors.New("exit status 1")
	}
	return []byte("SUCCESS"), nil
}

func TestStartCommandTools_RegistersAndRuns(t *testing.T) {
	r := &recordingRunner{}
	var logs []string
	logf := func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
	startCommandTools(r.run, installenv.ForEnv("dev"), `C:\Program Files\StonkAgents Dev`, `C:\Users\x\Documents\.stonkagents-dev\data`, "x", logf)

	if got := strings.Join(r.calls, " "); got != "/Create /Run" {
		t.Fatalf("calls = %q (logs %v)", got, logs)
	}
	for _, want := range []string{
		`<Command>C:\Program Files\StonkAgents Dev\stonkagents-tools.exe</Command>`,
		`--env dev --background --data-dir &#34;C:\Users\x\Documents\.stonkagents-dev\data&#34;`,
		"<LogonType>InteractiveToken</LogonType>",
	} {
		if !strings.Contains(r.xml, want) {
			t.Errorf("task xml lacks %s:\n%s", want, r.xml)
		}
	}
	// On Windows the test runs as an ordinary user, whose token the console
	// lookup cannot read, so the LogonUser fallback is what gets registered;
	// elsewhere the lookup does not exist at all. Either way "x" is the user.
	if runtime.GOOS != "windows" || !strings.Contains(r.xml, "<UserId>S-1-") {
		if !strings.Contains(r.xml, "<UserId>x</UserId>") {
			t.Errorf("task xml lacks the fallback user:\n%s", r.xml)
		}
	}
	if len(logs) == 0 || !strings.Contains(logs[len(logs)-1], "started") {
		t.Errorf("logs = %v", logs)
	}
}

func TestStartCommandTools_NoUserSkips(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the console session user may resolve on a Windows desktop")
	}
	r := &recordingRunner{}
	var logs []string
	startCommandTools(r.run, installenv.ForEnv("prd"), `C:\x`, `C:\d`, "", func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) })
	if len(r.calls) != 0 {
		t.Fatalf("calls = %v", r.calls)
	}
	if len(logs) == 0 || !strings.Contains(logs[len(logs)-1], "skipping") {
		t.Errorf("logs = %v", logs)
	}
}

func TestStartCommandTools_RegisterFailureIsLogged(t *testing.T) {
	r := &recordingRunner{fail: "/Create"}
	var logs []string
	startCommandTools(r.run, installenv.ForEnv("stg"), `C:\x`, `C:\d`, "x", func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) })
	if got := strings.Join(r.calls, " "); got != "/Create" {
		t.Fatalf("calls = %q", got)
	}
	if last := logs[len(logs)-1]; !strings.Contains(last, "non-fatal") || !strings.Contains(last, "ERROR: nope") {
		t.Errorf("logs = %v", logs)
	}
}
