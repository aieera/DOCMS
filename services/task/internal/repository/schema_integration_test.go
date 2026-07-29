//go:build integration
// +build integration

// Task 2 (2026-07-28 task-service design) — migration 000002 adopts the
// ADR-0068 `tasks` table (created by the document service's migration
// 000033_lightweight_tasks) into the task service's own migration chain:
// adds tasks.deleted_at and creates four relational side tables
// (task_assignees, task_documents, task_comments, task_activity) with RLS,
// plus a one-time backfill of the legacy single-value assignee_id /
// linked_document_id columns into task_assignees / task_documents.
//
// Fixture pattern copied from
// services/document/internal/handler/clause_matches_integration_test.go:
// an isolated Postgres testcontainer, document service migrations applied
// first (owner of organizations/users/documents/tasks), then the task
// service's own migration track applied on top with its own
// x-migrations-table bookkeeping (task_schema_migrations) so its version
// numbers don't collide with document's default schema_migrations track.
// The RLS assertion additionally borrows the dms_app (NOBYPASSRLS) role
// setup from pkg/database/rls_integration_test.go — the testcontainer's
// default user is a BYPASSRLS superuser, so proving "fails closed" needs a
// non-bypass connection.
//
// Run with:
//
//	go test -tags integration -race -run TestTaskSchema ./services/task/internal/repository/
package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

// Migration directories are relative to this file's package directory
// (services/task/internal/repository), which is what `go test` uses as
// the working directory for relative paths.
const (
	documentMigrationsDir = "../../../document/migrations"
	taskMigrationsDir     = "../../migrations"
)

// newSchemaFixture starts an isolated Postgres testcontainer, pre-creates
// document_entities (owned by the intelligence service's migration chain
// but ALTERed by document's 000021+ — the same workaround the clause
// fixture uses so a single-service migration run reaches completion), and
// runs the document service's full migration chain (creates
// organizations/users/workspaces/folders/documents/tasks). Returns the
// superuser (BYPASSRLS) pool, used for out-of-band seeding and for
// assertions that don't need to exercise RLS.
func newSchemaFixture(t *testing.T) (context.Context, string, *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)

	dsn, pgCleanup, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(pgCleanup)

	poolCfg := database.DefaultPoolConfig()
	poolCfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, poolCfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	_, err = pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS document_entities (
			tenant_id    UUID NOT NULL,
			id           UUID NOT NULL DEFAULT gen_random_uuid(),
			version_id   UUID NOT NULL,
			document_id  UUID NOT NULL,
			entity_type  TEXT NOT NULL,
			entity_value TEXT NOT NULL,
			start_offset INT  NOT NULL DEFAULT 0,
			end_offset   INT  NOT NULL DEFAULT 0,
			confidence   REAL NOT NULL DEFAULT 0,
			is_pii       BOOLEAN NOT NULL DEFAULT false,
			detected_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (tenant_id, id)
		)`)
	require.NoError(t, err)

	require.NoError(t, database.RunMigrations(dsn, documentMigrationsDir))

	return ctx, dsn, pool
}

// runTaskMigrations applies the task service's own migration chain
// (000001 no-op scaffold + 000002 under test) using its own
// task_schema_migrations bookkeeping table (database.RunServiceMigrations
// handles merging that query parameter into dsn regardless of whether dsn
// already carries one, e.g. testcontainers' `?sslmode=disable`).
func runTaskMigrations(t *testing.T, dsn string) {
	t.Helper()
	require.NoError(t, database.RunServiceMigrations(dsn, taskMigrationsDir, "task"))
}

// seedOrgAndUser inserts one organization (the tenant) and one user via
// the superuser pool (bypasses RLS, mirrors how ops tooling provisions a
// new tenant before any app-role connection could satisfy the RLS
// WITH CHECK on the first row). Emails use the FULL user uuid, not a
// truncated prefix: UUIDv7 is time-ordered, so two ids minted
// milliseconds apart in the same test can share their first 8 hex chars
// and collide on the (tenant_id, email) unique constraint (same pitfall
// documented in clause_matches_integration_test.go's seedDocument).
func seedOrgAndUser(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tenant, user uuid.UUID) {
	t.Helper()
	_, err := pool.Exec(ctx, `
		INSERT INTO organizations (id, name, slug) VALUES ($1, 'Test Org', $2)`,
		tenant, "t-"+tenant.String())
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO users (tenant_id, id, email, display_name, role)
		VALUES ($1, $2, $3, 'Test User', 'admin')`,
		tenant, user, "u-"+user.String()+"@test.local")
	require.NoError(t, err)
}

// seedDocument inserts the minimal parent chain (workspace, root folder)
// plus a documents row. The backfill query only reads
// documents.workspace_id and documents.title, so — unlike the clause
// fixture — no content_blobs/document_versions rows are needed.
func seedDocument(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tenant uuid.UUID, title string) (docID, wsID uuid.UUID) {
	t.Helper()
	wsID = uuid.Must(uuid.NewV7())
	folderID := uuid.Must(uuid.NewV7())
	docID = uuid.Must(uuid.NewV7())

	_, err := pool.Exec(ctx, `
		INSERT INTO workspaces (tenant_id, id, name) VALUES ($1, $2, 'ws')`,
		tenant, wsID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO folders (tenant_id, id, workspace_id, name, path, depth)
		VALUES ($1, $2, $3, 'root', 'root', 0)`,
		tenant, folderID, wsID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO documents
			(tenant_id, id, workspace_id, folder_id, title, description, region_pin, sha256_hash, mime_type)
		VALUES ($1, $2, $3, $4, $5, '', 'us-east-1', '', '')`,
		tenant, docID, wsID, folderID, title)
	require.NoError(t, err)
	return docID, wsID
}

