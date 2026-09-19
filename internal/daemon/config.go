// Package: internal/daemon
// Feature: F-010 (Go Core Daemon)
// Story: US-010-01 (HTTP Server with Health Endpoint)
// Purpose: Daemon-specific configuration

package daemon

import (
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/stonkagents/agent/internal/config"
	"github.com/stonkagents/agent/internal/installenv"
)

// Config holds daemon configuration
type Config struct {
	// Server
	Host    string // Server bind address (default: 127.0.0.1)
	Port    int    // Server port (default: 7841)
	Version string // Build version (injected via ldflags)

	// Logging
	LogDir     string // Directory for daemon logs
	LogMaxSize int64  // Max log file size in bytes (default: 10MB)
	LogBackups int    // Number of log file backups (default: 5)

	// Identity (from global config)
	PeerID      string
	PrivateKey  string
	PublicKey   string
	DisplayName string // F-032: Human-readable peer name (from config.yaml)

	// Network
	TrackerURL               string        // Audit F2: Configurable tracker URL
	TrackerHeartbeatInterval time.Duration // How often to re-register with tracker (default: 2 min)

	// Gateway (optional) — for POST /api/v1/ask when AskUseTracker is false (stateless prompt → response via OpenClaw /v1/chat/completions)
	GatewayURL   string // If empty and !AskUseTracker, ask returns 503
	GatewayToken string // Bearer token for gateway auth (from env)
	// AskUseTracker: when true, /ask proxies to tracker /api/v1/agents/completions (credits deducted, 402 on insufficient)
	AskUseTracker bool

	// Data
	DataDir string

	// Bandwidth caps in Mbps (0 = unlimited); from config.yaml, applied to the
	// upload/download throttles at startup and live via the setup surface.
	UploadCapMbps   float64
	DownloadCapMbps float64

	// CORSAllowedOrigins are extra allowed browser origins from config.yaml.
	CORSAllowedOrigins []string

	// Live agent replication (guardian / stonkagents-replicator)
	LiveAgentDownloadAPIKey string

	// Autopilot is the board watcher policy from config.yaml (autopilot:),
	// rewritten live by POST /api/v1/setup/autopilot.
	Autopilot config.AutopilotConfig
}

// LoadConfig loads daemon configuration from global config
func LoadConfig() (*Config, error) {
	// folder to .stonkagents (2.5.0). The config loader resolves each path it
	// opens through the same helper, so a failed rename only means the legacy
	// folder stays in use until the next start.
	if err := installenv.MigrateLegacyDataDirs(log.Printf); err != nil {
		log.Printf("data folder: %v (keeping the legacy folder for now)", err)
	}

	// Load global config
	globalCfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	// Validate config (Audit F1: Config Validation)
	if err := globalCfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	// Tracker heartbeat interval: from config or default 2 min
	heartbeatInterval := 2 * time.Minute
	if globalCfg.TrackerHeartbeatIntervalSeconds > 0 {
		heartbeatInterval = time.Duration(globalCfg.TrackerHeartbeatIntervalSeconds) * time.Second
	}

	// Build daemon config
	cfg := &Config{
		Host:                     "127.0.0.1", // Localhost only for security
		Port:                     globalCfg.DaemonPort,
		LogDir:                   filepath.Join(globalCfg.DataDir, "logs"),
		LogMaxSize:               10 * 1024 * 1024, // 10MB
		LogBackups:               5,
		PeerID:                   globalCfg.PeerID,
		PrivateKey:               globalCfg.PrivateKey,
		PublicKey:                globalCfg.PublicKey,
		DisplayName:              globalCfg.DisplayName,
		TrackerURL:               globalCfg.TrackerURL,
		TrackerHeartbeatInterval: heartbeatInterval,
		GatewayURL:               strings.TrimSuffix(globalCfg.GatewayURL, "/"),
		GatewayToken:             globalCfg.GatewayToken,
		AskUseTracker:            globalCfg.AskUseTracker,
		DataDir:                  globalCfg.DataDir,
		UploadCapMbps:            globalCfg.UploadCapMbps,
		DownloadCapMbps:          globalCfg.DownloadCapMbps,
		CORSAllowedOrigins:       globalCfg.CORSAllowedOrigins,
		LiveAgentDownloadAPIKey:  globalCfg.LiveAgentDownloadAPIKey,
		Autopilot:                globalCfg.Autopilot,
	}

	return cfg, nil
}

// DaemonLogPath returns the path to the daemon log file
func (c *Config) DaemonLogPath() string {
	return filepath.Join(c.LogDir, "daemon.log")
}

// LibP2PLogPath returns the path to the libp2p log file
func (c *Config) LibP2PLogPath() string {
	return filepath.Join(c.LogDir, "libp2p.log")
}

// Address returns the full server address (host:port)
func (c *Config) Address() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}
