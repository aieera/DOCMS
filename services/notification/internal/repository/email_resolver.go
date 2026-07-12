// user-id → email resolution for the email channel.
//
// The notification service reads the shared schema-of-record `users`
// table directly (same Postgres, tenant-scoped under WithTenantTx like
// every other read here). That IS the "local projection" a
// dms.user.*.v1 consumer would maintain — the table the auth service
// writes is already local to this database, so a projection would only
// add lag and a consumer to babysit, and a gRPC call would put a
// network hop on the delivery hot path.
package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/pkg/database"
)

// UserEmail is one recipient's resolution: the address plus whether the
// account can still receive mail (active + not soft-deleted).
type UserEmail struct {
	Email  string
	Active bool
}

// EmailsForUsers resolves user ids to email addresses in one query.
// Unknown ids are simply absent from the map; non-UUID entries (the DSR
// path puts raw addresses in user_ids — callers branch those off first)
// are ignored rather than failing the batch.
func (r *Repository) EmailsForUsers(ctx context.Context, tenantID string, userIDs []string) (map[string]UserEmail, error) {
	ids := make([]uuid.UUID, 0, len(userIDs))
	for _, s := range userIDs {
		if id, err := uuid.Parse(s); err == nil {
			ids = append(ids, id)
		}
	}
	out := make(map[string]UserEmail, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		return nil, err
	}
	err = database.WithTenantTx(ctx, r.pool, tid, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, email, (status = 'active' AND deleted_at IS NULL)
			FROM users
			WHERE tenant_id = $1 AND id = ANY($2)
		`, tenantID, ids)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var ue UserEmail
			if err := rows.Scan(&id, &ue.Email, &ue.Active); err != nil {
				return err
			}
			out[id] = ue
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
