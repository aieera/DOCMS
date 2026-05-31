package database

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AssertRLSPosture is the boot-time gate that catches DB-role drift.
//
// Every service that runs queries against tenant-RLS tables MUST call
// this after acquiring its pool. It queries `SELECT rolbypassrls FROM
// pg_roles WHERE rolname = current_user` and compares against the
// expected posture:
//
//   - Prod default: NOBYPASSRLS — connection role does NOT bypass
//     RLS, so every query must run inside WithTenantTx (which sets
//     app.current_tenant) or fail closed.
//   - Dev / op-bypass: BYPASSRLS — the role has been deliberately
//     elevated (e.g. for migrations, blob-reaper sweeps that need to
//     see all tenants). The caller must opt in via the env var
//     VAULTDMS_ALLOW_BYPASS_RLS=1 so the elevation is logged and
//     intentional, not silent.
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
	allowBypass := os.Getenv("VAULTDMS_ALLOW_BYPASS_RLS") == "1"
	if bypass && !allowBypass {
		return fmt.Errorf(
			"DB role has BYPASSRLS=true; this is unsafe in prod. " +
				"Set VAULTDMS_ALLOW_BYPASS_RLS=1 to opt in (dev / op-bypass) " +
				"or connect as a NOBYPASSRLS role (e.g. dms_app)")
	}
	if !bypass && allowBypass {
		// Soft warning surface — caller can choose to log it. The
		// posture is safe (NOBYPASSRLS) so we don't refuse to boot,
		// but the env var is contradictory to current reality and
		// likely indicates a stale deployment manifest.
		return nil
	}
	return nil
}
