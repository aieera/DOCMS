//go:build integration && prodposture
// +build integration,prodposture

// O-7 / L-10 / M-28 (2026-10-12 review). A table that ENABLEs row level
// security but does not FORCE it is not filtered for its OWNER, and Helm
// and Ansible ran the services as the table owner — so tenant_keks,
// idempotency_keys, document_entities and the intelligence ledgers had no
// tenant isolation there at all. outbox_dlq and privacy_ledger had no RLS
// whatsoever. Document migration 000108 and intelligence migration 000006
// close those gaps, and AssertRLSPosture (run by every NewPool) now
// refuses a NOBYPASSRLS connection while any tenant table lacks
// ENABLE + FORCE. Because that gate runs against the whole shared
// database, the test applies EVERY service's chain before checking it.
package database_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

// forceTargets are the tables 000108 / intelligence 000006 harden.
var forceTargets = []string{
	"tenant_keks", "idempotency_keys", "document_entities", // FORCE (document 000108)
	"outbox_dlq", "privacy_ledger", // ENABLE + FORCE + policy (document 000108)
	"document_classifications", "intel_processed_events", "ocr_processed_events", // FORCE (intelligence 000006)
}

// pendingForceRLS lists the tenant tables that still lack ENABLE + FORCE
// on this branch because another workstream of the 2026-10-12 review
// remediation owns their fix. Same mechanics as
// ci/prod-posture-allowlist.txt: the test fails if the posture gate flags
// a table NOT listed here (a regression), and fails if a listed table is
// no longer flagged (stale entry — the fix landed; delete the line in the
// same change). There is deliberately no such list in the production gate.
var pendingForceRLS = map[string]string{
	"stripe_customer_map": "MISC — billing migration 000012 (review M-28)",
}

// migrateAllLikeScript applies every service's chain in the order
// scripts/migrate-all.sh uses on a fresh database: document 000001 only
// (default schema_migrations table), intelligence, the rest of document,
// then auth, search, audit, billing, connector, notification, task.
func migrateAllLikeScript(t *testing.T, dsn string) {
	t.Helper()
	dir := func(svc string) string { return filepath.Join("..", "..", "services", svc, "migrations") }
	order := []string{"auth", "search", "audit", "billing", "connector", "notification", "task"}

	// Every service chain must be covered, so a new one cannot be skipped.
	dirs, err := filepath.Glob(filepath.Join("..", "..", "services", "*", "migrations"))
	require.NoError(t, err)
	var all []string
	for _, d := range dirs {
		all = append(all, filepath.Base(filepath.Dir(d)))
	}
	require.ElementsMatch(t, append([]string{"document", "intelligence"}, order...), all,
		"migrateAllLikeScript must mirror scripts/migrate-all.sh for every service chain")

	m, err := migrate.New("file://"+dir("document"), dsn)
	require.NoError(t, err)
	require.NoError(t, m.Steps(1), "document 000001")
	_, _ = m.Close()
	require.NoError(t, database.RunServiceMigrations(dsn, dir("intelligence"), "intelligence"))
	require.NoError(t, database.RunMigrations(dsn, dir("document")))
	for _, svc := range order {
		require.NoErrorf(t, database.RunServiceMigrations(dsn, dir(svc), svc), "%s migrations", svc)
	}
}

