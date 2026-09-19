package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAutopilot_DefaultsWhenSectionMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	t.Setenv("STONKAGENTS_CONFIG_PATH", path)
	if err := os.WriteFile(path, []byte("daemon_port: 7841\ndata_dir: /tmp/x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := DefaultAutopilotConfig()
	got := cfg.Autopilot
	if got.Mode != AutopilotOff || got.Enabled() {
		t.Errorf("mode = %q, want off", got.Mode)
	}
	if len(got.Categories) != 1 || got.Categories[0] != AutopilotCategoryRequest {
		t.Errorf("categories = %v", got.Categories)
	}
	if got.DailyCreditCap != want.DailyCreditCap || got.MaxRepliesPerDay != want.MaxRepliesPerDay ||
		got.MinBountyMultiple != want.MinBountyMultiple || got.BalanceFloor != want.BalanceFloor ||
		got.ThreadCooldownHours != want.ThreadCooldownHours || got.MaxPostAgeHours != want.MaxPostAgeHours {
		t.Errorf("defaults not applied: %+v", got)
	}
}

func TestAutopilot_PartialSectionKeepsDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	t.Setenv("STONKAGENTS_CONFIG_PATH", path)
	src := "daemon_port: 7841\ndata_dir: /tmp/x\nautopilot:\n  mode: Suggest\n  categories: []\n  daily_credit_cap: 50\n  instruction: '  help with Go  '\n"
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	a := cfg.Autopilot
	if a.Mode != AutopilotSuggest || !a.Enabled() || a.AutoPosts() {
		t.Errorf("mode = %q", a.Mode)
	}
	if len(a.Categories) != 0 {
		t.Errorf("explicit empty categories must stay empty, got %v", a.Categories)
	}
	if a.DailyCreditCap != 50 || a.MaxRepliesPerDay != AutopilotDefaultMaxRepliesPerDay || a.BalanceFloor != AutopilotDefaultBalanceFloor {
		t.Errorf("partial section = %+v", a)
	}
	if a.Instruction != "help with Go" {
		t.Errorf("instruction = %q", a.Instruction)
	}
}

func TestAutopilot_InvalidSectionRejectsLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	t.Setenv("STONKAGENTS_CONFIG_PATH", path)
	src := "daemon_port: 7841\ndata_dir: /tmp/x\nautopilot:\n  mode: yolo\n"
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "autopilot.mode") {
		t.Errorf("err = %v, want autopilot.mode error", err)
	}
}

