// Package: internal/config
// Feature: F-009 (CLI Tool)
// Story: US-009-01 (CLI Tool Scaffolding)
// Purpose: Configuration file management for StonkAgents

package config

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/stonkagents/agent/internal/installenv"
	"gopkg.in/yaml.v3"
)

const (
	envPrivateKey = "STONKAGENTS_PRIVATE_KEY"

	// envTrackerURL is the StonkAgents tracker URL override.
	envTrackerURL = "STONKAGENTS_TRACKER_URL"
)

// DefaultTrackerURL is the single source of truth for the tracker host.
const DefaultTrackerURL = "https://tracker.dev.stonkagents.com"

// Config represents the StonkAgents configuration
// SECURITY: Sensitive fields (private_key) MUST be loaded from environment variables
type Config struct {
	// Identity
	PeerID      string `yaml:"peer_id"`
	PrivateKey  string `yaml:"-"` // NEVER from YAML - must be from env: STONKAGENTS_PRIVATE_KEY
	PublicKey   string `yaml:"public_key"`
	DisplayName string `yaml:"display_name"` // F-032: Human-readable peer name

	// Daemon
	DaemonHost string `yaml:"daemon_host"`
	DaemonPort int    `yaml:"daemon_port"`

	// Network
	BootstrapPeers []string `yaml:"bootstrap_peers"`
	TrackerURL     string   `yaml:"tracker_url"` // Audit F2: Configurable tracker URL
	// TrackerHeartbeatIntervalSeconds is how often the daemon re-registers with the tracker (default: 120).
	// Set in config to change without code change; minimum 1.
	TrackerHeartbeatIntervalSeconds int `yaml:"tracker_heartbeat_interval_seconds"`

	// Gateway (optional) — StonkAgents/OpenClaw gateway URL for stateless ask (POST /api/v1/ask).
	// If empty and AskUseTracker is false, ask endpoint returns 503. Token can be set via STONKAGENTS_GATEWAY_TOKEN env.
	GatewayURL   string `yaml:"gateway_url"`
	GatewayToken string `yaml:"-"` // From env STONKAGENTS_GATEWAY_TOKEN only, never from YAML
	// AskUseTracker: when true, POST /api/v1/ask proxies to tracker POST /api/v1/agents/completions (deducts credits, returns 402 if insufficient).
	AskUseTracker bool `yaml:"ask_use_tracker"`

	// Storage
	DataDir string `yaml:"data_dir"`

	// Bandwidth caps in megabits per second (0 = unlimited). Written by the
	// portal's setup surface (POST /api/v1/setup/bandwidth) and applied live
	// to the upload/download throttles.
	UploadCapMbps   float64 `yaml:"upload_cap_mbps,omitempty"`
	DownloadCapMbps float64 `yaml:"download_cap_mbps,omitempty"`

	// CORSAllowedOrigins are extra browser origins (scheme://host[:port])
	// allowed to call the daemon, on top of the built-in portal origins and
	// CORS_ALLOWED_ORIGINS. Written by POST /api/v1/setup/origin.
	CORSAllowedOrigins []string `yaml:"cors_allowed_origins,omitempty"`

	// LiveAgentDownloadAPIKey (env STONKAGENTS_LIVE_AGENT_DOWNLOAD_API_KEY only, never YAML) gates the live-agent download endpoint.
	// When non-empty, requests must send X-Live-Agent-Key or Authorization: Bearer matching this value.
	LiveAgentDownloadAPIKey string `yaml:"-"`

	// Autopilot is the board watcher policy (see autopilot.go). Written by
	// POST /api/v1/setup/autopilot; defaults to off.
	Autopilot AutopilotConfig `yaml:"autopilot,omitempty"`
}

// DefaultDataDirName is the per-user data folder of the environment this
// process runs in (.stonkagents, or .stonkagents-dev / .stonkagents-stg).
// use, see resolveDataPath.
func DefaultDataDirName() string {
	return installenv.Current().DataDirName
}

