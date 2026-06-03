// Wave 11.1 — tenant-scoped query helper.
//
// Most pre-Wave-11 activity handlers ran against a.Pool directly,
// which meant the tenant GUC (`app.current_tenant`) was never set
// and Postgres RLS on tables like workflow_tasks / documents /
// audit_events was effectively bypassed. Queries still filtered by
// `tenant_id = $N` in their WHERE clause (defense-in-depth one),
// but RLS (defense-in-depth two) didn't fire.
//
// runTenant parses the string tenant id, opens a tenant-scoped
// transaction via pkg/database.WithTenantTx, and runs fn against
// the tx. Every tenant-touching query in this package routes
// through here.
package activities

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/pkg/database"
)

// runTenant parses tenantID, sets `app.current_tenant`, and runs fn
// inside a transaction. The UUID parse fails loudly rather than
// silently dropping the tenant guard.
func (a *Activities) runTenant(ctx context.Context, tenantID string, fn func(tx pgx.Tx) error) error {
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		return fmt.Errorf("tenant_id: %w", err)
	}
	return database.WithTenantTx(ctx, a.Pool, tid, fn)
}
