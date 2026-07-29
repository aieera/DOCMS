//go:build integration
// +build integration

// Task 3 (2026-07-28 task-service design) — shared fixture for repository
// integration tests. Extracts the container+migrations bootstrap
// schema_integration_test.go already established (Task 2) into a single
// setupTaskDB(t) helper so tasks_repo_integration_test.go doesn't
// duplicate it, and so TestTaskSchema_RLSFailsClosedAcrossTenants is
// rewritten on top of it instead of inlining its own dms_app setup.
package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

// setupTaskDB starts an isolated Postgres testcontainer, runs the document
// service's migration chain (owns organizations/users/workspaces/folders/
// documents/tasks) followed by the task service's own chain (000001 no-op
// + 000002 under test), then provisions the dms_app NOBYPASSRLS role the
// same way pkg/database/rls_integration_test.go does.
//
// Returns:
//   - ctx: bound to a 120s timeout, cancelled on test cleanup (via
//     newSchemaFixture)
//   - appPool: connects as dms_app (NOBYPASSRLS) — use this (via
//     database.WithTenantTx) for every repository call under test, so
//     repo tests exercise the same RLS posture production traffic does
//   - superPool: connects as the testcontainer's default BYPASSRLS
//     superuser — use this only for out-of-band seeding (organizations,
//     users, documents) via seedOrgAndUser/seedUser/seedDocument
func setupTaskDB(t *testing.T) (ctx context.Context, appPool *pgxpool.Pool, superPool *pgxpool.Pool) {
	t.Helper()
	ctx, dsn, superPool := newSchemaFixture(t)
	runTaskMigrations(t, dsn)

	_, err := superPool.Exec(ctx, `
		DO $$ BEGIN
		  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'dms_app') THEN
		    CREATE ROLE dms_app LOGIN PASSWORD 'devpassword' NOBYPASSRLS;
		  END IF;
		END $$;
		GRANT USAGE ON SCHEMA public TO dms_app;
		GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dms_app;
		GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO dms_app;
	`)
	require.NoError(t, err)

	appDSN, err := testutil.RewriteDSNUser(dsn, "dms_app", "devpassword")
	require.NoError(t, err)
	appPool, err = database.NewPool(ctx, appDSN, database.DefaultPoolConfig())
	require.NoError(t, err)
	t.Cleanup(appPool.Close)

	return ctx, appPool, superPool
}

// seedUser inserts one additional user row for an already-seeded tenant
// (seedOrgAndUser creates the organization plus its first user; tests
// that need a second/third user for assignee/created-by scenarios call
// this instead of re-seeding the org). Mirrors seedOrgAndUser's use of the
// full user uuid in the email local-part — UUIDv7 is time-ordered, so a
// truncated prefix risks colliding on (tenant_id, email) for ids minted
// milliseconds apart in the same test.
func seedUser(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tenant, user uuid.UUID, displayName string) {
	t.Helper()
	_, err := pool.Exec(ctx, `
		INSERT INTO users (tenant_id, id, email, display_name, role)
		VALUES ($1, $2, $3, $4, 'member')`,
		tenant, user, "u-"+user.String()+"@test.local", displayName)
	require.NoError(t, err)
}