// DefaultConfig returns the default configuration
func DefaultConfig() *Config {
	home, _ := os.UserHomeDir()
	return &Config{
		DaemonHost:                      "localhost",
		DaemonPort:                      7841,
		BootstrapPeers:                  []string{},
		TrackerURL:                      DefaultTrackerURL, // Override via config.yaml, STONKAGENTS_TRACKER_URL
		TrackerHeartbeatIntervalSeconds: 120,               // 2 minutes
		DataDir:                         filepath.Join(home, DefaultDataDirName(), "data"),
	}
}

// resolveDataPath follows a path into the data folder through the
// and returns the path to open. Every path the config layer opens goes
// through it, so it runs before any file in the folder is touched. A failed
// rename is logged and the legacy folder stays in use.
func resolveDataPath(path string) string {
	resolved, err := installenv.ResolveDataPath(path, log.Printf)
	if err != nil {
		log.Printf("data folder: %v (keeping the legacy folder for now)", err)
	}
	return resolved
}

// ConfigPath returns the path to the config file
func ConfigPath() (string, error) {
	if override := os.Getenv("STONKAGENTS_CONFIG_PATH"); override != "" {
		return resolveDataPath(override), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	return resolveDataPath(filepath.Join(home, DefaultDataDirName(), "config.yaml")), nil
}

// Load reads the config file from ~/.stonkagents/config.yaml (STONKAGENTS_CONFIG_PATH overrides)
// SECURITY: Sensitive fields are loaded from environment variables, never from YAML
func Load() (*Config, error) {
	configPath, err := ConfigPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("config file not found at %s: run the installer, or create it first", configPath)
		}
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	// Expand ~ to home directory in DataDir (shells don't expand ~ in config files)
	if strings.HasPrefix(cfg.DataDir, "~/") || cfg.DataDir == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to expand ~ in data_dir: %w", err)
		}
		cfg.DataDir = filepath.Join(home, cfg.DataDir[1:])
	}
	// A data_dir that still names the legacy folder follows the rename; the
	// file is updated so the next start does not resolve it again.
	if resolved := resolveDataPath(cfg.DataDir); resolved != cfg.DataDir {
		cfg.DataDir = resolved
		if err := UpdateFile(func(doc map[string]any) error {
			doc["data_dir"] = resolved
			return nil
		}); err != nil {
			log.Printf("data folder: config keeps the old data_dir (%v)", err)
		}
	}
	// Load secrets from environment variables (NEVER from config file)
	cfg.PrivateKey = loadPrivateKeyFromEnv()
	cfg.GatewayToken = strings.Trim(os.Getenv("STONKAGENTS_GATEWAY_TOKEN"), "\"")

	// Optional override: tracker URL from env (e.g. set in daemon.env or at install
	// time). STONKAGENTS_TRACKER_URL wins.
	if u := TrackerURLFromEnv(); u != "" {
		cfg.TrackerURL = u
	}

	// Optional override: enable ask→tracker (credit-backed portal chat) via env without editing config.yaml
	if v := strings.TrimSpace(strings.ToLower(os.Getenv("STONKAGENTS_ASK_USE_TRACKER"))); v == "true" || v == "1" {
		cfg.AskUseTracker = true
	}

	cfg.LiveAgentDownloadAPIKey = strings.TrimSpace(os.Getenv("STONKAGENTS_LIVE_AGENT_DOWNLOAD_API_KEY"))

	if cfg.PrivateKey == "" {
		secrets := loadSecretsFromFile()
		if key := loadPrivateKeyFromSecrets(secrets); key != "" {
			cfg.PrivateKey = key
		}
	}

	// Private key is optional for clients, required for daemons
	// Validation happens at daemon startup

	// Validate config values (Audit F1: Config Validation)
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	return &cfg, nil
}

// TrackerURLFromEnv returns the tracker URL override from the environment.
// Returns "" when STONKAGENTS_TRACKER_URL is not set.
func TrackerURLFromEnv() string {
	for _, name := range []string{envTrackerURL} {
		if u := strings.TrimSpace(os.Getenv(name)); u != "" {
			return u
		}
	}
	return ""
}

