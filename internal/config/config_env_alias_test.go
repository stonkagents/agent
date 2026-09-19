// Package: internal/config
// Purpose: Tests for private key loading from env and secrets (STONKAGENTS_PRIVATE_KEY only)

package config

import (
	"os"
	"testing"
)

// TestLoadPrivateKeyFromEnv_Set verifies that STONKAGENTS_PRIVATE_KEY is read.
func TestLoadPrivateKeyFromEnv_Set(t *testing.T) {
	t.Setenv("STONKAGENTS_PRIVATE_KEY", "new-key-value")

	key := loadPrivateKeyFromEnv()

	if key != "new-key-value" {
		t.Errorf("key = %q, want %q", key, "new-key-value")
	}
}

// TestLoadPrivateKeyFromEnv_Empty verifies empty return when not set.
func TestLoadPrivateKeyFromEnv_Empty(t *testing.T) {
	t.Setenv("STONKAGENTS_PRIVATE_KEY", "")

	key := loadPrivateKeyFromEnv()

	if key != "" {
		t.Errorf("key = %q, want empty string", key)
	}
}

// TestLoadPrivateKeyFromEnv_Quoted verifies quotes are stripped.
func TestLoadPrivateKeyFromEnv_Quoted(t *testing.T) {
	t.Setenv("STONKAGENTS_PRIVATE_KEY", `"quoted-key"`)

	key := loadPrivateKeyFromEnv()

	if key != "quoted-key" {
		t.Errorf("key = %q, want %q (quotes should be stripped)", key, "quoted-key")
	}
}

// TestLoadPrivateKeyFromEnv_Unset verifies empty when var is unset.
func TestLoadPrivateKeyFromEnv_Unset(t *testing.T) {
	os.Unsetenv("STONKAGENTS_PRIVATE_KEY")

	key := loadPrivateKeyFromEnv()

	if key != "" {
		t.Errorf("key = %q, want empty string", key)
	}
}

// TestLoadPrivateKeyFromSecrets_Set verifies secrets file with STONKAGENTS_PRIVATE_KEY.
func TestLoadPrivateKeyFromSecrets_Set(t *testing.T) {
	secrets := map[string]string{
		"STONKAGENTS_PRIVATE_KEY": "secrets-key",
	}

	key := loadPrivateKeyFromSecrets(secrets)

	if key != "secrets-key" {
		t.Errorf("key = %q, want %q", key, "secrets-key")
	}
}

// TestLoadPrivateKeyFromSecrets_Empty verifies empty return when no key in secrets.
func TestLoadPrivateKeyFromSecrets_Empty(t *testing.T) {
	secrets := map[string]string{}

	key := loadPrivateKeyFromSecrets(secrets)

	if key != "" {
		t.Errorf("key = %q, want empty string", key)
	}
}

// TestLoadPrivateKeyFromSecrets_MissingKey verifies empty when key not present.
func TestLoadPrivateKeyFromSecrets_MissingKey(t *testing.T) {
	secrets := map[string]string{"OTHER": "value"}

	key := loadPrivateKeyFromSecrets(secrets)

	if key != "" {
		t.Errorf("key = %q, want empty string", key)
	}
}

// TestTrackerURLFromEnv_Unset verifies "" when neither name is set, so the
// config default (DefaultTrackerURL) applies.
func TestTrackerURLFromEnv_Unset(t *testing.T) {
	t.Setenv("STONKAGENTS_TRACKER_URL", "")

	if got := TrackerURLFromEnv(); got != "" {
		t.Errorf("TrackerURLFromEnv() = %q, want empty string", got)
	}
	if DefaultConfig().TrackerURL != DefaultTrackerURL {
		t.Errorf("DefaultConfig().TrackerURL = %q, want %q", DefaultConfig().TrackerURL, DefaultTrackerURL)
	}
}
