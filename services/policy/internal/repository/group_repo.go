package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type groupRepo struct{}

func (r *groupRepo) GroupsForUser(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := tx.Query(ctx, `
		SELECT group_id FROM group_members
		WHERE tenant_id = $1 AND user_id = $2
	`, tenantID, userID)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, mapPgError(err)
		}
		out = append(out, id)
	}
	return out, mapPgError(rows.Err())
}

func (r *groupRepo) UsersInGroup(ctx context.Context, tx pgx.Tx, tenantID, groupID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := tx.Query(ctx, `
		SELECT user_id FROM group_members
		WHERE tenant_id = $1 AND group_id = $2
	`, tenantID, groupID)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, mapPgError(err)
		}
		out = append(out, id)
	}
	return out, mapPgError(rows.Err())
}
