package testutil

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AssertEventPublished waits up to timeout for a message on subject (may
// contain wildcards) whose Ce-Type header matches eventType. Fails the test
// if nothing arrives in time.
func AssertEventPublished(t *testing.T, nc *nats.Conn, subject, eventType string, timeout time.Duration) {
	t.Helper()
	ch := make(chan *nats.Msg, 8)
	sub, err := nc.ChanSubscribe(subject, ch)
	require.NoError(t, err)
	defer func() { _ = sub.Unsubscribe() }()

	deadline := time.After(timeout)
	for {
		select {
		case <-deadline:
			t.Fatalf("no event with type %q received on subject %q within %s", eventType, subject, timeout)
		case m := <-ch:
			if m.Header.Get("Ce-Type") == eventType {
				return
			}
		}
	}
}

// AssertAuditEventCreated checks that an audit row exists matching tenant +
// action. Requires a standard audit table with columns (tenant_id, action).
func AssertAuditEventCreated(t *testing.T, pool *pgxpool.Pool, tenantID uuid.UUID, action string) {
	t.Helper()
	var n int
	err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_events WHERE tenant_id = $1 AND action = $2`,
		tenantID, action,
	).Scan(&n)
	require.NoError(t, err)
	assert.Greater(t, n, 0, "expected audit event for tenant=%s action=%s", tenantID, action)
}

// AssertTenantIsolation inserts one row as tenantA, then selects as tenantB
// and asserts zero rows — proving the RLS policy is effective. The caller
// provides an insertFn and a selectCountFn (both tenant-scoped).
func AssertTenantIsolation(
	t *testing.T,
	tenantA, tenantB uuid.UUID,
	insertAs func(ctx context.Context, tenant uuid.UUID) error,
	countAs func(ctx context.Context, tenant uuid.UUID) (int, error),
) {
	t.Helper()
	ctx := context.Background()

	require.NoError(t, insertAs(ctx, tenantA), "insert as tenantA should succeed")

	n, err := countAs(ctx, tenantB)
	require.NoError(t, err)
	assert.Equal(t, 0, n, "tenantB must not see tenantA's data (RLS broken?)")

	n, err = countAs(ctx, tenantA)
	require.NoError(t, err)
	assert.Greater(t, n, 0, "tenantA should see its own data")
}
