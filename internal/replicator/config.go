package replicator

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	TrackerURL                string
	PollInterval              time.Duration
	ReplicaTarget             int
	MaxSourcePeerAge          time.Duration
	RecentAssetsLimit         int
	RecentAssetsLookback      time.Duration
	StateFilePath             string
	StorageSoftLimitBytes     int64
	StorageHardLimitBytes     int64
	DaemonEndpoints           []string
	DownloadTimeout           time.Duration
	DownloadPollInterval      time.Duration
	MinReplicasBeforeEviction int
	// LiveAgentAPIKey sent as X-Live-Agent-Key to POST /api/v1/live-agent/download (must match guardian STONKAGENTS_LIVE_AGENT_DOWNLOAD_API_KEY when set).
	LiveAgentAPIKey string
}

func LoadConfigFromEnv() Config {
	cfg := Config{
		TrackerURL:                strings.TrimSpace(os.Getenv("REPLICATOR_TRACKER_URL")),
		PollInterval:              durationEnv("REPLICATOR_POLL_INTERVAL", 30*time.Second),
		ReplicaTarget:             intEnv("REPLICATOR_REPLICA_TARGET", 2),
		MaxSourcePeerAge:          durationEnv("REPLICATOR_SOURCE_PEER_MAX_AGE", 5*time.Minute),
		RecentAssetsLimit:         intEnv("REPLICATOR_RECENT_LIMIT", 100),
		RecentAssetsLookback:      durationEnv("REPLICATOR_LOOKBACK", 10*time.Minute),
		StateFilePath:             strings.TrimSpace(os.Getenv("REPLICATOR_STATE_FILE")),
		StorageSoftLimitBytes:     bytesEnv("REPLICATOR_STORAGE_SOFT_LIMIT_BYTES", 128*1024*1024*1024),
		StorageHardLimitBytes:     bytesEnv("REPLICATOR_STORAGE_HARD_LIMIT_BYTES", 150*1024*1024*1024),
		DownloadTimeout:           durationEnv("REPLICATOR_DOWNLOAD_TIMEOUT", 30*time.Minute),
		DownloadPollInterval:      durationEnv("REPLICATOR_DOWNLOAD_POLL_INTERVAL", 5*time.Second),
		MinReplicasBeforeEviction: intEnv("REPLICATOR_MIN_REPLICAS_BEFORE_EVICTION", 2),
		LiveAgentAPIKey:           strings.TrimSpace(os.Getenv("REPLICATOR_LIVE_AGENT_API_KEY")),
	}
	if cfg.RecentAssetsLimit <= 0 {
		cfg.RecentAssetsLimit = 100
	}
	if cfg.ReplicaTarget <= 0 {
		cfg.ReplicaTarget = 1
	}
	if cfg.StateFilePath == "" {
		cfg.StateFilePath = "./replicator-state.json"
	}
	rawEndpoints := strings.TrimSpace(os.Getenv("REPLICATOR_DAEMON_ENDPOINTS"))
	if rawEndpoints != "" {
		for _, endpoint := range strings.Split(rawEndpoints, ",") {
			trimmed := strings.TrimSpace(endpoint)
			if trimmed != "" {
				cfg.DaemonEndpoints = append(cfg.DaemonEndpoints, strings.TrimRight(trimmed, "/"))
			}
		}
	}
	return cfg
}

func intEnv(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func durationEnv(key string, fallback time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}

func bytesEnv(key string, fallback int64) int64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}
