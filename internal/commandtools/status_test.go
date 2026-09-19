package commandtools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRead_MissingFileIsNotStarted(t *testing.T) {
	st := Read(t.TempDir())
	if st.State != StateNotStarted {
		t.Fatalf("state = %q, want %q", st.State, StateNotStarted)
	}
}

func TestRead_UnreadableOrForeignFileIsFailed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(Path(dir), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st := Read(dir); st.State != StateFailed || !strings.Contains(st.Error, "unreadable") {
		t.Fatalf("garbage: %+v", st)
	}
	if err := os.WriteFile(Path(dir), []byte(`{"state":"weird"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if st := Read(dir); st.State != StateFailed || st.Error == "" {
		t.Fatalf("unknown state: %+v", st)
	}
}

func TestWrite_RoundTripsAndLeavesNoTempFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data") // created by Write
	want := Status{State: StateReady, Phase: "Command tools ready", StartedAt: "2026-09-17T10:00:00Z", FinishedAt: "2026-09-17T10:04:00Z", Attempt: 2, CLIPath: `C:\Users\x\AppData\Roaming\npm\stonkagents.cmd`}
	if err := Write(dir, want); err != nil {
		t.Fatal(err)
	}
	if got := Read(dir); got != want {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != StatusFileName {
		t.Fatalf("data folder holds %v, want only %s", entries, StatusFileName)
	}
	raw, _ := os.ReadFile(Path(dir))
	var keys map[string]any
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"state", "phase", "started_at", "finished_at", "attempt", "cli_path"} {
		if _, ok := keys[k]; !ok {
			t.Errorf("json lacks %q: %s", k, raw)
		}
	}
	if _, ok := keys["error"]; ok {
		t.Errorf("empty error must be omitted: %s", raw)
	}
}

func TestEffective_StaleRunningIsFailed(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	fresh := Status{State: StateRunning, Phase: "Downloading", UpdatedAt: now.Add(-30 * time.Second).Format(time.RFC3339)}
	if got := Effective(fresh, now); got.State != StateRunning {
		t.Fatalf("fresh running became %q", got.State)
	}
	stale := Status{State: StateRunning, Phase: "Downloading", UpdatedAt: now.Add(-StaleAfter - time.Second).Format(time.RFC3339)}
	got := Effective(stale, now)
	if got.State != StateFailed || !strings.Contains(got.Error, "stopped before it finished") || got.FinishedAt != stale.UpdatedAt {
		t.Fatalf("stale running: %+v", got)
	}
	// Only started_at known (the first write): same rule.
	onlyStart := Status{State: StateRunning, StartedAt: now.Add(-time.Hour).Format(time.RFC3339)}
	if got := Effective(onlyStart, now); got.State != StateFailed {
		t.Fatalf("stale by started_at: %+v", got)
	}
	// Final states are never touched.
	done := Status{State: StateReady, UpdatedAt: now.Add(-time.Hour).Format(time.RFC3339)}
	if got := Effective(done, now); got != done {
		t.Fatalf("ready changed: %+v", got)
	}
}

func TestTracker_StateMachine(t *testing.T) {
	dir := t.TempDir()
	if err := Write(dir, Status{State: StateFailed, Attempt: 3, Error: "old"}); err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }

	tr := begin(dir, `C:\Temp\stonkagents-tools-dev.log`, 4242, now)
	st := Read(dir)
	if st.State != StateRunning || st.Attempt != 4 || st.PID != 4242 || st.Error != "" || st.LogPath == "" {
		t.Fatalf("after begin: %+v", st)
	}
	if st.StartedAt != "2026-09-17T12:00:00Z" || st.UpdatedAt != st.StartedAt {
		t.Fatalf("timestamps: %+v", st)
	}

	// An unchanged status within WriteInterval is not rewritten.
	clock = clock.Add(2 * time.Second)
	tr.Update("Starting the command tools setup", "")
	if got := Read(dir); got.UpdatedAt != "2026-09-17T12:00:00Z" {
		t.Fatalf("unchanged status rewritten: %+v", got)
	}
	// A change is written at once.
	tr.Update("Downloading the command tools", "12 package files ready")
	if got := Read(dir); got.Phase != "Downloading the command tools" || got.Detail != "12 package files ready" || got.UpdatedAt != "2026-09-17T12:00:02Z" {
		t.Fatalf("change not written: %+v", got)
	}
	// The same status is rewritten once WriteInterval has passed (the heartbeat).
	clock = clock.Add(WriteInterval)
	tr.Update("Downloading the command tools", "12 package files ready")
	if got := Read(dir); got.UpdatedAt != "2026-09-17T12:00:07Z" {
		t.Fatalf("heartbeat missing: %+v", got)
	}

	tr.Found(`C:\npm\stonkagents.cmd`, "OpenClaw Gateway (dev)")
	clock = clock.Add(time.Minute)
	tr.Finish("Command tools ready", "")
	got := Read(dir)
	if got.State != StateReady || got.FinishedAt != "2026-09-17T12:01:07Z" || got.Detail != "" || got.CLIPath == "" || got.GatewayTask == "" {
		t.Fatalf("after finish: %+v", got)
	}
	// Nothing after Finish changes the file.
	tr.Update("late", "late")
	tr.Finish("again", "boom")
	if again := Read(dir); again != got {
		t.Fatalf("finished status changed: %+v", again)
	}
	if tr.Err() != nil {
		t.Fatal(tr.Err())
	}
}

func TestTracker_FinishWithErrorIsFailed(t *testing.T) {
	dir := t.TempDir()
	tr := Begin(dir, "", 1)
	tr.Finish("Command tools setup ended with errors", "npm install exited 1")
	if got := Read(dir); got.State != StateFailed || got.Error != "npm install exited 1" || got.Attempt != 1 {
		t.Fatalf("%+v", got)
	}
}
