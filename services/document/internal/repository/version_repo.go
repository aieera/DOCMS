package repository

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/model"
)

type versionRepo struct{}

func (r *versionRepo) Create(ctx context.Context, tx pgx.Tx, v *model.Version) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO document_versions (
			id, tenant_id, document_id, version_number, content_blob_id,
			size_bytes, sha256_hash, mime_type,
			change_summary, created_by, created_by_name, created_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12
		)
	`, v.ID, v.TenantID, v.DocumentID, v.VersionNumber, v.ContentBlobID,
		v.SizeBytes, v.SHA256Hash, v.MimeType,
		v.ChangeSummary, v.CreatedBy, v.CreatedByName, v.CreatedAt)
	return mapPgError(err)
}

func (r *versionRepo) GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.Version, error) {
	row := tx.QueryRow(ctx, `
		SELECT id, tenant_id, document_id, version_number, content_blob_id,
		       size_bytes, mime_type, sha256_hash, created_by, created_by_name,
		       created_at, change_summary, COALESCE(label, '')
		FROM document_versions WHERE tenant_id = $1 AND id = $2
	`, tenantID, id)
	return scanVersion(row)
}

func (r *versionRepo) GetLatestByDocument(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID) (*model.Version, error) {
	row := tx.QueryRow(ctx, `
		SELECT id, tenant_id, document_id, version_number, content_blob_id,
		       size_bytes, mime_type, sha256_hash, created_by, created_by_name,
		       created_at, change_summary, COALESCE(label, '')
		FROM document_versions
		WHERE tenant_id = $1 AND document_id = $2
		ORDER BY version_number DESC LIMIT 1
	`, tenantID, documentID)
	return scanVersion(row)
}

func (r *versionRepo) CountByDocument(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `
		SELECT count(*) FROM document_versions
		WHERE tenant_id = $1 AND document_id = $2
	`, tenantID, documentID).Scan(&n)
	return n, mapPgError(err)
}

// NextVersionNumber reserves the next version number. Runs inside the caller's
// transaction; the UNIQUE(document_id, version_number) constraint serializes
// concurrent writers — the loser receives a 23505 and retries.
func (r *versionRepo) NextVersionNumber(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(version_number), 0) + 1
		FROM document_versions
		WHERE tenant_id = $1 AND document_id = $2
	`, tenantID, documentID).Scan(&n)
	if err != nil {
		return 0, mapPgError(err)
	}
	return n, nil
}

// ListByDocument returns a page of versions in descending version-number
// order. Pagination is keyset on version_number (strictly monotonic).
func (r *versionRepo) ListByDocument(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, f model.VersionFilter) (*model.Page[model.Version], error) {
	pageSize := clampPageSize(f.PageSize)

	args := []any{tenantID, f.DocumentID}
	cursorSQL := ""
	if c, ok := decodeCursor(f.PageToken); ok && c.Sort == "version_number" {
		args = append(args, int(c.Size)) // reuse Size field for the int cursor
		cursorSQL = fmt.Sprintf(" AND version_number < $%d", len(args))
	}
	args = append(args, pageSize+1)

	q := `
		SELECT id, tenant_id, document_id, version_number, content_blob_id,
		       size_bytes, mime_type, sha256_hash, created_by, created_by_name,
		       created_at, change_summary, COALESCE(label, '')
		FROM document_versions
		WHERE tenant_id = $1 AND document_id = $2` + cursorSQL + `
		ORDER BY version_number DESC
		LIMIT $` + fmt.Sprintf("%d", len(args))

	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()

	items := make([]model.Version, 0, pageSize)
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *v)
	}
	if err := rows.Err(); err != nil {
		return nil, mapPgError(err)
	}

	page := &model.Page[model.Version]{Items: items, TotalCount: -1}
	if len(items) > pageSize {
		last := items[pageSize-1]
		page.Items = items[:pageSize]
		page.NextPageToken = encodeCursor(cursor{
			Sort: "version_number",
			Size: int64(last.VersionNumber),
			ID:   last.ID,
		})
	}
	return page, nil
}

func scanVersion(r rowScanner) (*model.Version, error) {
	var v model.Version
	if err := r.Scan(
		&v.ID, &v.TenantID, &v.DocumentID, &v.VersionNumber, &v.ContentBlobID,
		&v.SizeBytes, &v.MimeType, &v.SHA256Hash, &v.CreatedBy, &v.CreatedByName,
		&v.CreatedAt, &v.ChangeSummary, &v.Label,
	); err != nil {
		return nil, mapPgError(err)
	}
	return &v, nil
}

// UpdateLabel sets (or clears, when label == "") the human-friendly
// name on a version. Returns ErrNotFound when the row doesn't exist
// in the caller's tenant — the policy gate above this catches
// cross-tenant access first, but the COUNT-check defends in depth.
func (r *versionRepo) UpdateLabel(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, label string) error {
	var labelArg any
	if label == "" {
		labelArg = nil
	} else {
		labelArg = label
	}
	tag, err := tx.Exec(ctx, `
		UPDATE document_versions
		SET label = $3
		WHERE tenant_id = $1 AND id = $2
	`, tenantID, id, labelArg)
	if err != nil {
		return mapPgError(err)
	}
	if tag.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

// compile-time assertion: versionRepo satisfies the interface.
var _ VersionRepository = (*versionRepo)(nil)

// silence unused-import in tooling where vdmserr isn't touched directly.
var _ = vdmserr.ErrNotFound
