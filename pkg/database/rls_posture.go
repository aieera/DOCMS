package database

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AssertRLSPosture is the boot-time gate that catches DB-role and schema
// drift.
//
// Every service that runs queries against tenant-RLS tables MUST call
// this after acquiring its pool (NewPool does unless
// SkipRLSPostureCheck). It queries `SELECT rolbypassrls FROM pg_roles
// WHERE rolname = current_user` and compares against the expected
// posture:
//
//   - Prod default: NOBYPASSRLS — connection role does NOT bypass
//     RLS, so every query must run inside WithTenantTx (which sets
//     app.current_tenant) or fail closed. In this posture every table
//     with a tenant_id column must also be ENABLE + FORCE ROW LEVEL
//     SECURITY: without FORCE the table OWNER is not filtered, so a
//     deployment that connects as the owner has no tenant isolation on
//     that table (review O-7 / L-10). See unforcedTenantTablesSQL for the
//     partition exclusion.
//   - Dev / op-bypass: BYPASSRLS — the role has been deliberately
//     elevated (e.g. for migrations, blob-reaper sweeps that need to
//     see all tenants). The caller must opt in via the env var
//     SEDOC_ALLOW_BYPASS_RLS=1 so the elevation is logged and
//     intentional, not silent. The opt-in also skips the schema check.
//
// Returns an error suitable for the service's startup path; the
// caller is expected to log+exit on it. Failing closed at boot is
// strictly better than silently inserting zero rows or quietly
// returning every other tenant's data later.
//
// FIX-7 (audit C6 + Section 14). Backstops the "Audit ingest may be
// silently failing" finding and any future role-grant drift.
func AssertRLSPosture(ctx context.Context, pool *pgxpool.Pool) error {
	var bypass bool
	if err := pool.QueryRow(ctx,
		`SELECT rolbypassrls FROM pg_roles WHERE rolname = current_user`,
	).Scan(&bypass); err != nil {
		return fmt.Errorf("rls posture probe: %w", err)
	}
	allowBypass := os.Getenv("SEDOC_ALLOW_BYPASS_RLS") == "1"
	if bypass && !allowBypass {
		return fmt.Errorf(
			"DB role has BYPASSRLS=true; this is unsafe in prod. " +
				"Set SEDOC_ALLOW_BYPASS_RLS=1 to opt in (dev / op-bypass) " +
				"or connect as a NOBYPASSRLS role (e.g. dms_app)")
	}
	if allowBypass {
		// Dev / op-bypass opt-in. A NOBYPASSRLS role with the env var
		// set is contradictory (likely a stale deployment manifest) but
		// safe, so it is not refused.
		return nil
	}
	return assertTenantTablesForceRLS(ctx, pool)
}

// unforcedTenantTablesSQL lists every table, including partitioned
// parents, that has a tenant_id column but is not ENABLE + FORCE ROW
// LEVEL SECURITY.
//
// Partitions are not listed yet. A partition read directly by name gets
// only its own RLS settings, so it needs ENABLE + FORCE too, but the
// audit_events partitions (document 000001, audit 000005) are being
// given RLS by the audit chain under review L-10 (a separate change);
// listing them before that lands would refuse every NOBYPASSRLS pool.
// Drop the relispartition filter once every partition is ENABLE + FORCE.
const unforcedTenantTablesSQL = `
SELECT c.oid::regclass::text
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE c.relkind IN ('r', 'p')
   AND NOT c.relispartition
   AND n.nspname NOT IN ('pg_catalog', 'information_schema')
   AND n.nspname NOT LIKE 'pg\_%'
   AND EXISTS (SELECT 1 FROM pg_attribute a
                WHERE a.attrelid = c.oid AND a.attname = 'tenant_id'
                  AND a.attnum > 0 AND NOT a.attisdropped)
   AND NOT (c.relrowsecurity AND c.relforcerowsecurity)
 ORDER BY 1`

func assertTenantTablesForceRLS(ctx context.Context, pool *pgxpool.Pool) error {
	rows, err := pool.Query(ctx, unforcedTenantTablesSQL)
	if err != nil {
		return fmt.Errorf("rls posture: list tenant tables: %w", err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("rls posture: list tenant tables: %w", err)
	}
	if len(names) == 0 {
		return nil
	}
	return fmt.Errorf(
		"%d tenant table(s) lack ENABLE + FORCE ROW LEVEL SECURITY, so their "+
			"owner bypasses tenant isolation; migrate them or set "+
			"SEDOC_ALLOW_BYPASS_RLS=1 (dev / op-bypass only): %s",
		len(names), strings.Join(names, ", "))
}
