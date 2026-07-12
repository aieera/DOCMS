// Per-tenant iteration for background jobs (Wave A.1).
//
// FORCE-RLS tables cannot be scanned cross-tenant by the NOBYPASSRLS
// app role, and SET row_security = off is forbidden. The sanctioned
// shape for legitimately cross-tenant work (delivery workers, metering,
// pollers, schedule reconcilers) is: enumerate tenants from the
// organizations registry — deliberately NOT row-level-secured, it IS
// the tenant list — then do each tenant's work under its own
// app.current_tenant. These helpers are that shape, shared so every
// service stops hand-rolling it (search/workflow, billing, and
// connector each grew a copy during Wave A.1).
package database

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ListTenantIDs enumerates every tenant from the organizations
// registry. Runs on the raw pool by design (no RLS on organizations).
func ListTenantIDs(ctx context.Context, pool *pgxpool.Pool) ([]uuid.UUID, error) {
	rows, err := pool.Query(ctx, `SELECT id FROM organizations WHERE deleted_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("enumerate tenants: %w", err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ForEachTenant runs fn once per tenant, each inside that tenant's
// WithTenantTx. A per-tenant error aborts the iteration and is returned
// wrapped with the tenant id — callers that prefer log-and-continue
// semantics should loop ListTenantIDs themselves.
func ForEachTenant(ctx context.Context, pool *pgxpool.Pool, fn func(tenantID uuid.UUID, tx pgx.Tx) error) error {
	tenants, err := ListTenantIDs(ctx, pool)
	if err != nil {
		return err
	}
	for _, tid := range tenants {
		tid := tid
		if err := WithTenantTx(ctx, pool, tid, func(tx pgx.Tx) error {
			return fn(tid, tx)
		}); err != nil {
			return fmt.Errorf("tenant %s: %w", tid, err)
		}
	}
	return nil
}
