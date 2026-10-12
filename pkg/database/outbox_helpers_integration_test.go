//go:build integration
// +build integration

package database_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/testutil"
)

// documentMigrationsDir is the schema-of-record chain that owns outbox,
// outbox_dlq and the drain's definer functions.
const documentMigrationsDir = "../../services/document/migrations"

// newTestJetStream starts a NATS container and binds ONE small stream for
// the given subjects. Raw nats.Connect on purpose: events.ConnectNATS
// provisions DefaultStreams (~240GB reservations, issue #84).
func newTestJetStream(ctx context.Context, t *testing.T, stream string, subjects ...string) (*nats.Conn, nats.JetStreamContext) {
	t.Helper()
	natsURL, natsCleanup, err := testutil.NewNATSContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(natsCleanup)
	nc, err := nats.Connect(natsURL)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	_, err = js.AddStream(&nats.StreamConfig{
		Name:     stream,
		Subjects: subjects,
		Storage:  nats.FileStorage,
		MaxBytes: 16 * 1024 * 1024,
	})
	require.NoError(t, err)
	return nc, js
}

// registerTenants inserts organizations rows (the tenant registry that
// ForEachTenant enumerates) through a BYPASSRLS pool.
func registerTenants(ctx context.Context, t *testing.T, super *pgxpool.Pool, tenants ...uuid.UUID) {
	t.Helper()
	for _, id := range tenants {
		_, err := super.Exec(ctx,
			`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
			id, "t", "t-"+id.String())
		require.NoError(t, err)
	}
}

// grantAppRole gives dms_app the table grants the schema-of-record
// expects (scripts/init-db.sql default privileges). NewProdPostureDB
// grants before the schema exists, so tests re-apply after migrating.
func grantAppRole(ctx context.Context, t *testing.T, super *pgxpool.Pool) {
	t.Helper()
	_, err := super.Exec(ctx, `
		GRANT USAGE ON SCHEMA public TO dms_app;
		GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dms_app;
		GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO dms_app;
	`)
	require.NoError(t, err)
}
