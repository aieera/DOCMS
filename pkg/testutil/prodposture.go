// Prod-posture test support (STATE_OF_THE_PROJECT 2026-07-03, Wave A).
//
// Production Postgres enforces NOBYPASSRLS on the app role (helm
// postgres-cluster.yaml postInit; migration 000065), so any query that
// runs without `app.current_tenant` fails closed. Dev and most
// integration tests mask this by connecting as the testcontainer
// superuser (BYPASSRLS) with SkipRLSPostureCheck — which is exactly how
// the audit's RLS tenant-context gaps stayed invisible. The helpers here
// let a test opt IN to the prod posture: a dms_app NOBYPASSRLS pool with
// the boot-time posture gate armed, plus an assertion that the dev
// bypass env cannot silently downgrade the lane.
package testutil

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/database"
)

// ProdPostureViolation returns a non-empty reason when the environment
// carries the dev RLS-bypass opt-in. The runtime gate
// (pkg/database/rls_posture.go) honors exactly the string "1", so only
// that value is a violation; unset or "0" is the enforced posture.
func ProdPostureViolation() string {
	if os.Getenv("SEDOC_ALLOW_BYPASS_RLS") == "1" {
		return "SEDOC_ALLOW_BYPASS_RLS=1 is set — this test verifies prod " +
			"(NOBYPASSRLS) behavior and must not run with the dev bypass opt-in; " +
			"unset the variable (see docker-compose.prod-posture.yml)"
	}
	return ""
}

// AssertProdPosture fails the test immediately if the dev bypass env is
// set, so prod-posture tests can't silently run in dev mode. Call it
// first in every //go:build integration && prodposture test.
func AssertProdPosture(t testing.TB) {
	t.Helper()
	if v := ProdPostureViolation(); v != "" {
		t.Fatalf("prod-posture violation: %s", v)
	}
}

// ProdPostureDB is an ephemeral Postgres wired the way prod is: the App
// pool connects as dms_app (NOBYPASSRLS) with the RLS posture gate ARMED
// (NewPool would refuse a BYPASSRLS role). The Super pool is the
// testcontainer superuser for out-of-band seeding (org rows, cross-tenant
// fixtures) — the same split ops tooling has in prod.
type ProdPostureDB struct {
	SuperDSN string
	AppDSN   string
	Super    *pgxpool.Pool
	App      *pgxpool.Pool
}

// NewProdPostureDB starts a Postgres container, runs migrationsDir as the
// superuser (skipped when empty — for tests that create their own
// fixture tables), ensures the dms_app NOBYPASSRLS role with the grants
// the schema-of-record gives it (migration 000065 / scripts/init-db.sql),
// and returns both pools. Fails the test on any error; cleanups are
// registered on t.
func NewProdPostureDB(ctx context.Context, t testing.TB, migrationsDir string) *ProdPostureDB {
	t.Helper()
	AssertProdPosture(t)

	dsn, cleanup, err := NewPostgresContainer(ctx)
	if err != nil {
		t.Fatalf("prod-posture postgres container: %v", err)
	}
	t.Cleanup(cleanup)

	if migrationsDir != "" {
		if err := database.RunMigrations(dsn, migrationsDir); err != nil {
			t.Fatalf("prod-posture migrations (%s): %v", migrationsDir, err)
		}
	}

	// Superuser pool (BYPASSRLS) — posture check skipped deliberately;
	// it exists only for seeding, mirroring ops tooling.
	superCfg := database.DefaultPoolConfig()
	superCfg.SkipRLSPostureCheck = true
	super, err := database.NewPool(ctx, dsn, superCfg)
	if err != nil {
		t.Fatalf("prod-posture superuser pool: %v", err)
	}
	t.Cleanup(super.Close)

	// dms_app: NOBYPASSRLS + the grants migration 000065 establishes.
	// Idempotent so migrationsDirs that already create the role are fine.
	if _, err := super.Exec(ctx, `
		DO $$ BEGIN
		  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'dms_app') THEN
		    CREATE ROLE dms_app LOGIN PASSWORD 'devpassword' NOBYPASSRLS;
		  ELSE
		    ALTER ROLE dms_app NOBYPASSRLS;
		  END IF;
		END $$;
		GRANT USAGE ON SCHEMA public TO dms_app;
		GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dms_app;
		GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO dms_app;
	`); err != nil {
		t.Fatalf("prod-posture dms_app role: %v", err)
	}

	appDSN, err := RewriteDSNUser(dsn, "dms_app", "devpassword")
	if err != nil {
		t.Fatalf("prod-posture app DSN: %v", err)
	}

	// The armed posture gate is the point: DefaultPoolConfig leaves
	// SkipRLSPostureCheck=false, so NewPool itself verifies dms_app has
	// NOBYPASSRLS — the same check prod boot runs.
	app, err := database.NewPool(ctx, appDSN, database.DefaultPoolConfig())
	if err != nil {
		t.Fatalf("prod-posture app pool: %v", err)
	}
	t.Cleanup(app.Close)

	return &ProdPostureDB{SuperDSN: dsn, AppDSN: appDSN, Super: super, App: app}
}

// RewriteDSNUser swaps the userinfo of a libpq URL DSN, keeping
// everything else — used to reconnect as dms_app against a container
// bootstrapped by the superuser.
func RewriteDSNUser(dsn, user, pass string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parse dsn: %w", err)
	}
	u.User = url.UserPassword(user, pass)
	return u.String(), nil
}
