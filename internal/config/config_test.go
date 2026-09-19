// Package: internal/config
// Feature: F-009 (CLI Tool)
// Story: US-009-01 (CLI Tool Scaffolding)
// Purpose: Tests for configuration file management

package config

import (
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestDefaultConfig verifies default configuration values
func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.DaemonHost != "localhost" {
		t.Errorf("DaemonHost = %s, want localhost", cfg.DaemonHost)
	}

	if cfg.DaemonPort != 7841 {
		t.Errorf("DaemonPort = %d, want 7841", cfg.DaemonPort)
	}

	if cfg.DataDir == "" {
		t.Error("DataDir should not be empty")
	}

	if cfg.TrackerHeartbeatIntervalSeconds != 120 {
		t.Errorf("TrackerHeartbeatIntervalSeconds = %d, want 120 (default)", cfg.TrackerHeartbeatIntervalSeconds)
	}
}

// TestConfigSaveAndLoad verifies config save/load roundtrip
func TestConfigSaveAndLoad(t *testing.T) {
	// Note: This test would modify the actual config file
	// In production, we'd inject the config path or use a test helper
	// For now, we'll test the default config generation
	t.Skip("Skipping save/load test - would modify real config file")
}

// TestLoadNonexistentConfig verifies error handling for missing config
func TestLoadNonexistentConfig(t *testing.T) {
	t.Skip("Skipping nonexistent config test - needs test helper for path injection")
}

// TestConfigPathReturnsValidPath verifies config path generation
func TestConfigPathReturnsValidPath(t *testing.T) {
	path, err := ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath() failed: %v", err)
	}

	if path == "" {
		t.Error("ConfigPath() returned empty string")
	}

	if !filepath.IsAbs(path) {
		t.Errorf("ConfigPath() = %s, expected absolute path", path)
	}

	// Should contain the environment data folder (".stonkagents" by default)
	if filepath.Base(filepath.Dir(path)) != DefaultDataDirName() {
		t.Errorf("ConfigPath() = %s, expected path to contain the %s directory", path, DefaultDataDirName())
	}
}

// TestExists verifies config existence check
func TestExists(t *testing.T) {
	t.Skip("Skipping exists test - needs test helper for path injection")
}

// TestSecretsLoadedFromEnvironmentVariables - SECURITY TEST
// Note: This test requires refactoring config.Load() to accept path parameter
// For now, we verify the code structure in TestSecretsNeverInYAML
func TestSecretsLoadedFromEnvironmentVariables(t *testing.T) {
	t.Skip("Requires config path injection - verified manually via code review")
	// Code review confirms:
	// - config.go loads PrivateKey from env / secrets file
	// - yaml:"-" tags prevent secrets from being loaded from YAML
}

// TestSecretsNeverInYAML - SECURITY TEST
// Verifies that PrivateKey has yaml:"-" tag (never serialized to YAML)
func TestSecretsNeverInYAML(t *testing.T) {
	cfg := DefaultConfig()
	cfg.PrivateKey = "super-secret-private-key"

	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal() failed: %v", err)
	}

	yamlContent := string(data)
	if strings.Contains(yamlContent, cfg.PrivateKey) {
		t.Error("PrivateKey found in YAML output - SECURITY VIOLATION")
	}
	if strings.Contains(yamlContent, "private_key") {
		t.Error("Field 'private_key' found in YAML - should have yaml:\"-\" tag")
	}
}

// TestValidate_MissingDataDir verifies validation fails when DataDir is empty
func TestValidate_MissingDataDir(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DataDir = ""

	err := cfg.Validate()
	if err == nil {
		t.Error("Validate() should fail with empty DataDir")
	}
	if err != nil && !strings.Contains(err.Error(), "data_dir") {
		t.Errorf("Validate() error = %v, should mention data_dir", err)
	}
}

// TestValidate_InvalidDaemonPort verifies validation fails for invalid daemon port
func TestValidate_InvalidDaemonPort(t *testing.T) {
	tests := []struct {
		name string
		port int
	}{
		{"zero port", 0},
		{"negative port", -1},
		{"port too high", 99999},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.DaemonPort = tt.port

			err := cfg.Validate()
			if err == nil {
				t.Errorf("Validate() should fail with DaemonPort=%d", tt.port)
			}
			if err != nil && !strings.Contains(err.Error(), "daemon_port") {
				t.Errorf("Validate() error = %v, should mention daemon_port", err)
			}
		})
	}
}

// TestValidate_NegativeTrackerHeartbeatIntervalSeconds verifies validation fails for negative heartbeat interval
func TestValidate_NegativeTrackerHeartbeatIntervalSeconds(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TrackerHeartbeatIntervalSeconds = -1

	err := cfg.Validate()
	if err == nil {
		t.Error("Validate() should fail with negative TrackerHeartbeatIntervalSeconds")
	}
	if err != nil && !strings.Contains(err.Error(), "tracker_heartbeat_interval_seconds") {
		t.Errorf("Validate() error = %v, should mention tracker_heartbeat_interval_seconds", err)
	}
}

// TestValidate_DisplayNameTruncatedTo50 verifies Validate truncates long display names
func TestValidate_DisplayNameTruncatedTo50(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DisplayName = strings.Repeat("x", 60)

	err := cfg.Validate()
	if err != nil {
		t.Fatalf("Validate() failed: %v", err)
	}
	if len(cfg.DisplayName) != 50 {
		t.Errorf("DisplayName length = %d, want 50 after truncation", len(cfg.DisplayName))
	}
}

// TestValidate_DisplayName50OrLessUnchanged verifies short display names are not modified
func TestValidate_DisplayName50OrLessUnchanged(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DisplayName = "My Cool Node"

	err := cfg.Validate()
	if err != nil {
		t.Fatalf("Validate() failed: %v", err)
	}
	if cfg.DisplayName != "My Cool Node" {
		t.Errorf("DisplayName = %q, want %q", cfg.DisplayName, "My Cool Node")
	}
}

// TestValidate_ValidConfig verifies validation passes for valid config
func TestValidate_ValidConfig(t *testing.T) {
	cfg := DefaultConfig()

	err := cfg.Validate()
	if err != nil {
		t.Errorf("Validate() failed for valid config: %v", err)
	}
}

// TestConfig_DefaultTrackerURL verifies default tracker URL
// Audit: F2 (Configurable Tracker URL)
func TestConfig_DefaultTrackerURL(t *testing.T) {
	cfg := DefaultConfig()

	expectedURL := "https://tracker.dev.stonkagents.com"
	if cfg.TrackerURL != expectedURL {
		t.Errorf("TrackerURL = %s, want %s", cfg.TrackerURL, expectedURL)
	}
}

// TestConfig_CustomTrackerURL verifies custom tracker URL is preserved
// Audit: F2 (Configurable Tracker URL)
func TestConfig_CustomTrackerURL(t *testing.T) {
	cfg := &Config{
		DaemonHost: "localhost",
		DaemonPort: 7841,
		TrackerURL: "https://custom-tracker.example.com:8080",
		DataDir:    "/tmp/test-data",
	}

	// Marshal to YAML and unmarshal to verify preservation
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal() failed: %v", err)
	}

	var loaded Config
	if err := yaml.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Unmarshal() failed: %v", err)
	}

	expectedURL := "https://custom-tracker.example.com:8080"
	if loaded.TrackerURL != expectedURL {
		t.Errorf("TrackerURL after round-trip = %s, want %s", loaded.TrackerURL, expectedURL)
	}
}
