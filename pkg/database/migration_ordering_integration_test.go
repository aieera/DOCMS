//go:build integration
// +build integration

// Run with: go test -tags integration -run TestMigration ./pkg/database/
//
// Cross-service migration ordering + isolation (STATE 2026-07-03
// "Migration blocker", ADR 0121). document_entities is read by the
// document service and written by intelligence; historically only
// intelligence 000002 created it, so document's chain (000021 ALTERs the
// table) exploded on any clean database unless intelligence happened to
// migrate first. These tests pin the fix:
//
//   - both application orders succeed on a shared database (the real
//     deployment shape), and
//   - each service's chain applies against a brand-new EMPTY database in
//     isolation, so ordering coupling cannot silently return. Chains
//     with known pre-existing coupling are allow-listed below with
//     tracking issues; fixing one MUST remove its entry (a listed chain
//     that passes fails this test as stale).
package database_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

func applyService(t *testing.T, dsn, service string) error {
	t.Helper()
	return database.RunServiceMigrations(dsn, filepath.Join("..", "..", "services", service, "migrations"), service)
}

// assertNERSchema proves the post-fix contract on a shared DB: the base
// table exists, document 000021's taxonomy extension applied (source
// column + its dependents), regardless of which service migrated first.
func assertNERSchema(ctx context.Context, t *testing.T, dsn string) {
	t.Helper()
	cfg := database.DefaultPoolConfig()
	cfg.SkipRLSPostureCheck = true // container superuser; schema assertions only
	pool, err := database.NewPool(ctx, dsn, cfg)
	require.NoError(t, err)
	defer pool.Close()

	var n int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'document_entities' AND column_name = 'source'
	`).Scan(&n))
	require.Equal(t, 1, n, "document_entities.source must exist after both chains")

	for _, tbl := range []string{"document_entities", "entity_corrections", "ner_config", "document_classifications"} {
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT count(*) FROM information_schema.tables WHERE table_name = $1`, tbl).Scan(&n))
		require.Equalf(t, 1, n, "table %s must exist", tbl)
	}
}

func TestMigrationOrdering_DocumentFirst(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)
	dsn, cleanup, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(cleanup)

	require.NoError(t, applyService(t, dsn, "document"),
		"document chain must apply on a clean DB with no other service migrated (the 000021 blocker)")
	require.NoError(t, applyService(t, dsn, "intelligence"),
		"intelligence chain must tolerate document having created document_entities first")
	assertNERSchema(ctx, t, dsn)
}

// TestMigrationOrdering_IntelligenceCreatesTableFirst models the order
// that historically MASKED the bug: document migrated up to just before
// 000021 in an earlier release, intelligence then migrated (creating
// document_entities via its 000002), and document's later releases
// applied 000021+. Note "intelligence first on a virgin DB" is not a
// real order — intelligence's own 000001 references document-owned
// organizations, so document is always the bootstrap anchor (that
// coupling is tracked in TestMigrationIsolation's allow-list).
func TestMigrationOrdering_IntelligenceCreatesTableFirst(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)
	dsn, cleanup, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(cleanup)

	require.NoError(t, applyServiceTo(t, dsn, "document", 20),
		"document chain up to 000020 (pre-NER-pipeline) must apply on a clean DB")
	require.NoError(t, applyService(t, dsn, "intelligence"),
		"intelligence chain must apply once document's base schema exists")
	require.NoError(t, applyService(t, dsn, "document"),
		"document 000021+ must apply when intelligence already created document_entities")
	assertNERSchema(ctx, t, dsn)
}

