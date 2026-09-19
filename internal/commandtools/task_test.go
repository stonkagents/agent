package commandtools

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stonkagents/agent/internal/installenv"
)

func TestTaskName(t *testing.T) {
	cases := map[string]string{
		installenv.Prd: "StonkAgents command tools",
		installenv.Stg: "StonkAgents Staging command tools",
		installenv.Dev: "StonkAgents Dev command tools",
	}
	for env, want := range cases {
		if got := TaskName(installenv.ForEnv(env)); got != want {
			t.Errorf("%s: %q, want %q", env, got, want)
		}
	}
}

func TestTaskXML_EscapesAndRunsAsInteractiveUser(t *testing.T) {
	exe := `C:\Program Files\StonkAgents Dev\stonkagents-tools.exe`
	// The MSI's [DATADIR] ends with a backslash, which must not precede the closing quote.
	args := ToolArgs("dev", `C:\Users\me & you\Documents\.stonkagents-dev\data\`)
	xml := TaskXML(exe, args, "S-1-5-21-1-2-3-1001")
	for _, want := range []string{
		"<UserId>S-1-5-21-1-2-3-1001</UserId>",
		"<LogonType>InteractiveToken</LogonType>",
		"<RunLevel>HighestAvailable</RunLevel>",
		"<Command>C:\\Program Files\\StonkAgents Dev\\stonkagents-tools.exe</Command>",
		`<Arguments>--env dev --background --data-dir &#34;C:\Users\me &amp; you\Documents\.stonkagents-dev\data&#34;</Arguments>`,
		"<WorkingDirectory>C:\\Program Files\\StonkAgents Dev</WorkingDirectory>",
		"<Triggers />",
		"<AllowStartOnDemand>true</AllowStartOnDemand>",
		"<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>",
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("xml lacks %s:\n%s", want, xml)
		}
	}
	if strings.Contains(xml, "<Password>") || strings.Contains(xml, "<LogonTrigger>") {
		t.Error("the task must store no password and have no logon trigger")
	}
}

// fakeRunner records schtasks calls and answers per verb.
type fakeRunner struct {
	calls  [][]string
	fail   map[string]error
	exists bool
}

func (f *fakeRunner) run(name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{filepath.Base(name)}, args...))
	verb := args[0]
	if verb == "/Query" && !f.exists {
		return []byte("ERROR: The system cannot find the file specified."), errors.New("exit status 1")
	}
	if err := f.fail[verb]; err != nil {
		return []byte("ERROR: something"), err
	}
	if verb == "/Create" {
		// The XML file must exist while schtasks runs.
		if _, err := os.Stat(args[len(args)-1]); err != nil {
			return nil, err
		}
		f.exists = true
	}
	return []byte("SUCCESS"), nil
}

func TestRegisterRunDelete(t *testing.T) {
	f := &fakeRunner{fail: map[string]error{}}
	if err := Register(f.run, "StonkAgents Dev command tools", `C:\x\stonkagents-tools.exe`, "--env dev", "S-1-5-21-1"); err != nil {
		t.Fatal(err)
	}
	c := f.calls[0]
	if c[0] != "schtasks.exe" || c[1] != "/Create" || c[2] != "/F" || c[3] != "/TN" || c[4] != "StonkAgents Dev command tools" || c[5] != "/XML" {
		t.Fatalf("create call: %v", c)
	}
	if _, err := os.Stat(c[6]); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("task xml %s left behind", c[6])
	}
	if !Exists(f.run, "StonkAgents Dev command tools") {
		t.Fatal("registered task must exist")
	}
	if err := Run(f.run, "StonkAgents Dev command tools"); err != nil {
		t.Fatal(err)
	}
	if last := f.calls[len(f.calls)-1]; last[1] != "/Run" || last[3] != "StonkAgents Dev command tools" {
		t.Fatalf("run call: %v", last)
	}
	if err := Delete(f.run, "StonkAgents Dev command tools"); err != nil {
		t.Fatal(err)
	}
	verbs := []string{}
	for _, c := range f.calls[len(f.calls)-3:] {
		verbs = append(verbs, c[1])
	}
	if strings.Join(verbs, " ") != "/Query /End /Delete" {
		t.Fatalf("delete sequence: %v", verbs)
	}
}

func TestDelete_MissingTaskIsFine(t *testing.T) {
	f := &fakeRunner{fail: map[string]error{"/Delete": errors.New("exit 1")}}
	if err := Delete(f.run, "gone"); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("a missing task needs one /Query only: %v", f.calls)
	}
}

func TestRun_ErrorCarriesSchtasksOutput(t *testing.T) {
	f := &fakeRunner{exists: true, fail: map[string]error{"/Run": errors.New("exit status 1")}}
	err := Run(f.run, "x")
	if err == nil || !strings.Contains(err.Error(), "ERROR: something") {
		t.Fatalf("err = %v", err)
	}
}
