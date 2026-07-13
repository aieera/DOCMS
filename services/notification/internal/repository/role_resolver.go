// Role→user resolution for role-targeted events (Wave 0.2). The Python
// intelligence tasks emit dms.notification.send.v1 with target_roles
// (e.g. compliance_officer/admin) rather than user ids; the consumer
// resolves them here before fanning out through the normal delivery
// path. Kept in its own file so it composes with the rest of the
// repository without touching the ADR-0086 pref methods.
package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/pkg/database"
)

// UsersForRoles returns the active, non-deleted user ids in the tenant
// whose role is one of `roles`. Runs under the tenant's RLS context —
// users is FORCE RLS and owned by the auth/document schema; the
// notification service reads it here only to fan a role-targeted event
// out to concrete recipients.
func (r *Repository) UsersForRoles(ctx context.Context, tenantID string, roles []string) ([]string, error) {
	if len(roles) == 0 {
		return nil, nil
	}
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		return nil, err
	}
	var out []string
	err = database.WithTenantTx(ctx, r.pool, tid, func(tx pgx.Tx) error {
		rows, qerr := tx.Query(ctx, `
			SELECT id::text
			FROM users
			WHERE tenant_id = $1
			  AND role = ANY($2)
			  AND status = 'active'
			  AND deleted_at IS NULL`, tenantID, roles)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if serr := rows.Scan(&id); serr != nil {
				return serr
			}
			out = append(out, id)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
