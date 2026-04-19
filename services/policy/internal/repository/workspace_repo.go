package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/vaultdms/vaultdms/services/policy/internal/model"
)

type workspaceRepo struct{}

func (r *workspaceRepo) MembershipsForUser(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID) ([]model.WorkspaceMember, error) {
	rows, err := tx.Query(ctx, `
		SELECT tenant_id, workspace_id, user_id, role
		FROM workspace_members
		WHERE tenant_id = $1 AND user_id = $2
	`, tenantID, userID)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	var out []model.WorkspaceMember
	for rows.Next() {
		var m model.WorkspaceMember
		if err := rows.Scan(&m.TenantID, &m.WorkspaceID, &m.UserID, &m.Role); err != nil {
			return nil, mapPgError(err)
		}
		out = append(out, m)
	}
	return out, mapPgError(rows.Err())
}
