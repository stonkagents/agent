// Package msiprogress turns the output of the setup tools (stonkagents-tools.exe,
// gateway-setup.exe) into one-line progress messages.
//
// A Reporter keeps the current phase ("Downloading the command tools"), the
// npm download counter and the elapsed time. Whenever one of them changes,
// and at least every Interval so the text keeps moving while npm is quiet, it
// prints a fresh line to its writer (prefixed with Prefix, for a parent
// process that relays it: stonkagents-tools.exe reads gateway-setup.exe's lines
// that way) and hands the phase and detail to its change callback (the
// background job writes them to the status file the portal shows, see
// internal/commandtools). Until 2.6.0 the lines went to an MSI custom action
// that showed them in the setup window; the MSI no longer runs the tools.
// Lines stay within MaxLen characters.
package msiprogress

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Prefix marks a progress line on stdout for a parent process that relays
// it. Tab and upper case keep it apart from anything the scripts or npm print.
const Prefix = "PROGRESS\t"

// Interval is how often the current line is repeated (with a fresh elapsed
// time) when nothing else changes.
const Interval = 1 * time.Second

// MaxLen is the longest line the Reporter prints (a status chip or a progress
// label shows it on one line of about 90 characters).
const MaxLen = 90

// minGap rate-limits lines driven by npm's per-file log, which can arrive
// many times a second.
const minGap = time.Second

// PhaseMarker is the text a script prints (optionally after a
// "[HH:mm:ss +Ns] " step prefix) to change the phase shown to the user.
const PhaseMarker = "phase: "

var phaseLine = regexp.MustCompile(`^(?:\[\d\d:\d\d:\d\d \+\d+s\] )?` + PhaseMarker + `(.+)$`)

// Reporter tracks the phase, the npm counter and the elapsed time and prints
// progress lines to out. A nil out disables printing; the rest still works.
type Reporter struct {
	mu       sync.Mutex
	out      io.Writer
	onChange func(phase, detail string, elapsed time.Duration)
	now      func() time.Time
	start    time.Time
	phase    string
	npm      NpmProgress
	lastEmit time.Time
	stop     chan struct{}
	stopped  chan struct{}
}

// New returns a Reporter that prints to out (nil for none). Start begins the
// periodic repeat; the elapsed time counts from New.
func New(out io.Writer) *Reporter {
	r := &Reporter{out: out, now: time.Now}
	r.start = r.now()
	return r
}

// OnChange registers a callback that receives the phase, the detail (the npm
// counter, empty when there is none) and the elapsed time whenever a line is
// due, with the same rate limit as printing. It runs with the Reporter's lock
// held, so it must not call back into the Reporter.
func (r *Reporter) OnChange(fn func(phase, detail string, elapsed time.Duration)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onChange = fn
}

// Start repeats the current line every Interval until Stop.
func (r *Reporter) Start() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stop != nil {
		return
	}
	r.stop = make(chan struct{})
	r.stopped = make(chan struct{})
	go r.loop(r.stop, r.stopped)
}

func (r *Reporter) loop(stop, stopped chan struct{}) {
	defer close(stopped)
	t := time.NewTicker(Interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			r.Tick()
		}
	}
}

// Stop ends the periodic repeat. Safe to call more than once.
func (r *Reporter) Stop() {
	r.mu.Lock()
	stop, stopped := r.stop, r.stopped
	r.stop = nil
	r.mu.Unlock()
	if stop != nil {
		close(stop)
		<-stopped
	}
}

// SetPhase replaces the phase text, clears the npm counter and prints.
func (r *Reporter) SetPhase(text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.phase = strings.TrimSpace(text)
	r.npm = NpmProgress{}
	r.emit(true)
}

// Observe feeds one line of tool output: a phase marker changes the phase,
// an npm download line advances the counter. Other lines are ignored.
func (r *Reporter) Observe(line string) {
	if m := phaseLine.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
		r.SetPhase(m[1])
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.npm.Observe(line) {
		r.emit(false)
	}
}

// Tick prints the current line with a fresh elapsed time.
func (r *Reporter) Tick() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.emit(true)
}

// Text is the current progress line, for example
// "Fetching the command tools: 213 package files ready, 1:42 elapsed".
func (r *Reporter) Text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.text()
}

func (r *Reporter) text() string {
	return Format(r.phase, r.npm.Summary(), r.now().Sub(r.start))
}

// emit prints the current line and notifies the change callback; unforced
// emits are dropped while the last one is younger than minGap.
func (r *Reporter) emit(force bool) {
	if r.out == nil && r.onChange == nil {
		return
	}
	now := r.now()
	if !force && now.Sub(r.lastEmit) < minGap {
		return
	}
	r.lastEmit = now
	if r.out != nil {
		fmt.Fprintln(r.out, Prefix+r.text())
	}
	if r.onChange != nil {
		r.onChange(r.phase, r.npm.Summary(), now.Sub(r.start))
	}
}

