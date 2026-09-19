package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUpdateFile_PreservesUnknownKeysAndWritesNew(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	t.Setenv("STONKAGENTS_CONFIG_PATH", path)
	t.Setenv("STONKAGENTS_TRACKER_URL", "https://tracker.example.test") // must not leak into the file
	src := "daemon_port: 7841\ndata_dir: /tmp/old\nfuture_key: keep-me\ntracker_url: https://tracker.dev.stonkagents.com\n"
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}

	err := UpdateFile(func(doc map[string]any) error {
		doc["data_dir"] = "/tmp/new"
		doc["upload_cap_mbps"] = 10
		AppendUniqueString(doc, "cors_allowed_origins", "https://a.example")
		AppendUniqueString(doc, "cors_allowed_origins", "https://a.example")
		AppendUniqueString(doc, "cors_allowed_origins", "https://b.example")
		return nil
	})
	if err != nil {
		t.Fatalf("UpdateFile: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DataDir != "/tmp/new" || cfg.UploadCapMbps != 10 {
		t.Errorf("cfg = %+v", cfg)
	}
	if len(cfg.CORSAllowedOrigins) != 2 || cfg.CORSAllowedOrigins[1] != "https://b.example" {
		t.Errorf("cors_allowed_origins = %v", cfg.CORSAllowedOrigins)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) == "" || !strings.Contains(string(raw), "future_key: keep-me") {
		t.Errorf("unknown key dropped: %s", raw)
	}
	if strings.Contains(string(raw), "tracker.example.test") {
		t.Errorf("env-derived tracker URL leaked into file: %s", raw)
	}
	// POSIX mode bits are not meaningful on Windows (os.Stat reports 0666).
	if info, _ := os.Stat(path); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("perm = %o", info.Mode().Perm())
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("temp file left behind")
	}
}

func TestUpdateFile_MissingFile(t *testing.T) {
	t.Setenv("STONKAGENTS_CONFIG_PATH", filepath.Join(t.TempDir(), "none.yaml"))
	if err := UpdateFile(func(map[string]any) error { return nil }); err == nil {
		t.Error("expected error for missing config")
	}
}

func TestValidate_NegativeCaps(t *testing.T) {
	cfg := DefaultConfig()
	cfg.UploadCapMbps = -1
	if err := cfg.Validate(); err == nil {
		t.Error("negative cap accepted")
	}
}
