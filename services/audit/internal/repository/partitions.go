package repository

import (
	"context"
	"fmt"
)

// Partition maintenance for audit_events (BUG-01).
//
// audit_events is PARTITION BY RANGE (created_at). Document migration
// 000001 shipped four monthly partitions and a comment saying operators
// would roll them forward with a cron job; no such job existed, so once
// the wall clock passed the last bound every INSERT failed with
// "no partition of relation \"audit_events\" found for row" (23514) and
// the audit log went silently empty.
//
// Audit migration 000005 adds a DEFAULT partition (so routing can never
// fail again) plus the idempotent maintenance functions called from here.
// The schedule lives in services/audit/internal/service/partitions.go.
//
// TENANT CONTEXT: these are the only audit repository calls that
// deliberately run on the raw pool instead of database.WithTenantTx, and
// they are not an RLS hole:
//
//   - Partition maintenance is DDL over the whole table. It is inherently
//     cross-tenant and there is no tenant to scope it to — WithTenantTx
//     needs a tenant UUID that does not exist for this operation.
//   - The work happens inside SECURITY DEFINER functions that the app role
//     (dms_app, NOBYPASSRLS, SELECT+INSERT only) could not perform itself.
//     Migration 000005 revokes EXECUTE from PUBLIC and grants dms_app only
//     the two bounded entry points below.
//   - Neither function returns tenant data: EnsurePartitions returns a
//     count of partitions created, DefaultPartitionRows an aggregate row
//     count. No audit row is ever read into the app.

// EnsurePartitions idempotently creates the audit_events monthly
// partitions covering last month through monthsAhead months forward, and
// relocates any rows that had landed in the DEFAULT partition into the
// month partitions it creates. Returns the number of partitions created
// (0 on a healthy, already-maintained database).
//
// Safe to call on every boot and every tick: months whose partition
// already exists are skipped.
func (r *Repository) EnsurePartitions(ctx context.Context, monthsAhead int) (int, error) {
	var created int
	if err := r.pool.QueryRow(ctx, `SELECT ensure_audit_partitions($1)`, monthsAhead).Scan(&created); err != nil {
		return 0, fmt.Errorf("ensure_audit_partitions(%d): %w", monthsAhead, err)
	}
	return created, nil
}

// DefaultPartitionRows reports how many rows are sitting in the DEFAULT
// safety-net partition. Those rows are captured and queryable — DEFAULT
// exists precisely so an unanticipated created_at can never be dropped —
// but a non-zero value means the maintenance window failed to anticipate
// them, so the service exports it as a gauge to watch.
func (r *Repository) DefaultPartitionRows(ctx context.Context) (int64, error) {
	var n int64
	if err := r.pool.QueryRow(ctx, `SELECT audit_default_partition_rows()`).Scan(&n); err != nil {
		return 0, fmt.Errorf("audit_default_partition_rows: %w", err)
	}
	return n, nil
}