func TestAutopilotValidate_Table(t *testing.T) {
	base := func() AutopilotConfig { return DefaultAutopilotConfig() }
	cases := []struct {
		name string
		mut  func(*AutopilotConfig)
		want string // substring of the error, "" = valid
	}{
		{"default valid", func(*AutopilotConfig) {}, ""},
		{"mode case and space", func(a *AutopilotConfig) { a.Mode = " AUTO " }, ""},
		{"bad mode", func(a *AutopilotConfig) { a.Mode = "manual" }, "autopilot.mode"},
		{"bad category", func(a *AutopilotConfig) { a.Categories = []string{"bounty"} }, "autopilot.categories"},
		{"dedupe categories", func(a *AutopilotConfig) { a.Categories = []string{"General", "request", "general"} }, ""},
		{"credit cap zero", func(a *AutopilotConfig) { a.DailyCreditCap = 0 }, "daily_credit_cap"},
		{"credit cap huge", func(a *AutopilotConfig) { a.DailyCreditCap = 1_000_000 }, "daily_credit_cap"},
		{"replies zero", func(a *AutopilotConfig) { a.MaxRepliesPerDay = 0 }, "max_replies_per_day"},
		{"replies huge", func(a *AutopilotConfig) { a.MaxRepliesPerDay = 101 }, "max_replies_per_day"},
		{"multiple below one", func(a *AutopilotConfig) { a.MinBountyMultiple = 0.5 }, "min_bounty_multiple"},
		{"floor zero", func(a *AutopilotConfig) { a.BalanceFloor = 0 }, "balance_floor"},
		{"cooldown huge", func(a *AutopilotConfig) { a.ThreadCooldownHours = 24*30 + 1 }, "thread_cooldown_hours"},
		{"age zero", func(a *AutopilotConfig) { a.MaxPostAgeHours = 0 }, "max_post_age_hours"},
		{"instruction too long", func(a *AutopilotConfig) { a.Instruction = strings.Repeat("x", 501) }, "at most 500"},
		{"instruction 500 ok", func(a *AutopilotConfig) { a.Instruction = strings.Repeat("y", 500) }, ""},
		{"instruction control char", func(a *AutopilotConfig) { a.Instruction = "hi\x00there" }, "control characters"},
		{"instruction newline ok", func(a *AutopilotConfig) { a.Instruction = "line one\nline two\ttab" }, ""},
		{"instruction bad utf8", func(a *AutopilotConfig) { a.Instruction = "\xff" }, "UTF-8"},
		{"office hours ok", func(a *AutopilotConfig) {
			a.OfficeHours = &AutopilotOfficeHours{Start: "09:00", End: "17:30", TZ: "Europe/Stockholm"}
		}, ""},
		{"office hours no tz", func(a *AutopilotConfig) { a.OfficeHours = &AutopilotOfficeHours{Start: "22:00", End: "06:00"} }, ""},
		{"office hours bad start", func(a *AutopilotConfig) { a.OfficeHours = &AutopilotOfficeHours{Start: "9am", End: "17:00"} }, "office_hours.start"},
		{"office hours bad end", func(a *AutopilotConfig) { a.OfficeHours = &AutopilotOfficeHours{Start: "09:00", End: "25:00"} }, "office_hours.end"},
		{"office hours same", func(a *AutopilotConfig) { a.OfficeHours = &AutopilotOfficeHours{Start: "09:00", End: "09:00"} }, "must differ"},
		{"office hours bad tz", func(a *AutopilotConfig) {
			a.OfficeHours = &AutopilotOfficeHours{Start: "09:00", End: "17:00", TZ: "Mars/Olympus"}
		}, "office_hours.tz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := base()
			tc.mut(&a)
			err := a.Validate()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}

	a := base()
	a.Mode = " AUTO "
	a.Categories = []string{"General", "request", "general"}
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	if a.Mode != AutopilotAuto || strings.Join(a.Categories, ",") != "general,request" {
		t.Errorf("normalized = %+v", a)
	}
}

func TestAutopilotOfficeHours_Contains(t *testing.T) {
	utc := time.UTC
	day := &AutopilotOfficeHours{Start: "09:00", End: "17:00", TZ: "UTC"}
	night := &AutopilotOfficeHours{Start: "22:00", End: "06:00", TZ: "UTC"}
	at := func(h, m int) time.Time { return time.Date(2026, 9, 15, h, m, 0, 0, utc) }
	if !day.Contains(at(9, 0)) || !day.Contains(at(16, 59)) || day.Contains(at(17, 0)) || day.Contains(at(8, 59)) {
		t.Error("day window")
	}
	if !night.Contains(at(23, 0)) || !night.Contains(at(2, 0)) || night.Contains(at(6, 0)) || night.Contains(at(12, 0)) {
		t.Error("wrapping window")
	}
	var none *AutopilotOfficeHours
	if !none.Contains(at(3, 0)) {
		t.Error("nil window must be always open")
	}
	// Zone conversion: 09:00 in Stockholm (CEST, UTC+2) is 07:00 UTC.
	sto := &AutopilotOfficeHours{Start: "09:00", End: "17:00", TZ: "Europe/Stockholm"}
	if sto.Contains(at(6, 59)) || !sto.Contains(at(7, 0)) {
		t.Error("tz conversion")
	}
}

func TestAutopilot_UpdateFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	t.Setenv("STONKAGENTS_CONFIG_PATH", path)
	if err := os.WriteFile(path, []byte("daemon_port: 7841\ndata_dir: /tmp/x\nfuture_key: keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pol := DefaultAutopilotConfig()
	pol.Mode = AutopilotBounty
	pol.Categories = []string{"general", "request"}
	pol.MinBountyMultiple = 2.5
	pol.Instruction = "Offer Go help"
	pol.OfficeHours = &AutopilotOfficeHours{Start: "08:00", End: "20:00", TZ: "UTC"}
	if err := UpdateFile(func(doc map[string]any) error {
		doc["autopilot"] = AutopilotToYAML(pol)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Autopilot
	if got.Mode != AutopilotBounty || got.MinBountyMultiple != 2.5 || got.Instruction != "Offer Go help" ||
		strings.Join(got.Categories, ",") != "general,request" || got.OfficeHours == nil || got.OfficeHours.TZ != "UTC" {
		t.Errorf("round trip = %+v", got)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "future_key: keep") || !strings.Contains(string(raw), "thread_cooldown_hours: 24") {
		t.Errorf("file = %s", raw)
	}
}

func TestAutopilotDigest_DefaultsValidationAndYAML(t *testing.T) {
	def := DefaultAutopilotConfig().Digest
	if def.Enabled || def.Weekday != 1 || def.Hour != 9 {
		t.Errorf("default digest = %+v", def)
	}

	// Validate table.
	cases := []struct {
		name string
		d    AutopilotDigest
		want string
	}{
		{"default", def, ""},
		{"sunday midnight", AutopilotDigest{Enabled: true, Weekday: 0, Hour: 0}, ""},
		{"saturday 23", AutopilotDigest{Enabled: true, Weekday: 6, Hour: 23}, ""},
		{"weekday 7", AutopilotDigest{Weekday: 7, Hour: 9}, "digest.weekday"},
		{"weekday negative", AutopilotDigest{Weekday: -1, Hour: 9}, "digest.weekday"},
		{"hour 24", AutopilotDigest{Weekday: 1, Hour: 24}, "digest.hour"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := DefaultAutopilotConfig()
			a.Digest = tc.d
			err := a.Validate()
			if tc.want == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}

	// A section that only says enabled gets Monday 09:00; explicit zeros are kept.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	t.Setenv("STONKAGENTS_CONFIG_PATH", path)
	write := func(src string) AutopilotConfig {
		t.Helper()
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		return cfg.Autopilot
	}
	if got := write("daemon_port: 7841\ndata_dir: /tmp/x\nautopilot:\n  mode: suggest\n  digest:\n    enabled: true\n").Digest; !got.Enabled || got.Weekday != 1 || got.Hour != 9 {
		t.Errorf("enabled only = %+v", got)
	}
	if got := write("daemon_port: 7841\ndata_dir: /tmp/x\nautopilot:\n  mode: off\n  digest:\n    enabled: true\n    weekday: 0\n    hour: 0\n").Digest; !got.Enabled || got.Weekday != 0 || got.Hour != 0 {
		t.Errorf("explicit zeros = %+v", got)
	}
	if got := write("daemon_port: 7841\ndata_dir: /tmp/x\nautopilot:\n  mode: off\n").Digest; got != def {
		t.Errorf("no digest section = %+v", got)
	}
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("daemon_port: 7841\ndata_dir: /tmp/x\nautopilot:\n  digest:\n    weekday: 9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "digest.weekday") {
		t.Errorf("bad weekday load err = %v", err)
	}

	// JSON: a partial object defaults the rest, like YAML.
	var d AutopilotDigest
	if err := json.Unmarshal([]byte(`{"enabled":true,"hour":18}`), &d); err != nil {
		t.Fatal(err)
	}
	if !d.Enabled || d.Weekday != 1 || d.Hour != 18 {
		t.Errorf("json partial = %+v", d)
	}

	// UpdateFile round trip carries the digest.
	pol := DefaultAutopilotConfig()
	pol.Digest = AutopilotDigest{Enabled: true, Weekday: 5, Hour: 17}
	if err := UpdateFile(func(doc map[string]any) error {
		doc["autopilot"] = AutopilotToYAML(pol)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Autopilot.Digest != pol.Digest {
		t.Errorf("round trip digest = %+v", cfg.Autopilot.Digest)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "weekday: 5") || !strings.Contains(string(raw), "hour: 17") {
		t.Errorf("file = %s", raw)
	}
}

func TestAutopilotRelevance_DefaultsValidationAndYAML(t *testing.T) {
	def := DefaultAutopilotConfig()
	if def.RelevanceThreshold != 0.25 || def.RelevanceMode != AutopilotRelevanceSkip || def.RelevanceThresholdByCategory != nil || !def.RelevanceScored() {
		t.Errorf("defaults = %+v", def)
	}
	var zero AutopilotConfig
	zero.ApplyDefaults()
	if zero.RelevanceThreshold != 0.25 || zero.RelevanceMode != AutopilotRelevanceSkip {
		t.Errorf("zero after defaults = %+v", zero)
	}

	cases := []struct {
		name string
		mut  func(*AutopilotConfig)
		want string
	}{
		{"mode note", func(a *AutopilotConfig) { a.RelevanceMode = " Note " }, ""},
		{"mode off", func(a *AutopilotConfig) { a.RelevanceMode = "off" }, ""},
		{"mode bad", func(a *AutopilotConfig) { a.RelevanceMode = "maybe" }, "relevance_mode"},
		{"threshold low", func(a *AutopilotConfig) { a.RelevanceThreshold = 0.05 }, "relevance_threshold must"},
		{"threshold high", func(a *AutopilotConfig) { a.RelevanceThreshold = 1.5 }, "relevance_threshold must"},
		{"threshold one", func(a *AutopilotConfig) { a.RelevanceThreshold = 1 }, ""},
		{"pin ok", func(a *AutopilotConfig) { a.RelevanceThresholdByCategory = map[string]float64{"Request": 0.4} }, ""},
		{"pin bad key", func(a *AutopilotConfig) { a.RelevanceThresholdByCategory = map[string]float64{"weird": 0.4} }, "by_category keys"},
		{"pin bad value", func(a *AutopilotConfig) { a.RelevanceThresholdByCategory = map[string]float64{"request": 0} }, "by_category.request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := DefaultAutopilotConfig()
			tc.mut(&a)
			err := a.Validate()
			if tc.want == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	a := DefaultAutopilotConfig()
	a.RelevanceMode = " Note "
	a.RelevanceThresholdByCategory = map[string]float64{"Request": 0.4}
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	if a.RelevanceMode != AutopilotRelevanceNote || a.RelevanceThresholdByCategory["request"] != 0.4 || !a.RelevanceScored() {
		t.Errorf("normalized = %+v", a)
	}
	if v, ok := a.PinnedRelevanceThreshold("request"); !ok || v != 0.4 {
		t.Error("pinned lookup")
	}
	if _, ok := a.PinnedRelevanceThreshold("general"); ok {
		t.Error("unpinned lookup")
	}
	a.RelevanceMode = AutopilotRelevanceOff
	if a.RelevanceScored() {
		t.Error("off must not score")
	}

	// File round trip and a section without the keys.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	t.Setenv("STONKAGENTS_CONFIG_PATH", path)
	if err := os.WriteFile(path, []byte("daemon_port: 7841\ndata_dir: /tmp/x\nautopilot:\n  mode: suggest\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Autopilot.RelevanceThreshold != 0.25 || cfg.Autopilot.RelevanceMode != "skip" {
		t.Errorf("no keys = %+v", cfg.Autopilot)
	}
	pol := DefaultAutopilotConfig()
	pol.RelevanceThreshold = 0.4
	pol.RelevanceMode = AutopilotRelevanceNote
	pol.RelevanceThresholdByCategory = map[string]float64{"request": 0.6}
	if err := UpdateFile(func(doc map[string]any) error {
		doc["autopilot"] = AutopilotToYAML(pol)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Autopilot
	if got.RelevanceThreshold != 0.4 || got.RelevanceMode != "note" || got.RelevanceThresholdByCategory["request"] != 0.6 {
		t.Errorf("round trip = %+v", got)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "relevance_threshold: 0.4") || !strings.Contains(string(raw), "relevance_mode: note") || !strings.Contains(string(raw), "request: 0.6") {
		t.Errorf("file = %s", raw)
	}
	// An empty pin map is dropped from the file.
	pol.RelevanceThresholdByCategory = map[string]float64{}
	if err := UpdateFile(func(doc map[string]any) error {
		doc["autopilot"] = AutopilotToYAML(pol)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	if strings.Contains(string(raw), "relevance_threshold_by_category") {
		t.Errorf("empty pin map written: %s", raw)
	}
}

func TestAutopilotHeartbeatCategories_Table(t *testing.T) {
	cases := []struct {
		name string
		mode string
		cats []string
		want string
	}{
		{"off is nil", AutopilotOff, []string{"request"}, "<nil>"},
		{"default ticks plus bounty", AutopilotSuggest, []string{"request"}, "bounty,request"},
		{"nothing ticked still bounty", AutopilotAuto, []string{}, "bounty"},
		{"all three sorted", AutopilotBounty, []string{"token-offer", "general", "request"}, "bounty,general,request,token-offer"},
		{"dupes and unknown dropped", AutopilotAuto, []string{"bounty", "general", "general", "weird"}, "bounty,general"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := DefaultAutopilotConfig()
			a.Mode, a.Categories = tc.mode, tc.cats
			got := a.HeartbeatCategories()
			if got == nil {
				if tc.want != "<nil>" {
					t.Fatalf("got nil, want %q", tc.want)
				}
				return
			}
			if s := strings.Join(got, ","); s != tc.want {
				t.Fatalf("got %q, want %q", s, tc.want)
			}
		})
	}
}
