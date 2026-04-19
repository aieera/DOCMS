package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/document/internal/model"
)

// AnnotationRepository is the data-access contract for the
// annotations table. As with every other repo in this package, every
// method takes a pgx.Tx so the service layer can bundle the write
// with an outbox insert in one transaction (§4.7).
type AnnotationRepository interface {
	Create(ctx context.Context, tx pgx.Tx, a *model.Annotation) error
	GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.Annotation, error)
	ListByDocumentVersion(ctx context.Context, tx pgx.Tx, tenantID, documentID, versionID uuid.UUID) ([]model.Annotation, error)
	Update(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, page int, data map[string]any) error
	SoftDelete(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error
}

type annotationRepo struct{}

// NewAnnotationRepo constructs the repo.
func NewAnnotationRepo() AnnotationRepository { return &annotationRepo{} }

func (r *annotationRepo) Create(ctx context.Context, tx pgx.Tx, a *model.Annotation) error {
	raw, err := json.Marshal(a.Data)
	if err != nil {
		return fmt.Errorf("marshal annotation data: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO annotations (
		    tenant_id, id, document_id, version_id,
		    page_number, annotation_type, annotation_data,
		    created_by, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)
	`, a.TenantID, a.ID, a.DocumentID, a.VersionID,
		a.PageNumber, a.Type, raw, a.CreatedBy, a.CreatedAt)
	return mapPgError(err)
}

func (r *annotationRepo) GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.Annotation, error) {
	row := tx.QueryRow(ctx, `
		SELECT id, tenant_id, document_id, version_id,
		       page_number, annotation_type, annotation_data,
		       created_by, created_at, updated_at, deleted_at
		FROM annotations
		WHERE tenant_id = $1 AND id = $2
	`, tenantID, id)
	return scanAnnotation(row)
}

func (r *annotationRepo) ListByDocumentVersion(ctx context.Context, tx pgx.Tx, tenantID, documentID, versionID uuid.UUID) ([]model.Annotation, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, document_id, version_id,
		       page_number, annotation_type, annotation_data,
		       created_by, created_at, updated_at, deleted_at
		FROM annotations
		WHERE tenant_id = $1 AND document_id = $2 AND version_id = $3
		  AND deleted_at IS NULL
		ORDER BY page_number ASC, created_at ASC
	`, tenantID, documentID, versionID)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()

	var out []model.Annotation
	for rows.Next() {
		a, err := scanAnnotation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, mapPgError(rows.Err())
}

func (r *annotationRepo) Update(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, page int, data map[string]any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal annotation data: %w", err)
	}
	ct, err := tx.Exec(ctx, `
		UPDATE annotations
		SET page_number = $3, annotation_data = $4, updated_at = now()
		WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
	`, tenantID, id, page, raw)
	if err != nil {
		return mapPgError(err)
	}
	if ct.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

func (r *annotationRepo) SoftDelete(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error {
	ct, err := tx.Exec(ctx, `
		UPDATE annotations SET deleted_at = now()
		WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
	`, tenantID, id)
	if err != nil {
		return mapPgError(err)
	}
	if ct.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

func scanAnnotation(r rowScanner) (*model.Annotation, error) {
	var (
		a         model.Annotation
		dataRaw   []byte
		deletedAt *time.Time
	)
	if err := r.Scan(
		&a.ID, &a.TenantID, &a.DocumentID, &a.VersionID,
		&a.PageNumber, &a.Type, &dataRaw,
		&a.CreatedBy, &a.CreatedAt, &a.UpdatedAt, &deletedAt,
	); err != nil {
		return nil, mapPgError(err)
	}
	a.DeletedAt = deletedAt
	if len(dataRaw) > 0 {
		_ = json.Unmarshal(dataRaw, &a.Data)
	}
	if a.Data == nil {
		a.Data = map[string]any{}
	}
	return &a, nil
}
