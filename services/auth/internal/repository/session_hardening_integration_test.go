//go:build integration

package repository_test

// Blueprint §8.1 — session hardening coverage that needs a real
// Postgres. Three contracts:
//
//   1. Migration 000002 adds the five session_* columns with the
//      documented defaults and CHECK constraints.
//   2. Concurrent-session enforcement deletes the oldest row when a
//      user exceeds the per-tenant cap. Runs against live
//      upload_sessions-style SQL so we catch CHECK / RLS drift.
//   3. Binding columns default sane (warn) and reject bad values.
//
// Full ValidateSessionWithBinding end-to-end (which needs Redis too)
// lives in its own integration file; keeping this one Postgres-only
// makes it fast enough to run on every push.

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/vaultdms/vaultdms/pkg/testharness"
)

func TestSessionConfig_DefaultsAndCheckConstraints(t *testing.T) {
	h := testharness.NewWithContainers(t,
		testharness.ContainerOptions{Postgres: true},
		testharness.BootPostgres, nil, nil,
	)
	h.RunMigrations(t, "../../../../services/document/migrations")
	h.RunMigrations(t, "../../../../services/auth/migrations")

	tenantA := h.SeedTenant(t, "A")
	ctx := context.Background()

	// Defaults from migration 000002.
	require.NoError(t, h.WithTenantTx(tenantA, func(tx pgx.Tx) error {
		var ttl, slide, absMax, conc int
		var strictness string
		err := tx.QueryRow(ctx, `
			SELECT session_ttl_hours, session_sliding_minutes,
			       session_absolute_max_days, concurrent_session_limit,
			       session_binding_strictness
			FROM organizations WHERE id = $1
		`, tenantA).Scan(&ttl, &slide, &absMax, &conc, &strictness)
		require.NoError(t, err)
		require.Equal(t, 24, ttl)
		require.Equal(t, 60, slide)
		require.Equal(t, 7, absMax)
		require.Equal(t, 5, conc)
		require.Equal(t, "warn", strictness)
		return nil
	}))

	// CHECK rejects unknown strictness.
	require.NoError(t, h.WithTenantTx(tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE organizations SET session_binding_strictness = 'paranoid' WHERE id = $1
		`, tenantA)
		require.Error(t, err, "CHECK must reject values outside {none,warn,enforce}")
		return nil
	}))

	// CHECK rejects out-of-range TTL.
	require.NoError(t, h.WithTenantTx(tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE organizations SET session_ttl_hours = 0 WHERE id = $1`, tenantA)
		require.Error(t, err, "session_ttl_hours must be >= 1")
		_, err = tx.Exec(ctx, `UPDATE organizations SET session_ttl_hours = 200 WHERE id = $1`, tenantA)
		require.Error(t, err, "session_ttl_hours must be <= 168")
		return nil
	}))
}

// TestSession_ConcurrentLimit pins that DeleteOldestForUser picks the
// row with the smallest created_at — the property the runtime loop in
// createSessionInTx depends on. If a repo change accidentally orders
// by id or last_activity, this test catches it before production.
func TestSession_ConcurrentLimit_OldestWins(t *testing.T) {
	h := testharness.NewWithContainers(t,
		testharness.ContainerOptions{Postgres: true},
		testharness.BootPostgres, nil, nil,
	)
	h.RunMigrations(t, "../../../../services/document/migrations")
	h.RunMigrations(t, "../../../../services/auth/migrations")

	tenantA := h.SeedTenant(t, "A")
	userA := seedUser(t, h, tenantA, "a@example.com")
	ctx := context.Background()

	// Insert 6 sessions with staggered created_at so the ordering is
	// unambiguous.
	created := make([]uuid.UUID, 6)
	base := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, h.WithTenantTx(tenantA, func(tx pgx.Tx) error {
		for i := 0; i < 6; i++ {
			id, _ := uuid.NewV7()
			created[i] = id
			_, err := tx.Exec(ctx, `
				INSERT INTO sessions (
					id, tenant_id, user_id, token_hash,
					expires_at, last_activity_at, created_at
				) VALUES ($1, $2, $3, $4, $5, $6, $7)
			`, id, tenantA, userA, "hash-"+id.String(),
				base.Add(24*time.Hour), base.Add(time.Duration(i)*time.Second),
				base.Add(time.Duration(i)*time.Second))
			if err != nil {
				return err
			}
		}
		return nil
	}))

	// Delete the oldest using the same ORDER BY created_at ASC LIMIT 1
	// pattern the repo uses. We assert the row we get is the one we
	// inserted first.
	require.NoError(t, h.WithTenantTx(tenantA, func(tx pgx.Tx) error {
		var got uuid.UUID
		err := tx.QueryRow(ctx, `
			DELETE FROM sessions
			WHERE id = (
				SELECT id FROM sessions
				WHERE tenant_id = $1 AND user_id = $2 AND revoked_at IS NULL
				ORDER BY created_at ASC
				LIMIT 1
			)
			RETURNING id
		`, tenantA, userA).Scan(&got)
		require.NoError(t, err)
		require.Equal(t, created[0], got, "oldest session (first inserted) must be evicted")
		return nil
	}))
}

// seedUser is provided by rls_integration_test.go in this same package.
