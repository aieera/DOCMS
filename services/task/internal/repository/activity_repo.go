// Task-3 (2026-07-28 task-service design) — `task_activity` repo. This is
// the append-only audit trail for a task (assigned/status_changed/etc.);
// there is no Update/Delete by design.
package repository

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/services/task/internal/model"
)

type activityRepo struct{}

// NewActivityRepo returns the default Postgres-backed ActivityRepository impl.
func NewActivityRepo() ActivityRepository { return &activityRepo{} }

func (r *activityRepo) Insert(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, a *model.TaskActivity) error {
	detail := a.Detail
	if len(detail) == 0 {
		detail = json.RawMessage(`{}`)
	}
	err := tx.QueryRow(ctx, `
		INSERT INTO task_activity (tenant_id, task_id, actor_id, action, detail)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, created_at`,
		tenantID, a.TaskID, a.ActorID, a.Action, detail).Scan(&a.ID, &a.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert task_activity: %w", err)
	}
	return nil
}

func (r *activityRepo) List(ctx context.Context, tx pgx.Tx, tenantID, taskID uuid.UUID, limit, offset int) ([]model.TaskActivity, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, task_id, actor_id, action, detail, created_at
		  FROM task_activity
		 WHERE tenant_id = $1 AND task_id = $2
		 ORDER BY id DESC
		 LIMIT $3 OFFSET $4`, tenantID, taskID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list task_activity: %w", err)
	}
	defer rows.Close()

	var out []model.TaskActivity
	for rows.Next() {
		var a model.TaskActivity
		if err := rows.Scan(&a.ID, &a.TaskID, &a.ActorID, &a.Action, &a.Detail, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