// applyServiceTo migrates a service's chain up to (and including) the
// given version, using the same per-service bookkeeping table the
// Makefile and RunServiceMigrations use.
func applyServiceTo(t *testing.T, dsn, service string, version uint) error {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("x-migrations-table", database.ServiceMigrationsTable(service))
	u.RawQuery = q.Encode()

	dir := filepath.Join("..", "..", "services", service, "migrations")
	m, err := migrate.New("file://"+dir, u.String())
	if err != nil {
		return err
	}
	defer func() { _, _ = m.Close() }()
	if err := m.Migrate(version); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// knownCoupledChains are service migration chains that CANNOT apply to a
// brand-new empty database today because they ALTER (or depend on)
// tables owned by another service's chain. Each has a tracking issue;
// the fixing PR removes the entry — a listed chain that starts passing
// fails this test as a stale entry.
var knownCoupledChains = map[string]string{
	// auth's migration adds SECURITY DEFINER lookup FUNCTIONS over
	// document-owned api_keys/sessions/users (issue #75) — coupled to
	// those tables BY DESIGN (the functions can't be created before the
	// tables exist), not a defect to fix, so it has no tracking issue.
	"auth":         "SECURITY DEFINER lookups over document-owned api_keys/sessions/users (by design, #75)",
	"audit":        "https://github.com/aieera/DOCMS/issues/80 (ALTERs document-owned audit_events)",
	"billing":      "https://github.com/aieera/DOCMS/issues/81 (reshapes document-owned subscriptions_billing/usage_meters)",
	"intelligence": "https://github.com/aieera/DOCMS/issues/82 (FK to document-owned organizations)",
	"notification": "https://github.com/aieera/DOCMS/issues/83 (ALTERs document-owned notifications)",
	// task's 000002 ADOPTS the document-owned `tasks` table (created by
	// document migration 000033 per ADR-0068) — it ALTERs tasks and adds
	// FKs onto it from the new side tables. That's the entire point of
	// the 2026-07-28 task-service migration (task-2-brief.md): ownership
	// of `tasks` transfers to the task service from this migration on,
	// but the table itself is still created by document's chain. Coupled
	// BY DESIGN, same class as auth's #75 — no tracking issue.
	"task": "ALTERs/FKs onto document-owned tasks table (ADR-0068 adoption, by design)",
}

func TestMigrationIsolation_EachServiceStandalone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	t.Cleanup(cancel)

	root := repoRootFromWD(t)
	entries, err := filepath.Glob(filepath.Join(root, "services", "*", "migrations"))
	require.NoError(t, err)
	require.NotEmpty(t, entries, "no service migration dirs found — glob broken")

	var services []string
	for _, e := range entries {
		services = append(services, filepath.Base(filepath.Dir(e)))
	}
	sort.Strings(services)

	// One container, one pristine database per service — cheaper than a
	// container each, identical isolation.
	adminDSN, cleanup, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(cleanup)

	adminCfg := database.DefaultPoolConfig()
	adminCfg.SkipRLSPostureCheck = true
	admin, err := database.NewPool(ctx, adminDSN, adminCfg)
	require.NoError(t, err)
	t.Cleanup(admin.Close)

	for _, svc := range services {
		svc := svc
		t.Run(svc, func(t *testing.T) {
			dbName := "iso_" + svc
			_, err := admin.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %q`, dbName))
			require.NoError(t, err)

			u, err := url.Parse(adminDSN)
			require.NoError(t, err)
			u.Path = "/" + dbName
			isoDSN := u.String()

			migErr := applyService(t, isoDSN, svc)
			reason, coupled := knownCoupledChains[svc]

			switch {
			case migErr != nil && !coupled:
				t.Fatalf("%s migrations must apply standalone on an empty database "+
					"(cross-service ordering coupling — the 000021 class); add the "+
					"missing guarded DDL or allow-list with a tracking issue:\n%v", svc, migErr)
			case migErr == nil && coupled:
				t.Fatalf("%s is allow-listed as coupled (%s) but now applies standalone — "+
					"remove its knownCoupledChains entry in this PR", svc, reason)
			case migErr != nil && coupled:
				t.Logf("%s: known-coupled (tracked: %s): %v", svc, reason, migErr)
			}
		})
	}
}

// repoRootFromWD walks up to go.work (same convention as pkg/archtest).
func repoRootFromWD(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not find go.work from test working directory")
	return ""
}