// Format builds one progress line from the phase, an optional detail (the
// npm counter) and the elapsed time, shortening the phase so the whole line
// fits MaxLen.
func Format(phase, detail string, elapsed time.Duration) string {
	if phase == "" {
		phase = "Working"
	}
	tail := ", " + Elapsed(elapsed) + " elapsed"
	if detail != "" {
		tail = ": " + detail + tail
	}
	room := MaxLen - len(tail)
	if room < 8 {
		room = 8
	}
	if len(phase) > room {
		phase = strings.TrimSpace(phase[:room-3]) + "..."
	}
	return phase + tail
}

// The npm counter (NpmProgress.Summary) and the elapsed tail, as Format lays them out.
var movingTail = regexp.MustCompile(`(: \d[\d,]* packages? (files? ready(, unpacking)?|installed))?, \d+:\d\d(:\d\d)? elapsed$`)

// ParseLine reads a line another tool printed with Prefix and returns its
// phase without the moving parts (the npm counter and the elapsed tail);
// ok is false for any other line.
func ParseLine(line string) (phase string, ok bool) {
	if !strings.HasPrefix(line, Prefix) {
		return "", false
	}
	text := strings.TrimSpace(strings.TrimPrefix(line, Prefix))
	return strings.TrimSpace(movingTail.ReplaceAllString(text, "")), true
}

// Elapsed formats a duration as m:ss (h:mm:ss from one hour).
func Elapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int(d / time.Second)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s%3600/60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

var (
	// A package file fetched from the registry, or served from the local npm cache
	// ("npm http cache <name>@<url>.tgz 0ms (cache hit)"): both count.
	npmTarballFetch = regexp.MustCompile(`^npm http (?:fetch GET 2\d\d \S+\.tgz |cache \S+\.tgz )`)
	npmAdded        = regexp.MustCompile(`^added (\d+) packages? in `)
)

// NpmProgress counts the package tarballs npm reports downloading when it
// runs with --loglevel=http, the only live signal npm gives on a pipe.
type NpmProgress struct {
	tarballs  int
	installed string
	// lastFetch is when the last package file line arrived; once the fetches
	// stop npm unpacks and links everything it fetched, which produces no
	// output for minutes on a slow disk or behind an antivirus.
	lastFetch time.Time
	now       func() time.Time
}

// UnpackAfter is how long the fetch lines must have been quiet before the
// counter says npm is unpacking.
const UnpackAfter = 5 * time.Second

func (n *NpmProgress) clock() time.Time {
	if n.now != nil {
		return n.now()
	}
	return time.Now()
}

// Observe feeds one output line; it returns true when the line changed the
// summary.
func (n *NpmProgress) Observe(line string) bool {
	if npmTarballFetch.MatchString(line) {
		n.tarballs++
		n.lastFetch = n.clock()
		return true
	}
	if m := npmAdded.FindStringSubmatch(line); m != nil {
		n.installed = m[1] + " packages installed"
		return true
	}
	return false
}

// Summary is the counter text, empty until npm has fetched something:
// "213 package files ready", then "711 package files ready, unpacking" once
// the fetches have been quiet for UnpackAfter, then "664 packages installed".
func (n *NpmProgress) Summary() string {
	if n.installed != "" {
		return n.installed
	}
	if n.tarballs == 0 {
		return ""
	}
	text := strconv.Itoa(n.tarballs) + " package files ready"
	if n.tarballs == 1 {
		text = "1 package file ready"
	}
	if n.clock().Sub(n.lastFetch) >= UnpackAfter {
		text += ", unpacking"
	}
	return text
}

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// LineWriter is an io.Writer that hands complete lines to Fn, without the
// trailing CR/LF and with ANSI colour codes removed. It is safe to share
// between stdout and stderr of one child process.
type LineWriter struct {
	Fn  func(line string)
	mu  sync.Mutex
	buf []byte
}

// Write buffers partial lines and forwards each finished one.
func (l *LineWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		line := string(l.buf[:i])
		l.buf = l.buf[i+1:]
		l.emit(line)
	}
	return len(p), nil
}

// Flush forwards a trailing partial line, if any. Call it once the child
// process has exited.
func (l *LineWriter) Flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.buf) > 0 {
		line := string(l.buf)
		l.buf = nil
		l.emit(line)
	}
}

func (l *LineWriter) emit(line string) {
	line = strings.TrimRight(line, "\r")
	line = ansiEscape.ReplaceAllString(line, "")
	if l.Fn != nil {
		l.Fn(line)
	}
}

// Swallow is an io.Writer that never fails. Run by hand without a console,
// writes to os.Stdout can error; wrapping it keeps io.MultiWriter from
// stopping before the log file gets the same bytes.
type Swallow struct {
	W interface{ Write([]byte) (int, error) }
}

// Write forwards to W and reports success regardless.
func (s Swallow) Write(p []byte) (int, error) {
	if s.W != nil {
		_, _ = s.W.Write(p)
	}
	return len(p), nil
}
