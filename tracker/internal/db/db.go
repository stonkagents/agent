// Package db provides PostgreSQL connection and migration for the tracker.
// Feature: F-007 (Centralized Tracker). Story: US-007-01 (PostgreSQL Schema and Migrations)

package db

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/lib/pq"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// ErrDatabaseURLMissing is returned when DATABASE_URL is not set.
var ErrDatabaseURLMissing = errors.New("DATABASE_URL is required and must be set")

// DefaultMaxConns is the default maximum number of connections in the pool.
const DefaultMaxConns = 25

// Connect creates a pgx pool from DATABASE_URL. Fails if DATABASE_URL is unset.
// Uses DefaultMaxConns to avoid exhausting Postgres connections.
func Connect() (*pgxpool.Pool, error) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return nil, ErrDatabaseURLMissing
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("pgxpool.ParseConfig: %w", err)
	}
	if config.MaxConns <= 0 {
		config.MaxConns = DefaultMaxConns
	}
	// DB_MAX_CONNS overrides pool size (e.g. for high-concurrency deployments)
	if s := os.Getenv("DB_MAX_CONNS"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			config.MaxConns = int32(n)
		}
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		return nil, fmt.Errorf("pgxpool.NewWithConfig: %w", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return pool, nil
}

// RunMigrations runs all pending up migrations from the embedded migrations directory.
// databaseURL must be the same Postgres connection string (e.g. DATABASE_URL).
func RunMigrations(databaseURL string) error {
	if databaseURL == "" {
		return ErrDatabaseURLMissing
	}
	source, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("iofs.New: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", source, databaseURL)
	if err != nil {
		return fmt.Errorf("migrate.NewWithSourceInstance: %w", err)
	}
	defer m.Close()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate.Up: %w", err)
	}
	return nil
}
