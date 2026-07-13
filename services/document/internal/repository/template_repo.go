// Workspace-template CRUD (ADR 0118). All queries run inside
// WithTenantTx — the table is RLS-forced (migration 000092).
package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/services/document/internal/model"
)

type templateRepo struct{}

func (r *templateRepo) Create(ctx context.Context, tx pgx.Tx, t *model.WorkspaceTemplate) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO workspace_templates (tenant_id, id, name, description, definition, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, t.TenantID, t.ID, t.Name, t.Description, t.Definition, t.CreatedBy, t.CreatedAt, t.UpdatedAt)
	return mapPgError(err)
}

func (r *templateRepo) Update(ctx context.Context, tx pgx.Tx, t *model.WorkspaceTemplate) (bool, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE workspace_templates
		   SET name = $3, description = $4, definition = $5, updated_at = $6
		 WHERE tenant_id = $1 AND id = $2
	`, t.TenantID, t.ID, t.Name, t.Description, t.Definition, time.Now().UTC())
	if err != nil {
		return false, mapPgError(err)
	}
	return tag.RowsAffected() > 0, nil
}

func (r *templateRepo) GetProvisionRecord(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, key string) (*ProvisionRecord, error) {
	rec := &ProvisionRecord{}
	err := tx.QueryRow(ctx, `
		SELECT input_digest, result FROM template_provision_requests
		WHERE tenant_id = $1 AND idempotency_key = $2
	`, tenantID, key).Scan(&rec.Digest, &rec.Result)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, mapPgError(err)
	}
	return rec, nil
}

func (r *templateRepo) SaveProvisionRecord(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, key, digest string, result []byte) (bool, error) {
	tag, err := tx.Exec(ctx, `
		INSERT INTO template_provision_requests (tenant_id, idempotency_key, input_digest, result)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (tenant_id, idempotency_key) DO NOTHING
	`, tenantID, key, digest, result)
	if err != nil {
		return false, mapPgError(err)
	}
	return tag.RowsAffected() > 0, nil
}

func (r *templateRepo) GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.WorkspaceTemplate, error) {
	t := &model.WorkspaceTemplate{}
	err := tx.QueryRow(ctx, `
		SELECT tenant_id, id, name, description, definition, created_by, created_at, updated_at
		FROM workspace_templates
		WHERE tenant_id = $1 AND id = $2
	`, tenantID, id).Scan(&t.TenantID, &t.ID, &t.Name, &t.Description, &t.Definition, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, mapPgError(err)
	}
	return t, nil
}

func (r *templateRepo) List(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]model.WorkspaceTemplate, error) {
	rows, err := tx.Query(ctx, `
		SELECT tenant_id, id, name, description, definition, created_by, created_at, updated_at
		FROM workspace_templates
		WHERE tenant_id = $1
		ORDER BY name, id
	`, tenantID)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	var out []model.WorkspaceTemplate
	for rows.Next() {
		var t model.WorkspaceTemplate
		if err := rows.Scan(&t.TenantID, &t.ID, &t.Name, &t.Description, &t.Definition, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, mapPgError(err)
		}
		out = append(out, t)
	}
	return out, mapPgError(rows.Err())
}

func (r *templateRepo) Delete(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (bool, error) {
	tag, err := tx.Exec(ctx, `
		DELETE FROM workspace_templates WHERE tenant_id = $1 AND id = $2
	`, tenantID, id)
	if err != nil {
		return false, mapPgError(err)
	}
	return tag.RowsAffected() > 0, nil
}
