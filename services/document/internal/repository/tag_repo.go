package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/document/internal/model"
)

type tagRepo struct{}

func (r *tagRepo) Create(ctx context.Context, tx pgx.Tx, t *model.Tag) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO tags_catalog (id, tenant_id, name, color, created_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, t.ID, t.TenantID, t.Name, t.Color, t.CreatedBy, t.CreatedAt)
	return mapPgError(err)
}

// ListByTenant returns all tags with an aggregated document_count derived
// from the documents.tags array.
func (r *tagRepo) ListByTenant(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]model.Tag, error) {
	rows, err := tx.Query(ctx, `
		SELECT t.id, t.tenant_id, t.name, t.color, t.created_by, t.created_at,
		       COALESCE((SELECT count(*) FROM documents d
		                 WHERE d.tenant_id = t.tenant_id
		                   AND d.deleted_at IS NULL
		                   AND t.name = ANY(d.tags)), 0) AS doc_count
		FROM tags_catalog t
		WHERE t.tenant_id = $1
		ORDER BY lower(t.name) ASC
	`, tenantID)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	var out []model.Tag
	for rows.Next() {
		var t model.Tag
		if err := rows.Scan(&t.ID, &t.TenantID, &t.Name, &t.Color, &t.CreatedBy, &t.CreatedAt, &t.DocumentCount); err != nil {
			return nil, mapPgError(err)
		}
		out = append(out, t)
	}
	return out, mapPgError(rows.Err())
}

func (r *tagRepo) Delete(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error {
	ct, err := tx.Exec(ctx, `
		DELETE FROM tags_catalog WHERE tenant_id = $1 AND id = $2
	`, tenantID, id)
	if err != nil {
		return mapPgError(err)
	}
	if ct.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

// RemoveTagFromAllDocuments scrubs a tag name from every document's tags
// array for the tenant. Used when deleting a catalog entry.
func (r *tagRepo) RemoveTagFromAllDocuments(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, name string) error {
	_, err := tx.Exec(ctx, `
		UPDATE documents SET tags = array_remove(tags, $2), updated_at = now()
		WHERE tenant_id = $1 AND $2 = ANY(tags) AND deleted_at IS NULL
	`, tenantID, name)
	return mapPgError(err)
}

func (r *tagRepo) GetByName(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, name string) (*model.Tag, error) {
	var t model.Tag
	err := tx.QueryRow(ctx, `
		SELECT id, tenant_id, name, color, created_by, created_at
		FROM tags_catalog
		WHERE tenant_id = $1 AND lower(name) = lower($2)
	`, tenantID, name).Scan(&t.ID, &t.TenantID, &t.Name, &t.Color, &t.CreatedBy, &t.CreatedAt)
	if err != nil {
		return nil, mapPgError(err)
	}
	return &t, nil
}
