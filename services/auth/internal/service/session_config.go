package service

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/vaultdms/vaultdms/pkg/database"
)

// SessionConfig carries the per-tenant session knobs from migration
// 000002. Defaults mirror the old package-level constants so tenants
// without an explicit override get identical behavior.
type SessionConfig struct {
	TTL                time.Duration
	SlidingThreshold   time.Duration
	AbsoluteMax        time.Duration
	ConcurrentLimit    int
	BindingStrictness  BindingStrictness
}

// DefaultSessionConfig is what we fall back to when the tenant row is
// missing or the settings columns haven't been migrated yet.
func DefaultSessionConfig() SessionConfig {
	return SessionConfig{
		TTL:               SessionTTL,
		SlidingThreshold:  SessionSlidingThreshold,
		AbsoluteMax:       SessionMaxLifetime,
		ConcurrentLimit:   ConcurrentSessionLimit,
		BindingStrictness: BindingWarn,
	}
}

// LoadSessionConfig reads the five session-* columns from organizations.
// Runs on the provided tx when present (to stay inside a caller's
// tenant-scoped transaction); otherwise opens its own short WithTenantTx.
//
// The lookup is best-effort: any error degrades to DefaultSessionConfig
// rather than failing the request. Sessions should keep working during
// a DB blip; losing per-tenant overrides is a strictly safer fallback.
func (s *Service) LoadSessionConfig(ctx context.Context, tenantID uuid.UUID) SessionConfig {
	cfg := DefaultSessionConfig()
	if tenantID == uuid.Nil {
		return cfg
	}
	var (
		ttlHours   int
		slideMin   int
		absDays    int
		concLimit  int
		strictness string
	)
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT session_ttl_hours, session_sliding_minutes,
			       session_absolute_max_days, concurrent_session_limit,
			       session_binding_strictness
			FROM organizations
			WHERE id = $1
		`, tenantID).Scan(&ttlHours, &slideMin, &absDays, &concLimit, &strictness)
	})
	if err != nil {
		return cfg
	}
	cfg.TTL = time.Duration(ttlHours) * time.Hour
	cfg.SlidingThreshold = time.Duration(slideMin) * time.Minute
	cfg.AbsoluteMax = time.Duration(absDays) * 24 * time.Hour
	cfg.ConcurrentLimit = concLimit
	cfg.BindingStrictness = BindingStrictness(strictness)
	return cfg
}
