// Package: internal/daemon/download
// Feature: F-029 (P2P Download Parallelization)
// Story: US-029-P6 (Configuration)
// Purpose: TDD tests for download configuration with env loading and validation

package download

import (
	"os"
	"testing"
	"time"
)

// TestDownloadConfig_DefaultsAreValid - RED test
// Acceptance Criterion: DefaultDownloadConfig produces valid configuration
func TestDownloadConfig_DefaultsAreValid(t *testing.T) {
	cfg := DefaultDownloadConfig()

	if err := cfg.Validate(); err != nil {
		t.Fatalf("DefaultDownloadConfig() should be valid, got: %v", err)
	}

	// Verify specific defaults
	if cfg.WorkerCount != 5 {
		t.Errorf("WorkerCount default: got %d, want 5", cfg.WorkerCount)
	}
	if cfg.ChunkLeaseTimeout != 30*time.Second {
		t.Errorf("ChunkLeaseTimeout default: got %v, want 30s", cfg.ChunkLeaseTimeout)
	}
	if cfg.MaxConcurrentPerPeer != 2 {
		t.Errorf("MaxConcurrentPerPeer default: got %d, want 2", cfg.MaxConcurrentPerPeer)
	}
	if cfg.RetryBudget != 3 {
		t.Errorf("RetryBudget default: got %d, want 3", cfg.RetryBudget)
	}
	if cfg.RetryBackoffBase != 1*time.Second {
		t.Errorf("RetryBackoffBase default: got %v, want 1s", cfg.RetryBackoffBase)
	}
}