// TestTaskSchema_BackfillsLegacyAssigneeAndDocument seeds a legacy-style
// tasks row (assignee_id + linked_document_id populated, the only shape
// that existed before this migration) BEFORE the task service's own
// migration chain runs, then asserts the 000002 backfill produced
// matching task_assignees / task_documents rows — including the
// title_snapshot carried over from the document's title at backfill time.
func TestTaskSchema_BackfillsLegacyAssigneeAndDocument(t *testing.T) {
	ctx, dsn, pool := newSchemaFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	assignee := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, pool, tenant, creator)
	_, err := pool.Exec(ctx, `
		INSERT INTO users (tenant_id, id, email, display_name, role)
		VALUES ($1, $2, $3, 'Assignee User', 'member')`,
		tenant, assignee, "u-"+assignee.String()+"@test.local")
	require.NoError(t, err)

	docID, wsID := seedDocument(ctx, t, pool, tenant, "Legacy Contract")

	taskID := uuid.Must(uuid.NewV7())
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	_, err = pool.Exec(ctx, `
		INSERT INTO tasks
			(tenant_id, id, title, assignee_id, linked_document_id, created_by, created_at)
		VALUES ($1, $2, 'Legacy task', $3, $4, $5, $6)`,
		tenant, taskID, assignee, docID, creator, createdAt)
	require.NoError(t, err, "seed the pre-migration legacy tasks row (assignee_id + linked_document_id)")

	// This is the migration under test: applying it must backfill the
	// legacy row into the new side tables.
	runTaskMigrations(t, dsn)

	var (
		gotUserID, gotAddedBy uuid.UUID
		gotAddedAt            time.Time
	)
	err = pool.QueryRow(ctx, `
		SELECT user_id, added_by, added_at
		  FROM task_assignees
		 WHERE tenant_id = $1 AND task_id = $2`,
		tenant, taskID).Scan(&gotUserID, &gotAddedBy, &gotAddedAt)
	require.NoError(t, err, "backfill must have inserted a task_assignees row for the legacy assignee_id")
	require.Equal(t, assignee, gotUserID, "task_assignees.user_id must be the legacy assignee_id")
	require.Equal(t, creator, gotAddedBy, "task_assignees.added_by must be the legacy created_by")
	require.WithinDuration(t, createdAt, gotAddedAt, time.Second, "task_assignees.added_at must be backfilled from tasks.created_at")

	var (
		gotDocID, gotWsID uuid.UUID
		gotTitleSnapshot  string
	)
	err = pool.QueryRow(ctx, `
		SELECT document_id, workspace_id, title_snapshot
		  FROM task_documents
		 WHERE tenant_id = $1 AND task_id = $2`,
		tenant, taskID).Scan(&gotDocID, &gotWsID, &gotTitleSnapshot)
	require.NoError(t, err, "backfill must have inserted a task_documents row for the legacy linked_document_id")
	require.Equal(t, docID, gotDocID)
	require.Equal(t, wsID, gotWsID, "task_documents.workspace_id must be joined from documents.workspace_id")
	require.Equal(t, "Legacy Contract", gotTitleSnapshot, "task_documents.title_snapshot must be the document's title at backfill time")

	// tasks.deleted_at must now exist (nullable, unset for this row).
	var deletedAt *time.Time
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT deleted_at FROM tasks WHERE tenant_id = $1 AND id = $2`,
		tenant, taskID).Scan(&deletedAt))
	require.Nil(t, deletedAt)
}

// TestTaskSchema_RLSFailsClosedAcrossTenants proves the new side tables
// carry the same tenant-isolation posture as every other tenant table:
// FORCE ROW LEVEL SECURITY plus a USING/WITH CHECK policy on
// app.current_tenant. setupTaskDB (repo_fixture_integration_test.go)
// hands back a dedicated NOBYPASSRLS pool — same pattern
// pkg/database/rls_integration_test.go uses — so this actually exercises
// the policy rather than the testcontainer's BYPASSRLS superuser role.
func TestTaskSchema_RLSFailsClosedAcrossTenants(t *testing.T) {
	ctx, appPool, pool := setupTaskDB(t)

	tenantA := uuid.Must(uuid.NewV7())
	tenantB := uuid.Must(uuid.NewV7())
	userA := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, pool, tenantA, userA)

	taskID := uuid.Must(uuid.NewV7())
	require.NoError(t, database.WithTenantTx(ctx, appPool, tenantA, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO tasks (tenant_id, id, title, created_by) VALUES ($1, $2, 'A-only task', $3)`,
			tenantA, taskID, userA); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO task_assignees (tenant_id, task_id, user_id, added_by)
			VALUES ($1, $2, $3, $3)`,
			tenantA, taskID, userA)
		return err
	}), "seeding tenant A's task_assignees row as the NOBYPASSRLS app role must succeed under its own tenant context")

	// tenantB is never seeded as an organization and has no rows of its
	// own — the point is that setting app.current_tenant to ANY other
	// tenant must not surface tenant A's row.
	var count int
	require.NoError(t, database.WithTenantTx(ctx, appPool, tenantB, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM task_assignees`).Scan(&count)
	}))
	require.Equal(t, 0, count, "task_assignees must fail closed: tenant B's context must not see tenant A's row")
}
