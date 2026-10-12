//go:build integration

package service

import (
	"context"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // migrate driver
	_ "github.com/golang-migrate/migrate/v4/source/file"       // migrate source
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

// migrateAuditSchema applies exactly the schema audit_events needs on a
// fresh database: document migration 000001 (the partitioned table is that
// service's schema-of-record) and then the audit service's own migrations
// under audit_schema_migrations — what `make migrate-up SERVICE=audit`
// produces. The full document chain cannot run standalone (000021 needs the
// intelligence service's tables).
func migrateAuditSchema(t *testing.T, dsn string) {
	t.Helper()
	m, err := migrate.New("file://../../../../services/document/migrations", dsn)
	require.NoError(t, err, "init document migrator")
	require.NoError(t, m.Steps(1), "document migration 000001 (audit_events)")
	_, _ = m.Close()
	require.NoError(t, database.RunServiceMigrations(dsn, "../../migrations", "audit"),
		"audit service migrations")
}

// newAuditPool starts a Postgres container carrying the audit schema and
// returns a superuser pool over it.
func newAuditPool(ctx context.Context, t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn, cleanup, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err, "start postgres container")
	t.Cleanup(cleanup)
	migrateAuditSchema(t, dsn)
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}