// TestDownloadConfig_Validate - RED test
// Acceptance Criterion: Validate rejects out-of-range values
func TestDownloadConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  *DownloadConfig
		wantErr bool
	}{
		{
			name:    "default config valid",
			config:  DefaultDownloadConfig(),
			wantErr: false,
		},
		{
			name: "worker count too low",
			config: &DownloadConfig{
				WorkerCount: 0, ChunkLeaseTimeout: 30 * time.Second,
				MaxConcurrentPerPeer: 2, RetryBudget: 3, RetryBackoffBase: 1 * time.Second,
			},
			wantErr: true,
		},
		{
			name: "worker count too high",
			config: &DownloadConfig{
				WorkerCount: 21, ChunkLeaseTimeout: 30 * time.Second,
				MaxConcurrentPerPeer: 2, RetryBudget: 3, RetryBackoffBase: 1 * time.Second,
			},
			wantErr: true,
		},
		{
			name: "lease timeout too short",
			config: &DownloadConfig{
				WorkerCount: 5, ChunkLeaseTimeout: 5 * time.Second,
				MaxConcurrentPerPeer: 2, RetryBudget: 3, RetryBackoffBase: 1 * time.Second,
			},
			wantErr: true,
		},
		{
			name: "lease timeout too long",
			config: &DownloadConfig{
				WorkerCount: 5, ChunkLeaseTimeout: 121 * time.Second,
				MaxConcurrentPerPeer: 2, RetryBudget: 3, RetryBackoffBase: 1 * time.Second,
			},
			wantErr: true,
		},
		{
			name: "max concurrent per peer too low",
			config: &DownloadConfig{
				WorkerCount: 5, ChunkLeaseTimeout: 30 * time.Second,
				MaxConcurrentPerPeer: 0, RetryBudget: 3, RetryBackoffBase: 1 * time.Second,
			},
			wantErr: true,
		},
		{
			name: "retry budget too high",
			config: &DownloadConfig{
				WorkerCount: 5, ChunkLeaseTimeout: 30 * time.Second,
				MaxConcurrentPerPeer: 2, RetryBudget: 11, RetryBackoffBase: 1 * time.Second,
			},
			wantErr: true,
		},
		{
			name: "retry backoff too short",
			config: &DownloadConfig{
				WorkerCount: 5, ChunkLeaseTimeout: 30 * time.Second,
				MaxConcurrentPerPeer: 2, RetryBudget: 3, RetryBackoffBase: 50 * time.Millisecond,
			},
			wantErr: true,
		},
		{
			name: "all at max boundaries",
			config: &DownloadConfig{
				WorkerCount: 20, ChunkLeaseTimeout: 120 * time.Second,
				MaxConcurrentPerPeer: 5, RetryBudget: 10, RetryBackoffBase: 10 * time.Second,
			},
			wantErr: false,
		},
		{
			name: "all at min boundaries",
			config: &DownloadConfig{
				WorkerCount: 1, ChunkLeaseTimeout: 10 * time.Second,
				MaxConcurrentPerPeer: 1, RetryBudget: 1, RetryBackoffBase: 100 * time.Millisecond,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestLoadDownloadConfig_EnvOverrides - RED test
// Acceptance Criterion: Environment variables override defaults
func TestLoadDownloadConfig_EnvOverrides(t *testing.T) {
	t.Setenv("DOWNLOAD_WORKER_COUNT", "10")
	t.Setenv("DOWNLOAD_RETRY_BUDGET", "5")
	t.Setenv("DOWNLOAD_CHUNK_LEASE_TIMEOUT", "45s")

	cfg, err := LoadDownloadConfig()
	if err != nil {
		t.Fatalf("LoadDownloadConfig failed: %v", err)
	}

	if cfg.WorkerCount != 10 {
		t.Errorf("WorkerCount not loaded from env, got %d want 10", cfg.WorkerCount)
	}
	if cfg.RetryBudget != 5 {
		t.Errorf("RetryBudget not loaded from env, got %d want 5", cfg.RetryBudget)
	}
	if cfg.ChunkLeaseTimeout != 45*time.Second {
		t.Errorf("ChunkLeaseTimeout not loaded from env, got %v want 45s", cfg.ChunkLeaseTimeout)
	}

	// Other fields should have defaults
	if cfg.MaxConcurrentPerPeer != 2 {
		t.Errorf("MaxConcurrentPerPeer should be default, got %d", cfg.MaxConcurrentPerPeer)
	}
	if cfg.RetryBackoffBase != 1*time.Second {
		t.Errorf("RetryBackoffBase should be default, got %v", cfg.RetryBackoffBase)
	}
}

// TestLoadDownloadConfig_InvalidEnv - RED test
// Acceptance Criterion: Invalid env values produce clear errors
func TestLoadDownloadConfig_InvalidEnv(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{
			name: "non-numeric worker count",
			env:  map[string]string{"DOWNLOAD_WORKER_COUNT": "abc"},
		},
		{
			name: "invalid duration format",
			env:  map[string]string{"DOWNLOAD_CHUNK_LEASE_TIMEOUT": "notaduration"},
		},
		{
			name: "out of range after parse",
			env:  map[string]string{"DOWNLOAD_WORKER_COUNT": "0"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clean env first
			for k := range tt.env {
				os.Unsetenv(k)
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			_, err := LoadDownloadConfig()
			if err == nil {
				t.Error("LoadDownloadConfig should fail with invalid env")
			}
		})
	}
}

// TestManager_SetDownloadConfig - RED test
// Acceptance Criterion: Config is injectable via setter (preserves stable constructor)
func TestManager_SetDownloadConfig(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 5)

	// Default config should be set
	manager.mu.RLock()
	defaultConfig := manager.config
	manager.mu.RUnlock()

	if defaultConfig == nil {
		t.Fatal("Manager should have default config after NewManager")
	}
	if defaultConfig.WorkerCount != 5 {
		t.Errorf("Default WorkerCount: got %d, want 5", defaultConfig.WorkerCount)
	}

	// Override via setter
	custom := &DownloadConfig{
		WorkerCount:          10,
		ChunkLeaseTimeout:    60 * time.Second,
		MaxConcurrentPerPeer: 3,
		RetryBudget:          5,
		RetryBackoffBase:     2 * time.Second,
	}
	manager.SetDownloadConfig(custom)

	manager.mu.RLock()
	updatedConfig := manager.config
	manager.mu.RUnlock()

	if updatedConfig.WorkerCount != 10 {
		t.Errorf("Updated WorkerCount: got %d, want 10", updatedConfig.WorkerCount)
	}
	if updatedConfig.ChunkLeaseTimeout != 60*time.Second {
		t.Errorf("Updated ChunkLeaseTimeout: got %v, want 60s", updatedConfig.ChunkLeaseTimeout)
	}
}

// TestCoordinator_LeaseTimeout_FromConfig - RED test
// Acceptance Criterion: Coordinator uses configurable lease timeout instead of hardcoded 30s
func TestCoordinator_LeaseTimeout_FromConfig(t *testing.T) {
	coord := NewCoordinatorWithConfig(10, DefaultMaxPeers, 60*time.Second)

	// Register a peer with all chunks
	coord.RegisterPeer("peer1", []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, nil)

	// Get a chunk — creates a lease
	chunkIdx, err := coord.GetNextChunk()
	if err != nil {
		t.Fatalf("GetNextChunk failed: %v", err)
	}

	// Verify leased with 60s timeout (not 30s)
	coord.mu.RLock()
	expiry, exists := coord.leases[chunkIdx]
	coord.mu.RUnlock()

	if !exists {
		t.Fatal("Chunk should be leased")
	}

	// With 60s timeout, expiry should be at least 59s from now
	remaining := time.Until(expiry)
	if remaining < 59*time.Second {
		t.Errorf("Lease timeout too short: %v remaining (expected ~60s)", remaining)
	}
}
