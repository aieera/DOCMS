//go:build integration

// Blueprint §8.1 checklist item: "concurrent_session_limit=2, log in three
// times, oldest session revoked". This test exercises the real code path
// through createSessionInTx — with a real Postgres + miniredis + outbox —
// and asserts both the DB invariant (2 live rows) and the audit invariant
// (1 dms.auth.session.evicted.v1 outbox row). Without this we only know
// the eviction SQL works in isolation; this proves the full tx is coherent.

package service

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/vaultdms/vaultdms/pkg/database"
	"github.com/vaultdms/vaultdms/pkg/testharness"
	"github.com/vaultdms/vaultdms/services/auth/internal/model"
	"github.com/vaultdms/vaultdms/services/auth/internal/repository"
)

func TestConcurrentEviction_LimitTwo_EvictsOldestWithAudit(t *testing.T) {
	h := testharness.NewWithContainers(t,
		testharness.ContainerOptions{Postgres: true},
		testharness.BootPostgres, nil, nil,
	)
	h.RunMigrations(t, "../../../../services/document/migrations")
	h.RunMigrations(t, "../../../../services/auth/migrations")

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	ctx := context.Background()
	tenantID := h.SeedTenant(t, "A")

	// Set concurrent_session_limit = 2 via the new organizations column
	// (migration 000002). This is exactly the knob a tenant would flip
	// in /admin/tenant/security.
	require.NoError(t, h.WithTenantTx(tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE organizations SET concurrent_session_limit = 2 WHERE id = $1`, tenantID)
		return err
	}))

	// Seed a real user so the FK on sessions(user_id) holds.
	userID, _ := uuid.NewV7()
	require.NoError(t, h.WithTenantTx(tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO users (tenant_id, id, email, password_hash, display_name, role, status, created_at)
			VALUES ($1, $2, $3, 'bcrypt$dummy', 'Alice', 'user', 'active', now())
		`, tenantID, userID, "alice@example.com")
		return err
	}))

	// Wire a real Service — mirrors production main.go. Each repo has
	// its own factory; Repository.New(pool) is the pool holder only.
	svc := New(Config{
		Pool:     h.Pool,
		Redis:    rdb,
		Users:    repository.NewUserRepo(),
		Sessions: repository.NewSessionRepo(),
		APIKeys:  repository.NewAPIKeyRepo(),
		Outbox:   database.NewOutboxRepository(),
		Logger:   zerolog.Nop(),
	})

	user := &model.User{
		ID:          userID,
		TenantID:    tenantID,
		Email:       "alice@example.com",
		DisplayName: "Alice",
		Role:        model.Role("user"),
		Status:      model.StatusActive,
	}

	// Three "logins" with staggered clocks so created_at ordering is
	// deterministic. Same pattern Login() uses; we go direct to
	// createSessionInTx so the test stays narrow.
	base := time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		step := base.Add(time.Duration(i) * time.Second)
		svc.now = func() time.Time { return step }
		err := database.WithTenantTx(ctx, h.Pool, tenantID, func(tx pgx.Tx) error {
			_, err := svc.createSessionInTx(ctx, tx, user, "192.168.1.1", "integration-ua")
			return err
		})
		require.NoError(t, err, "login %d", i)
	}

	// Invariant A: exactly 2 active sessions remain.
	var active int
	require.NoError(t, h.WithTenantTx(tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT count(*) FROM sessions
			WHERE tenant_id = $1 AND user_id = $2 AND revoked_at IS NULL
		`, tenantID, userID).Scan(&active)
	}))
	require.Equal(t, 2, active, "concurrent_session_limit=2 must cap at 2 live rows")

	// Invariant B: one audit event for the eviction, carrying the cap.
	var evictedCount int
	var payload []byte
	require.NoError(t, h.WithTenantTx(tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM outbox
			WHERE tenant_id = $1 AND event_type = 'dms.auth.session.evicted.v1'
		`, tenantID).Scan(&evictedCount); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			SELECT payload FROM outbox
			WHERE tenant_id = $1 AND event_type = 'dms.auth.session.evicted.v1'
			LIMIT 1
		`, tenantID).Scan(&payload)
	}))
	require.Equal(t, 1, evictedCount, "exactly one eviction should have fired (3rd login → evict oldest)")
	require.Contains(t, string(payload), `"reason":"concurrent_limit"`)
	require.Contains(t, string(payload), `"concurrent_cap":2`)
}