func TestProdPosture_ForceRLSOnAllTenantTables(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	t.Cleanup(cancel)

	db := testutil.NewProdPostureDB(ctx, t, "")
	migrateAllLikeScript(t, db.SuperDSN)
	grantAppRole(ctx, t, db.Super)

	t.Run("the posture gate passes on every service chain except the pending tables", func(t *testing.T) {
		// The gate NewPool runs, against the full schema, as dms_app.
		err := database.AssertRLSPosture(ctx, db.App)
		var offenders []string
		if err != nil {
			require.Contains(t, err.Error(), "lack ENABLE + FORCE ROW LEVEL SECURITY")
			offenders = gateOffenders(t, err)
		}
		flagged := map[string]bool{}
		for _, tbl := range offenders {
			flagged[tbl] = true
			if _, pending := pendingForceRLS[tbl]; !pending {
				t.Errorf("%s lacks ENABLE + FORCE ROW LEVEL SECURITY and is not a known pending fix", tbl)
			}
		}
		for tbl, owner := range pendingForceRLS {
			if flagged[tbl] {
				continue
			}
			var enabled, forced bool
			_ = db.Super.QueryRow(ctx, `
				SELECT relrowsecurity, relforcerowsecurity FROM pg_class
				 WHERE oid = to_regclass('public.' || $1)`, tbl).Scan(&enabled, &forced)
			t.Errorf("stale pendingForceRLS entry %s (%s): the gate no longer flags it "+
				"(enabled=%v forced=%v) — delete the entry", tbl, owner, enabled, forced)
		}
	})

	t.Run("the tables this change hardens are ENABLE+FORCE", func(t *testing.T) {
		for _, tbl := range forceTargets {
			var enabled, forced bool
			require.NoErrorf(t, db.Super.QueryRow(ctx,
				`SELECT relrowsecurity, relforcerowsecurity FROM pg_class
				  WHERE oid = to_regclass('public.' || $1)`, tbl).Scan(&enabled, &forced), tbl)
			require.Truef(t, enabled && forced, "%s must be ENABLE+FORCE ROW LEVEL SECURITY", tbl)
		}
	})

	tenantA, tenantB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	registerTenants(ctx, t, db.Super, tenantA, tenantB)

	t.Run("the table owner no longer bypasses tenant_keks", func(t *testing.T) {
		_, err := db.Super.Exec(ctx, `
			INSERT INTO tenant_keks (tenant_id, version, alias) VALUES ($1, 7, 'a'), ($2, 7, 'b')`,
			tenantA, tenantB)
		require.NoError(t, err)
		_, err = db.Super.Exec(ctx, `
			CREATE ROLE probe_owner NOLOGIN NOBYPASSRLS;
			ALTER TABLE tenant_keks OWNER TO probe_owner;`)
		require.NoError(t, err)

		// A fresh session (no GUC residue), acting as the NOBYPASSRLS owner —
		// the Helm/Ansible runtime shape.
		conn, err := pgx.Connect(ctx, db.SuperDSN)
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close(context.Background()) })
		count := func(tenant *uuid.UUID) int {
			tx, err := conn.Begin(ctx)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback(ctx) }()
			_, err = tx.Exec(ctx, `SET LOCAL ROLE probe_owner`)
			require.NoError(t, err)
			if tenant != nil {
				_, err = tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, tenant.String())
				require.NoError(t, err)
			}
			var n int
			require.NoError(t, tx.QueryRow(ctx, `SELECT count(*) FROM tenant_keks WHERE version = 7`).Scan(&n))
			return n
		}
		require.Zero(t, count(nil), "the owner without a tenant context must see no KEK rows")
		require.Equal(t, 1, count(&tenantA), "the owner in tenant A's context sees only A's KEK")
	})

	t.Run("outbox_dlq is tenant-scoped and replay works as dms_app", func(t *testing.T) {
		for _, tenant := range []uuid.UUID{tenantA, tenantB} {
			_, err := db.Super.Exec(ctx, `
				INSERT INTO outbox_dlq (id, tenant_id, event_type, aggregate_type, aggregate_id, payload,
				                        created_at, attempts, last_error)
				VALUES (gen_random_uuid(), $1, 'dms.rlstest.created.v1', 'document', gen_random_uuid(),
				        '{}', now(), 5, 'x')`, tenant)
			require.NoError(t, err)
		}
		require.Equal(t, 1, countAs(ctx, t, db, tenantA, `SELECT count(*) FROM outbox_dlq`))
		require.Zero(t, countRaw(ctx, t, db, `SELECT count(*) FROM outbox_dlq`),
			"no tenant context must mean no dead letters")

		// A dead-letter insert for another tenant is rejected.
		err := database.WithTenantTx(ctx, db.App, tenantA, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `
				INSERT INTO outbox_dlq (id, tenant_id, event_type, aggregate_type, aggregate_id, payload, created_at, attempts)
				VALUES (gen_random_uuid(), $1, 'x', 'document', gen_random_uuid(), '{}', now(), 1)`, tenantB)
			return err
		})
		require.Error(t, err, "outbox_dlq must reject a cross-tenant insert")

		// The replay runs per tenant under RLS and moves each tenant's row.
		got, err := database.ReplayDLQ(ctx, db.App, database.ReplayDLQOptions{Limit: 10})
		require.NoError(t, err)
		require.Len(t, got, 2)
		var left int
		require.NoError(t, db.Super.QueryRow(ctx, `SELECT count(*) FROM outbox_dlq`).Scan(&left))
		require.Zero(t, left)
	})

	t.Run("privacy_ledger is tenant-scoped", func(t *testing.T) {
		write := func(tenant uuid.UUID) error {
			details, _ := json.Marshal(map[string]any{"k": "v"})
			return database.WithTenantTx(ctx, db.App, tenant, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `
					INSERT INTO privacy_ledger (id, tenant_id, subject_email, action, outcome, details)
					VALUES (gen_random_uuid(), $1, 's@x.test', 'export', 'ok', $2)`, tenant, details)
				return err
			})
		}
		// The DSR workflow's write path (WithTenantTx) keeps working.
		require.NoError(t, write(tenantA))
		require.NoError(t, write(tenantB))
		require.Equal(t, 1, countAs(ctx, t, db, tenantA, `SELECT count(*) FROM privacy_ledger`))
		require.Zero(t, countRaw(ctx, t, db, `SELECT count(*) FROM privacy_ledger`))
	})
}

