// Package repository: Postgres implementation of PlatformSettingsRepository with in-memory cache.
package repository

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const settingsCacheTTL = 5 * time.Minute

// PlatformSettingsRepository reads platform_settings with an in-memory cache.
type PlatformSettingsRepository struct {
	pool     *pgxpool.Pool
	mu       sync.RWMutex
	cache    map[string]string
	loadedAt time.Time
}

// NewPlatformSettingsRepository creates a new settings repository.
func NewPlatformSettingsRepository(pool *pgxpool.Pool) *PlatformSettingsRepository {
	return &PlatformSettingsRepository{
		pool:  pool,
		cache: make(map[string]string),
	}
}

// Get returns the value for a key, or empty string if not found.
func (r *PlatformSettingsRepository) Get(ctx context.Context, key string) (string, error) {
	if err := r.ensureCache(ctx); err != nil {
		return "", err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cache[key], nil
}

// GetModelCost returns the credit cost for a model. Falls back to defaultCost if not found.
func (r *PlatformSettingsRepository) GetModelCost(ctx context.Context, model string, defaultCost int) (int, error) {
	val, err := r.Get(ctx, "model_cost:"+model)
	if err != nil {
		return defaultCost, err
	}
	if val == "" {
		return defaultCost, nil
	}
	cost, err := strconv.Atoi(val)
	if err != nil {
		return defaultCost, nil
	}
	return cost, nil
}

// GetIntSetting returns the integer value for a key, or defaultVal if not found
// or unparseable. Used for tunable knobs like credit_lamports_rate.
func (r *PlatformSettingsRepository) GetIntSetting(ctx context.Context, key string, defaultVal int) (int, error) {
	val, err := r.Get(ctx, key)
	if err != nil {
		return defaultVal, err
	}
	if val == "" {
		return defaultVal, nil
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return defaultVal, nil
	}
	return n, nil
}

// GetAllowedModels returns the allowed model list from the "allowed_models" setting.
func (r *PlatformSettingsRepository) GetAllowedModels(ctx context.Context) ([]string, error) {
	val, err := r.Get(ctx, "allowed_models")
	if err != nil {
		return nil, err
	}
	if val == "" {
		return nil, nil
	}
	parts := strings.Split(val, ",")
	models := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			models = append(models, p)
		}
	}
	return models, nil
}

// GetDefaultModel returns the default model name.
func (r *PlatformSettingsRepository) GetDefaultModel(ctx context.Context) (string, error) {
	return r.Get(ctx, "default_model")
}

// ensureCache loads all settings into memory if cache is stale.
func (r *PlatformSettingsRepository) ensureCache(ctx context.Context) error {
	r.mu.RLock()
	if time.Since(r.loadedAt) < settingsCacheTTL && len(r.cache) > 0 {
		r.mu.RUnlock()
		return nil
	}
	r.mu.RUnlock()

	rows, err := r.pool.Query(ctx, `SELECT key, value FROM platform_settings`)
	if err != nil {
		// If table doesn't exist yet (pre-migration), return empty cache without error
		if strings.Contains(err.Error(), "does not exist") {
			return nil
		}
		return err
	}
	defer rows.Close()

	newCache := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return err
		}
		newCache[k] = v
	}
	if err := rows.Err(); err != nil {
		return err
	}

	r.mu.Lock()
	r.cache = newCache
	r.loadedAt = time.Now()
	r.mu.Unlock()
	return nil
}
