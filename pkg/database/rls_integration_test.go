//go:build integration
// +build integration

// Run with: go test -tags integration ./pkg/database/...
//
// RLS is the backstop that keeps one tenant from reading another's data.
// If RLS ever regresses, this test is what catches it — every insert /
// select has to run under an explicit tenant GUC; a query without the GUC
// must return zero rows.
package database_test

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/vaultdms/vaultdms/pkg/database"
	"github.com/vaultdms/vaultdms/pkg/testutil"
)

func TestRLS_DocumentsAreTenantScoped(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)

	dsn, pgCleanup, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(pgCleanup)

	require.NoError(t, database.RunMigrations(dsn, "../../services/document/migrations"))

	// The testcontainer's default user is a superuser with BYPASSRLS — RLS
	// policies are a no-op for it. Create a dedicated non-bypass role +
	// seed the organizations rows via the superuser pool (no RLS on orgs),
	// then do every tenant-scoped write/read through a pool that connects
	// as `dms_app` (NOBYPASSRLS) so FORCE ROW LEVEL SECURITY is honored.
	// Superuser pool deliberately has BYPASSRLS — opt out of the
	// posture check so NewPool doesn't refuse to construct it.
	superCfg := database.DefaultPoolConfig()
	superCfg.SkipRLSPostureCheck = true
	superPool, err := database.NewPool(ctx, dsn, superCfg)
	require.NoError(t, err)
	t.Cleanup(func() { superPool.Close() })

	_, err = superPool.Exec(ctx, `
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

	pool, err := database.NewPool(ctx, rewriteDSNUser(dsn, "dms_app", "devpassword"), database.DefaultPoolConfig())
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })

	// Two tenants + one admin user per tenant (foreign-key requirement for
	// workspaces + documents). Tenants are org rows.
	tenantA := uuid.Must(uuid.NewV7())
	tenantB := uuid.Must(uuid.NewV7())
	userA := uuid.Must(uuid.NewV7())
	userB := uuid.Must(uuid.NewV7())
	wsA := uuid.Must(uuid.NewV7())
	wsB := uuid.Must(uuid.NewV7())

	// --- Seed setup: organizations + users + workspaces + one doc each ---
	// Organizations write goes through the superuser pool because the app
	// pool under RLS can't write to a row with a tenant_id that doesn't
	// yet have its GUC set. This mirrors how ops tooling provisions tenants.
	_, err = superPool.Exec(ctx, `INSERT INTO organizations (id, name, slug, plan, primary_region) VALUES
		($1,'Tenant A','tenant-a','standard','us-east-1'),
		($2,'Tenant B','tenant-b','standard','us-east-1')`,
		tenantA, tenantB)
	require.NoError(t, err)

	// Users + workspaces + folder all live under RLS, so each needs its own
	// tenant-scoped transaction. `folders.path` is an LTREE, not TEXT.
	folderA := uuid.Must(uuid.NewV7())
	folderB := uuid.Must(uuid.NewV7())
	createFixtures := func(tenant, user, ws, folder uuid.UUID) {
		require.NoError(t, database.WithTenantTx(ctx, pool, tenant, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `
				INSERT INTO users (tenant_id, id, email, display_name, role, status)
				VALUES ($1, $2, $3, 'u', 'owner', 'active')`,
				tenant, user, "u-"+user.String()[:8]+"@t"); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO workspaces (tenant_id, id, name, region_pin, created_by)
				VALUES ($1, $2, 'ws', 'us-east-1', $3)`, tenant, ws, user); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO folders (tenant_id, id, workspace_id, parent_folder_id, name, path, depth, created_by)
				VALUES ($1, $2, $3, NULL, 'root', 'root'::ltree, 0, $4)`,
				tenant, folder, ws, user); err != nil {
				return err
			}
			return nil
		}))
	}
	createFixtures(tenantA, userA, wsA, folderA)
	createFixtures(tenantB, userB, wsB, folderB)

	// One document per tenant.
	docA := uuid.Must(uuid.NewV7())
	docB := uuid.Must(uuid.NewV7())
	insertDoc := func(tenant, ws, folder, user, doc uuid.UUID, title string) {
		require.NoError(t, database.WithTenantTx(ctx, pool, tenant, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `
				INSERT INTO documents (tenant_id, id, workspace_id, folder_id, title, created_by, lifecycle_state, region_pin)
				VALUES ($1, $2, $3, $4, $5, $6, 'draft', 'us-east-1')`,
				tenant, doc, ws, folder, title, user)
			return err
		}))
	}
	insertDoc(tenantA, wsA, folderA, userA, docA, "a-secret")
	insertDoc(tenantB, wsB, folderB, userB, docB, "b-secret")

	// --- Assertion 1: tenant A sees exactly ONE doc, and it's the A doc ---
	var rows []docRow
	require.NoError(t, database.WithTenantTx(ctx, pool, tenantA, func(tx pgx.Tx) error {
		return readDocsTx(ctx, tx, &rows)
	}))
	if len(rows) != 1 {
		t.Fatalf("tenant A should see exactly 1 row, got %d", len(rows))
	}
	if rows[0].ID != docA {
		t.Errorf("tenant A saw someone else's doc: %v", rows[0])
	}

	// --- Assertion 2: tenant B sees exactly ONE doc, and it's the B doc ---
	rows = rows[:0]
	require.NoError(t, database.WithTenantTx(ctx, pool, tenantB, func(tx pgx.Tx) error {
		return readDocsTx(ctx, tx, &rows)
	}))
	if len(rows) != 1 {
		t.Fatalf("tenant B should see exactly 1 row, got %d", len(rows))
	}
	if rows[0].ID != docB {
		t.Errorf("tenant B saw someone else's doc: %v", rows[0])
	}

	// --- Assertion 3: a raw pool query with NO tenant GUC sees zero rows ---
	// Without SET LOCAL app.current_tenant, the RLS policy evaluates
	// current_setting('app.current_tenant', true) as empty — the USING
	// clause's uuid cast fails-closed to "no match", so 0 rows.
	var count int
	err = pool.QueryRow(ctx, "SELECT COUNT(*) FROM documents").Scan(&count)
	// Either 0 rows (RLS match failed) or a cast error — both are acceptable
	// "fail closed" behaviors. What must NOT happen is a leak.
	if err == nil && count != 0 {
		t.Errorf("raw pool query without tenant GUC leaked %d rows", count)
	}
}

type docRow struct {
	ID    uuid.UUID
	Title string
}

// rewriteDSNUser swaps the userinfo of a libpq DSN. testcontainers hands
// us `postgres://vaultdms:devpassword@host:port/db?...` — we want the same
// DSN with `dms_app:devpassword` so the pool connects without BYPASSRLS.
func rewriteDSNUser(dsn, user, pass string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	u.User = url.UserPassword(user, pass)
	return u.String()
}

func readDocsTx(ctx context.Context, tx pgx.Tx, out *[]docRow) error {
	rows, err := tx.Query(ctx, "SELECT id, title FROM documents ORDER BY title")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r docRow
		if err := rows.Scan(&r.ID, &r.Title); err != nil {
			return err
		}
		*out = append(*out, r)
	}
	return rows.Err()
}