// countAs runs a count query as dms_app inside tenant's context.
func countAs(ctx context.Context, t *testing.T, db *testutil.ProdPostureDB, tenant uuid.UUID, q string) int {
	t.Helper()
	var n int
	require.NoError(t, database.WithTenantTx(ctx, db.App, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, q).Scan(&n)
	}))
	return n
}

// countRaw runs a count query as dms_app on a fresh session with no
// tenant context (a reused pooled session would carry an empty-string GUC
// residue and fail the uuid cast instead — also fail-closed, but noisier).
func countRaw(ctx context.Context, t *testing.T, db *testutil.ProdPostureDB, q string) int {
	t.Helper()
	conn, err := pgx.Connect(ctx, db.AppDSN)
	require.NoError(t, err)
	defer func() { _ = conn.Close(ctx) }()
	var n int
	require.NoError(t, conn.QueryRow(ctx, q).Scan(&n))
	return n
}

func TestProdPosture_RLSPostureGateRequiresForce(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)

	db := testutil.NewProdPostureDB(ctx, t, "")
	exec := func(sql string) {
		t.Helper()
		_, err := db.Super.Exec(ctx, sql)
		require.NoError(t, err)
	}
	const policy = `USING (tenant_id = current_setting('app.current_tenant', true)::uuid)`

	exec(`CREATE TABLE no_tenant_column (id uuid)`)
	exec(`CREATE TABLE forced (tenant_id uuid);
	      ALTER TABLE forced ENABLE ROW LEVEL SECURITY;
	      ALTER TABLE forced FORCE ROW LEVEL SECURITY;
	      CREATE POLICY p ON forced ` + policy)
	require.NoError(t, database.AssertRLSPosture(ctx, db.App),
		"ENABLE+FORCE tenant tables and tables without tenant_id pass the gate")

	exec(`CREATE TABLE enabled_only (tenant_id uuid);
	      ALTER TABLE enabled_only ENABLE ROW LEVEL SECURITY;
	      CREATE POLICY p ON enabled_only ` + policy)
	exec(`CREATE TABLE no_rls (tenant_id uuid)`)
	err := database.AssertRLSPosture(ctx, db.App)
	require.Error(t, err, "a tenant table without FORCE must fail the gate")
	require.ElementsMatch(t, []string{"enabled_only", "no_rls"}, gateOffenders(t, err),
		"the gate names exactly the tenant tables lacking ENABLE+FORCE")

	// The dev / op-bypass opt-in skips the schema check, as it does the
	// role check.
	t.Setenv("SEDOC_ALLOW_BYPASS_RLS", "1")
	require.NoError(t, database.AssertRLSPosture(ctx, db.App))
	t.Setenv("SEDOC_ALLOW_BYPASS_RLS", "")

	exec(`ALTER TABLE enabled_only FORCE ROW LEVEL SECURITY;
	      ALTER TABLE no_rls ENABLE ROW LEVEL SECURITY;
	      ALTER TABLE no_rls FORCE ROW LEVEL SECURITY;`)
	require.NoError(t, database.AssertRLSPosture(ctx, db.App))

	// A partitioned parent is checked like any table. Its partitions are
	// not listed yet (see unforcedTenantTablesSQL): the audit_events
	// partitions get their RLS from the audit chain (review L-10).
	exec(`CREATE TABLE events (tenant_id uuid, at date) PARTITION BY RANGE (at);
	      CREATE TABLE events_2026 PARTITION OF events FOR VALUES FROM ('2026-01-01') TO ('2027-01-01');`)
	err = database.AssertRLSPosture(ctx, db.App)
	require.Error(t, err, "a partitioned tenant table without ENABLE+FORCE must fail the gate")
	require.Equal(t, []string{"events"}, gateOffenders(t, err))
	exec(`ALTER TABLE events ENABLE ROW LEVEL SECURITY;
	      ALTER TABLE events FORCE ROW LEVEL SECURITY;
	      CREATE POLICY p ON events ` + policy)
	require.NoError(t, database.AssertRLSPosture(ctx, db.App))

	// NewPool runs the same gate: a NOBYPASSRLS pool refuses to open.
	exec(`CREATE TABLE late_unforced (tenant_id uuid)`)
	_, err = database.NewPool(ctx, db.AppDSN, database.DefaultPoolConfig())
	require.Error(t, err, "NewPool must refuse a NOBYPASSRLS role while a tenant table lacks FORCE")
	require.Equal(t, []string{"late_unforced"}, gateOffenders(t, err))
}

// gateOffenders extracts the table list from AssertRLSPosture's error
// ("...: a, b, c").
func gateOffenders(t *testing.T, err error) []string {
	t.Helper()
	msg := err.Error()
	i := strings.LastIndex(msg, ": ")
	require.Positivef(t, i, "unexpected gate error shape: %q", msg)
	return strings.Split(msg[i+2:], ", ")
}
