package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/storage/internal/model"
)

type lifecycleRepo struct{}

func (r *lifecycleRepo) Create(ctx context.Context, tx pgx.Tx, j *model.LifecycleJob) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO lifecycle_jobs (id, tenant_id, document_id, from_tier, to_tier, status, reason, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, j.ID, j.TenantID, j.DocumentID, j.FromTier, j.ToTier, j.Status, j.Reason, j.CreatedAt)
	return mapPgError(err)
}

func (r *lifecycleRepo) GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.LifecycleJob, error) {
	row := tx.QueryRow(ctx, `
		SELECT id, tenant_id, document_id, from_tier, to_tier, status,
		       COALESCE(reason, ''), COALESCE(error, ''), created_at, completed_at
		FROM lifecycle_jobs WHERE tenant_id = $1 AND id = $2
	`, tenantID, id)
	var (
		j         model.LifecycleJob
		completed *time.Time
	)
	if err := row.Scan(&j.ID, &j.TenantID, &j.DocumentID, &j.FromTier, &j.ToTier,
		&j.Status, &j.Reason, &j.Error, &j.CreatedAt, &completed); err != nil {
		return nil, mapPgError(err)
	}
	j.CompletedAt = completed
	return &j, nil
}

func (r *lifecycleRepo) UpdateStatus(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, status, errMsg string, completed *time.Time) error {
	var completedArg any
	if completed != nil {
		completedArg = *completed
	}
	ct, err := tx.Exec(ctx, `
		UPDATE lifecycle_jobs
		SET status = $3, error = NULLIF($4, ''), completed_at = $5
		WHERE tenant_id = $1 AND id = $2
	`, tenantID, id, status, errMsg, completedArg)
	if err != nil {
		return mapPgError(err)
	}
	if ct.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}
