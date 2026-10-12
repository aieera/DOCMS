//go:build integration

package repository_test

import (
	"context"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // migrate driver
	_ "github.com/golang-migrate/migrate/v4/source/file"       // migrate source
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
)

// migrateNotificationSchema applies document migration 000001 (the
// notifications / users / organizations schema-of-record) and then the
// notification service's own migrations under
// notification_schema_migrations, as `make migrate-up` does.
func migrateNotificationSchema(t *testing.T, dsn string) {
	t.Helper()
	m, err := migrate.New("file://../../../document/migrations", dsn)
	require.NoError(t, err)
	require.NoError(t, m.Steps(1), "document migration 000001")
	_, _ = m.Close()
	require.NoError(t, database.RunServiceMigrations(dsn, "../../migrations", "notification"),
		"notification migrations")
}

// seedTenantUser inserts an organization and a user through a pool that
// can see past RLS (the superuser), returning their ids.
func seedTenantUser(ctx context.Context, t *testing.T, super *pgxpool.Pool) (uuid.UUID, uuid.UUID) {
	t.Helper()
	tenant, user := uuid.New(), uuid.New()
	_, err := super.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1, 'org', $2)`, tenant, tenant.String())
	require.NoError(t, err)
	_, err = super.Exec(ctx, `INSERT INTO users (tenant_id, id, email, display_name) VALUES ($1, $2, $3, 'u')`,
		tenant, user, user.String()+"@example.com")
	require.NoError(t, err)
	return tenant, user
}
