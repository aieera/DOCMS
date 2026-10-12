//go:build integration
// +build integration

// H-13 (2026-10-12 review) — there was no way to put dead-lettered events
// back on the drain. ReplayDLQ (`dms-admin outbox replay-dlq`) moves
// outbox_dlq rows back into outbox with a fresh retry budget.
package database_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
)

func TestReplayDLQ_MovesRowsBackWithZeroAttempts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)
	pool := newSuperOutboxPool(ctx, t)

	tenantA, tenantB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	registerTenants(ctx, t, pool, tenantA, tenantB)
	dead := func(tenant uuid.UUID, age time.Duration) uuid.UUID {
		id := uuid.Must(uuid.NewV7())
		_, err := pool.Exec(ctx, `
			INSERT INTO outbox_dlq (id, tenant_id, event_type, aggregate_type, aggregate_id, payload,
			                        created_at, actor_name, ip_address, attempts, last_error, dead_lettered_at)
			VALUES ($1, $2, 'dms.replay.created.v1', 'document', gen_random_uuid(), '{"k":"v"}',
			        now() - $3::interval, 'alice', '10.0.0.1', 5, 'js publish: nats: timeout', now() - $3::interval)`,
			id, tenant, age.String())
		require.NoError(t, err)
		return id
	}
	a1 := dead(tenantA, 3*time.Hour)
	a2 := dead(tenantA, 2*time.Hour)
	a3 := dead(tenantA, time.Hour)
	b1 := dead(tenantB, time.Hour)

	countIn := func(table string, id uuid.UUID) int {
		var n int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE id = $1`, id).Scan(&n))
		return n
	}

	// Dry run lists but changes nothing.
	got, err := database.ReplayDLQ(ctx, pool, database.ReplayDLQOptions{TenantID: tenantA, Limit: 10, DryRun: true})
	require.NoError(t, err)
	require.Len(t, got, 3)
	require.Equal(t, 1, countIn("outbox_dlq", a1))
	require.Zero(t, countIn("outbox", a1))

	// Tenant filter + limit: the two oldest of tenant A move back.
	got, err = database.ReplayDLQ(ctx, pool, database.ReplayDLQOptions{TenantID: tenantA, Limit: 2})
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.ElementsMatch(t, []uuid.UUID{a1, a2}, []uuid.UUID{got[0].ID, got[1].ID})
	for _, id := range []uuid.UUID{a1, a2} {
		require.Zero(t, countIn("outbox_dlq", id), "replayed row must leave outbox_dlq")
		var (
			attempts  int
			published bool
			lastErr   *string
			next      *time.Time
			actor     *string
			ip        *string
		)
		require.NoError(t, pool.QueryRow(ctx, `
			SELECT attempts, published, last_error, next_attempt_at, actor_name, host(ip_address)
			FROM outbox WHERE id = $1`, id).Scan(&attempts, &published, &lastErr, &next, &actor, &ip))
		require.Zero(t, attempts, "replayed row gets a fresh retry budget")
		require.False(t, published)
		require.Nil(t, lastErr)
		require.Nil(t, next, "replayed row is due immediately")
		require.Equal(t, "alice", *actor, "audit context survives the round trip")
		require.Equal(t, "10.0.0.1", *ip)
	}
	require.Equal(t, 1, countIn("outbox_dlq", a3), "rows past the limit stay dead-lettered")
	require.Equal(t, 1, countIn("outbox_dlq", b1), "other tenants are untouched by a tenant-scoped replay")

	// No tenant: every registered tenant, oldest first.
	got, err = database.ReplayDLQ(ctx, pool, database.ReplayDLQOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, 1, countIn("outbox", a3))
	require.Equal(t, 1, countIn("outbox", b1))
	var left int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM outbox_dlq`).Scan(&left))
	require.Zero(t, left)
}