// loadPrivateKeyFromEnv reads the private key from STONKAGENTS_PRIVATE_KEY.
func loadPrivateKeyFromEnv() string {
	return strings.Trim(strings.TrimSpace(os.Getenv(envPrivateKey)), "\"")
}

// loadPrivateKeyFromSecrets reads the private key from a parsed secrets map (key STONKAGENTS_PRIVATE_KEY).
func loadPrivateKeyFromSecrets(secrets map[string]string) string {
	return strings.TrimSpace(secrets[envPrivateKey])
}

func loadSecretsFromFile() map[string]string {
	secrets := map[string]string{}
	path := os.Getenv("STONKAGENTS_SECRETS_PATH")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return secrets
		}
		path = filepath.Join(home, DefaultDataDirName(), "secrets.env")
	}
	path = resolveDataPath(path)

	data, err := os.ReadFile(path)
	if err != nil {
		// Fallback: check for secrets.env in working directory (tester packages)
		data, err = os.ReadFile("secrets.env")
		if err != nil {
			return secrets
		}
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		val = strings.Trim(val, "\"")
		if key != "" {
			secrets[key] = val
		}
	}

	return secrets
}

// Save writes the config to ~/.stonkagents/config.yaml
func (c *Config) Save() error {
	configPath, err := ConfigPath()
	if err != nil {
		return err
	}

	// Ensure directory exists
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Marshal to YAML
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Write to file
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	return nil
}

// Exists checks if the config file exists
func Exists() bool {
	configPath, err := ConfigPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(configPath)
	return err == nil
}

// Validate checks that all config values are valid
// Returns an error if any validation fails
func (c *Config) Validate() error {
	// Validate DataDir
	if c.DataDir == "" {
		return fmt.Errorf("data_dir must not be empty")
	}

	// Validate DaemonPort
	if c.DaemonPort < 1 || c.DaemonPort > 65535 {
		return fmt.Errorf("daemon_port must be in range 1-65535, got %d", c.DaemonPort)
	}

	// Validate TrackerHeartbeatIntervalSeconds (0 = use default 120)
	if c.TrackerHeartbeatIntervalSeconds < 0 {
		return fmt.Errorf("tracker_heartbeat_interval_seconds must be >= 0, got %d", c.TrackerHeartbeatIntervalSeconds)
	}

	if c.UploadCapMbps < 0 || c.DownloadCapMbps < 0 {
		return fmt.Errorf("upload_cap_mbps and download_cap_mbps must be >= 0")
	}

	// Truncate DisplayName to 50 characters if longer (defensive — config files are
	// user-editable). Counted in runes so a multibyte name is never cut mid-character
	// and matches what POST /api/v1/setup/identity accepts.
	if runes := []rune(c.DisplayName); len(runes) > 50 {
		c.DisplayName = string(runes[:50])
	}

	// Autopilot: unset fields take defaults, then the section must be sane. A
	// bad section is a config error like any other (the file is user-editable).
	c.Autopilot.ApplyDefaults()
	if err := c.Autopilot.Validate(); err != nil {
		return err
	}

	return nil
}

// UpdateFile applies fn to the config file as a generic YAML document and
// writes it back (0600). It preserves keys this version does not model and
// never persists env-derived values (unlike Load + Save). Used by the setup
// surface to write data_dir, bandwidth caps and cors_allowed_origins.
func UpdateFile(fn func(doc map[string]any) error) error {
	configPath, err := ConfigPath()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}
	doc := map[string]any{}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	if err := fn(doc); err != nil {
		return err
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	tmp := configPath + ".tmp"
	if err := os.WriteFile(tmp, out, 0600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}
	if err := os.Rename(tmp, configPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("failed to replace config: %w", err)
	}
	return nil
}

// AppendUniqueString appends v to the string list stored under key in doc
// (creating it) unless it is already present. Returns true when appended.
func AppendUniqueString(doc map[string]any, key, v string) bool {
	var list []any
	if existing, ok := doc[key].([]any); ok {
		list = existing
	}
	for _, item := range list {
		if s, ok := item.(string); ok && s == v {
			return false
		}
	}
	doc[key] = append(list, v)
	return true
}
