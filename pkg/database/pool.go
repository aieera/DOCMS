// Package database holds the Postgres access primitives used by every
// service: connection pools, per-tenant RLS setup, transaction helpers, the
// transactional outbox, and migration runner.
package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolConfig tunes pgxpool behavior. Zero-valued fields fall back to defaults.
type PoolConfig struct {
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
	HealthCheck     time.Duration
	ConnectTimeout  time.Duration
	// SkipRLSPostureCheck disables the automatic AssertRLSPosture call
	// NewPool runs after Ping. Default is false (check enabled). Set
	// true only for pools that intentionally connect as a BYPASSRLS
	// role (test fixtures, migration tooling, ops scripts). Production
	// service code should never set this — leaving the check on is
	// what guarantees a new service can't silently boot with the
	// wrong role.
	SkipRLSPostureCheck bool
}

// DefaultPoolConfig returns sane production defaults.
func DefaultPoolConfig() PoolConfig {
	return PoolConfig{
		MaxConns:        50,
		MinConns:        5,
		MaxConnLifetime: time.Hour,
		MaxConnIdleTime: 30 * time.Minute,
		HealthCheck:     30 * time.Second,
		ConnectTimeout:  10 * time.Second,
	}
}

// NewPool builds a pgxpool configured from a connection URL plus tuning. It
// verifies the pool with a Ping before returning.
func NewPool(ctx context.Context, databaseURL string, cfg PoolConfig) (*pgxpool.Pool, error) {
	pcfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	if cfg.MaxConns > 0 {
		pcfg.MaxConns = cfg.MaxConns
	}
	if cfg.MinConns >= 0 {
		pcfg.MinConns = cfg.MinConns
	}
	if cfg.MaxConnLifetime > 0 {
		pcfg.MaxConnLifetime = cfg.MaxConnLifetime
	}
	if cfg.MaxConnIdleTime > 0 {
		pcfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	}
	if cfg.HealthCheck > 0 {
		pcfg.HealthCheckPeriod = cfg.HealthCheck
	}
	if cfg.ConnectTimeout > 0 {
		pcfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout
	}

	// BeforeAcquire: cheap liveness check. Dropped connections are replaced
	// transparently by pgxpool.
	pcfg.BeforeAcquire = func(ctx context.Context, c *pgx.Conn) bool {
		return c.Ping(ctx) == nil
	}

	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	// Auto-assert RLS posture so a new service can't silently boot as
	// a BYPASSRLS role. Tests / migration tooling that legitimately
	// need BYPASSRLS opt out via cfg.SkipRLSPostureCheck=true OR set
	// SEDOC_ALLOW_BYPASS_RLS=1 in the environment.
	if !cfg.SkipRLSPostureCheck {
		if err := AssertRLSPosture(pingCtx, pool); err != nil {
			pool.Close()
			return nil, err
		}
	}
	return pool, nil
}
