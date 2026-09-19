// Package: internal/daemon/download
// Feature: F-029 (P2P Download Parallelization)
// Story: US-029-P6 (Configuration)
// Purpose: Externalized configuration for download parallelization knobs

package download

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// DownloadConfig holds tunable parameters for parallel download behavior.
// All fields have validated ranges enforced by Validate().
type DownloadConfig struct {
	WorkerCount          int           // Number of concurrent download workers (1-20)
	ChunkLeaseTimeout    time.Duration // How long a chunk lease is held before expiry (10s-120s)
	MaxConcurrentPerPeer int           // Max concurrent chunk requests per peer (1-5)
	RetryBudget          int           // Max retries per chunk before permanent failure (1-10)
	RetryBackoffBase     time.Duration // Base duration for exponential backoff (100ms-10s)
}

// DefaultDownloadConfig returns a DownloadConfig with production-safe defaults.
func DefaultDownloadConfig() *DownloadConfig {
	return &DownloadConfig{
		WorkerCount:          5,
		ChunkLeaseTimeout:    30 * time.Second,
		MaxConcurrentPerPeer: 2,
		RetryBudget:          3,
		RetryBackoffBase:     1 * time.Second,
	}
}

// Validate checks that all config values are within allowed ranges.
func (c *DownloadConfig) Validate() error {
	if c.WorkerCount < 1 || c.WorkerCount > 20 {
		return fmt.Errorf("WorkerCount %d out of range [1, 20]", c.WorkerCount)
	}
	if c.ChunkLeaseTimeout < 10*time.Second || c.ChunkLeaseTimeout > 120*time.Second {
		return fmt.Errorf("ChunkLeaseTimeout %v out of range [10s, 120s]", c.ChunkLeaseTimeout)
	}
	if c.MaxConcurrentPerPeer < 1 || c.MaxConcurrentPerPeer > 5 {
		return fmt.Errorf("MaxConcurrentPerPeer %d out of range [1, 5]", c.MaxConcurrentPerPeer)
	}
	if c.RetryBudget < 1 || c.RetryBudget > 10 {
		return fmt.Errorf("RetryBudget %d out of range [1, 10]", c.RetryBudget)
	}
	if c.RetryBackoffBase < 100*time.Millisecond || c.RetryBackoffBase > 10*time.Second {
		return fmt.Errorf("RetryBackoffBase %v out of range [100ms, 10s]", c.RetryBackoffBase)
	}
	return nil
}

// LoadDownloadConfig creates a config from defaults, overridden by environment variables.
// Returns error if env values are unparseable or the resulting config is invalid.
func LoadDownloadConfig() (*DownloadConfig, error) {
	cfg := DefaultDownloadConfig()

	if v := os.Getenv("DOWNLOAD_WORKER_COUNT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("DOWNLOAD_WORKER_COUNT: %w", err)
		}
		cfg.WorkerCount = n
	}

	if v := os.Getenv("DOWNLOAD_CHUNK_LEASE_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("DOWNLOAD_CHUNK_LEASE_TIMEOUT: %w", err)
		}
		cfg.ChunkLeaseTimeout = d
	}

	if v := os.Getenv("DOWNLOAD_MAX_CONCURRENT_PER_PEER"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("DOWNLOAD_MAX_CONCURRENT_PER_PEER: %w", err)
		}
		cfg.MaxConcurrentPerPeer = n
	}

	if v := os.Getenv("DOWNLOAD_RETRY_BUDGET"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("DOWNLOAD_RETRY_BUDGET: %w", err)
		}
		cfg.RetryBudget = n
	}

	if v := os.Getenv("DOWNLOAD_RETRY_BACKOFF_BASE"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("DOWNLOAD_RETRY_BACKOFF_BASE: %w", err)
		}
		cfg.RetryBackoffBase = d
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("download config validation: %w", err)
	}

	return cfg, nil
}
