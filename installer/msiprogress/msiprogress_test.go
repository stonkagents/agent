package msiprogress

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"
)

// newClocked returns a Reporter whose clock the test moves by hand.
func newClocked(out *bytes.Buffer) (*Reporter, func(time.Duration)) {
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	r := New(out)
	r.now = func() time.Time { return now }
	r.start = now
	return r, func(d time.Duration) { now = now.Add(d) }
}

func lines(out *bytes.Buffer) []string {
	s := strings.TrimSpace(out.String())
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func TestElapsed(t *testing.T) {
	cases := map[time.Duration]string{
		0:                              "0:00",
		42 * time.Second:               "0:42",
		102 * time.Second:              "1:42",
		10*time.Minute + 5*time.Second: "10:05",
		time.Hour + 61*time.Second:     "1:01:01",
		-time.Second:                   "0:00",
	}
	for d, want := range cases {
		if got := Elapsed(d); got != want {
			t.Errorf("Elapsed(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestFormat(t *testing.T) {
	got := Format("Downloading the command tools", "213 package files ready", 102*time.Second)
	want := "Downloading the command tools: 213 package files ready, 1:42 elapsed"
	if got != want {
		t.Errorf("Format = %q, want %q", got, want)
	}
	if got := Format("Waiting for the StonkAgents service", "", 12*time.Second); got != "Waiting for the StonkAgents service, 0:12 elapsed" {
		t.Errorf("Format without detail = %q", got)
	}
	if got := Format("", "", 0); got != "Working, 0:00 elapsed" {
		t.Errorf("Format with no phase = %q", got)
	}
}

func TestFormatFitsMaxLen(t *testing.T) {
	long := strings.Repeat("Onboarding the command tools with the daemon peer key ", 3)
	got := Format(long, "213 package files ready", 5*time.Hour)
	if len(got) > MaxLen {
		t.Errorf("line is %d chars: %q", len(got), got)
	}
	if !strings.Contains(got, "...: 213 package files ready, 5:00:00 elapsed") {
		t.Errorf("shortened phase lost its tail: %q", got)
	}
}

func TestNpmProgress(t *testing.T) {
	var p NpmProgress
	if p.Summary() != "" {
		t.Fatalf("empty progress should have no summary, got %q", p.Summary())
	}
	if p.Observe("npm http fetch GET 200 https://registry.npmjs.org/stonkagents 93ms (cache miss)") {
		t.Error("a packument fetch is not a package file")
	}
	if !p.Observe("npm http fetch GET 200 https://registry.npmjs.org/zod/-/zod-4.3.6.tgz 412ms (cache miss)") {
		t.Error("tarball fetch not counted")
	}
	p.Observe("npm http fetch GET 200 https://registry.npmjs.org/ws/-/ws-8.20.0.tgz 90ms (cache miss)")
	if !p.Observe("npm http cache inflight@https://registry.npmjs.org/inflight/-/inflight-1.0.6.tgz 0ms (cache hit)") {
		t.Fatal("a cached package file must count too")
	}
	if got := p.Summary(); got != "3 package files ready" {
		t.Errorf("summary = %q", got)
	}
	// Quiet fetch lines mean npm is unpacking what it fetched.
	fetchedAt := p.lastFetch
	p.now = func() time.Time { return fetchedAt.Add(UnpackAfter) }
	if got := p.Summary(); got != "3 package files ready, unpacking" {
		t.Errorf("summary while quiet = %q", got)
	}
	p.now = nil
	if !p.Observe("added 664 packages in 2m") {
		t.Error("final npm line not recognised")
	}
	if got := p.Summary(); got != "664 packages installed" {
		t.Errorf("summary after install = %q", got)
	}
}

func TestReporterPhasesAndCounter(t *testing.T) {
	var out bytes.Buffer
	r, advance := newClocked(&out)

	r.SetPhase("Starting the command tools setup")
	advance(12 * time.Second)
	r.Observe("[10:00:12 +12s] phase: Waiting for the StonkAgents service")
	advance(30 * time.Second)
	r.Observe("phase: Downloading the command tools")
	advance(2 * time.Second)
	r.Observe("npm http fetch GET 200 https://registry.npmjs.org/zod/-/zod-4.3.6.tgz 412ms (cache miss)")
	r.Observe("npm http fetch GET 200 https://registry.npmjs.org/ws/-/ws-8.20.0.tgz 90ms (cache miss)") // within minGap: dropped
	advance(60 * time.Second)
	r.Observe("npm http fetch GET 200 https://registry.npmjs.org/yaml/-/yaml-2.8.0.tgz 90ms (cache miss)")
	r.Observe("some unrelated output line")
	advance(5 * time.Second)
	r.Tick()
	r.Observe("phase: Connecting the command tools to your agent") // clears the counter

	want := []string{
		Prefix + "Starting the command tools setup, 0:00 elapsed",
		Prefix + "Waiting for the StonkAgents service, 0:12 elapsed",
		Prefix + "Downloading the command tools, 0:42 elapsed",
		Prefix + "Downloading the command tools: 1 package file ready, 0:44 elapsed",
		Prefix + "Downloading the command tools: 3 package files ready, 1:44 elapsed",
		Prefix + "Downloading the command tools: 3 package files ready, 1:49 elapsed",
		Prefix + "Connecting the command tools to your agent, 1:49 elapsed",
	}
	if got := lines(&out); !reflect.DeepEqual(got, want) {
		t.Errorf("lines =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if got := r.Text(); got != "Connecting the command tools to your agent, 1:49 elapsed" {
		t.Errorf("Text = %q", got)
	}
}

func TestReporterOnChangeWithoutOutput(t *testing.T) {
	r, advance := newClocked(nil)
	r.out = nil
	type call struct {
		phase, detail string
		elapsed       time.Duration
	}
	var calls []call
	r.OnChange(func(phase, detail string, elapsed time.Duration) {
		calls = append(calls, call{phase, detail, elapsed})
	})
	r.SetPhase("Downloading the command tools")
	advance(3 * time.Second)
	r.Observe("npm http fetch GET 200 https://registry.npmjs.org/zod/-/zod-4.3.6.tgz 412ms (cache miss)")
	r.Observe("npm http fetch GET 200 https://registry.npmjs.org/ws/-/ws-8.20.0.tgz 90ms (cache miss)") // within minGap: dropped
	advance(2 * time.Second)
	r.Tick()
	want := []call{
		{"Downloading the command tools", "", 0},
		{"Downloading the command tools", "1 package file ready", 3 * time.Second},
		{"Downloading the command tools", "2 package files ready", 5 * time.Second},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("calls = %+v, want %+v", calls, want)
	}
}

func TestParseLine(t *testing.T) {
	cases := map[string]string{
		Prefix + "Installing the gateway task, 0:03 elapsed":                                     "Installing the gateway task",
		Prefix + "Downloading the command tools: 213 package files ready, 1:42 elapsed":          "Downloading the command tools",
		Prefix + "Downloading the command tools: 1 package file ready, unpacking, 1:42 elapsed":  "Downloading the command tools",
		Prefix + "Downloading the command tools: 664 packages installed, 1:02:03 elapsed":        "Downloading the command tools",
		Prefix + "Gateway setup skipped: the command tools are missing, see x.log, 0:01 elapsed": "Gateway setup skipped: the command tools are missing, see x.log",
	}
	for line, want := range cases {
		got, ok := ParseLine(line)
		if !ok || got != want {
			t.Errorf("ParseLine(%q) = %q, %v; want %q", line, got, ok, want)
		}
	}
	if _, ok := ParseLine("npm http fetch GET 200 x.tgz"); ok {
		t.Error("a plain line is not a progress line")
	}
}

func TestReporterNilOutputStillTracks(t *testing.T) {
	r := New(nil)
	r.SetPhase("Quiet")
	r.Tick()
	if got := r.Text(); !strings.HasPrefix(got, "Quiet, ") {
		t.Errorf("Text = %q", got)
	}
}

func TestReporterStartStop(t *testing.T) {
	var out bytes.Buffer
	r := New(&out)
	r.Start()
	r.Start() // idempotent
	r.Stop()
	r.Stop() // idempotent
	r.SetPhase("after stop")
	if got := lines(&out); len(got) != 1 || !strings.HasPrefix(got[0], Prefix+"after stop, ") {
		t.Errorf("lines = %q", got)
	}
}

func TestLineWriterSplitsAndCleans(t *testing.T) {
	var got []string
	lw := &LineWriter{Fn: func(s string) { got = append(got, s) }}
	if _, err := lw.Write([]byte("one\r\ntw")); err != nil {
		t.Fatal(err)
	}
	if _, err := lw.Write([]byte("o\n\x1b[32mgreen\x1b[0m\nlast")); err != nil {
		t.Fatal(err)
	}
	lw.Flush()
	lw.Flush() // no partial line left: must not emit an empty one
	want := []string{"one", "two", "green", "last"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("lines = %q, want %q", got, want)
	}
}

func TestSwallowNeverFails(t *testing.T) {
	n, err := Swallow{}.Write([]byte("abc"))
	if n != 3 || err != nil {
		t.Errorf("Swallow{}.Write = %d, %v", n, err)
	}
}
