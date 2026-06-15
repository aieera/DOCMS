package repository

import (
	"context"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/services/document/internal/model"
)

// ingestionRepo is the Postgres data-access for the WS3 ingestion_items table.
type ingestionRepo struct{}

const ingestionCols = `tenant_id, id, workspace_id, folder_id, target_customer_ref,
	blob_ref, blob_checksum, status, ocr_result_ref, extracted_external_key,
	match_document_id, confidence, document_class, storage_bucket, storage_key,
	mime_type, region_pin, created_by, failure_reason, created_at, updated_at`

func scanIngestionItem(row pgx.Row) (*model.IngestionItem, error) {
	var (
		it      model.IngestionItem
		statusS string
	)
	if err := row.Scan(
		&it.TenantID, &it.ID, &it.WorkspaceID, &it.FolderID, &it.TargetCustomerRef,
		&it.BlobRef, &it.BlobChecksum, &statusS, &it.OCRResultRef, &it.ExtractedExternalKey,
		&it.MatchDocumentID, &it.Confidence, &it.DocumentClass, &it.StorageBucket, &it.StorageKey,
		&it.MimeType, &it.RegionPin, &it.CreatedBy, &it.FailureReason, &it.CreatedAt, &it.UpdatedAt,
	); err != nil {
		return nil, mapPgError(err)
	}
	it.Status = model.IngestionStatus(statusS)
	return &it, nil
}

// Create inserts a staged item. A collision on the active-row dedup index
// (tenant, checksum, target where status not terminal) is a no-op: created=false
// and the existing active row is returned so the caller can be idempotent.
func (r *ingestionRepo) Create(ctx context.Context, tx pgx.Tx, it *model.IngestionItem) (bool, *model.IngestionItem, error) {
	if it.ID == uuid.Nil {
		it.ID = uuid.New()
	}
	now := time.Now().UTC()
	it.CreatedAt, it.UpdatedAt = now, now
	if it.Status == "" {
		it.Status = model.IngestReceived
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO ingestion_items (
			tenant_id, id, workspace_id, folder_id, target_customer_ref,
			blob_ref, blob_checksum, status, document_class, storage_bucket,
			storage_key, mime_type, region_pin, created_by, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10,
			$11, $12, $13, $14, $15, $16
		)
		ON CONFLICT (tenant_id, blob_checksum, target_customer_ref)
			WHERE status NOT IN ('committed','rejected')
			DO NOTHING`,
		it.TenantID, it.ID, it.WorkspaceID, nullableUUID(it.FolderID), it.TargetCustomerRef,
		it.BlobRef, it.BlobChecksum, string(it.Status), it.DocumentClass, it.StorageBucket,
		it.StorageKey, it.MimeType, it.RegionPin, nullableUUID(it.CreatedBy), it.CreatedAt, it.UpdatedAt,
	)
	if err != nil {
		return false, nil, mapPgError(err)
	}
	if tag.RowsAffected() == 1 {
		return true, it, nil
	}
	// Lost the dedup race (or a re-POST) — return the existing active row.
	existing, gerr := r.getActiveByDedup(ctx, tx, it.TenantID, it.BlobChecksum, it.TargetCustomerRef)
	if gerr != nil {
		return false, nil, gerr
	}
	return false, existing, nil
}

func (r *ingestionRepo) getActiveByDedup(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, checksum, target string) (*model.IngestionItem, error) {
	row := tx.QueryRow(ctx, `SELECT `+ingestionCols+`
		FROM ingestion_items
		WHERE tenant_id = $1 AND blob_checksum = $2 AND target_customer_ref = $3
		  AND status NOT IN ('committed','rejected')
		ORDER BY created_at DESC
		LIMIT 1`, tenantID, checksum, target)
	return scanIngestionItem(row)
}

func (r *ingestionRepo) GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, forUpdate bool) (*model.IngestionItem, error) {
	q := `SELECT ` + ingestionCols + ` FROM ingestion_items WHERE tenant_id = $1 AND id = $2`
	if forUpdate {
		q += " FOR UPDATE"
	}
	return scanIngestionItem(tx.QueryRow(ctx, q, tenantID, id))
}

func (r *ingestionRepo) UpdateRouting(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, status model.IngestionStatus, matchDocumentID *uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE ingestion_items
		   SET status = $3, match_document_id = $4, updated_at = now()
		 WHERE tenant_id = $1 AND id = $2`,
		tenantID, id, string(status), nullableUUID(matchDocumentID))
	return mapPgError(err)
}

func (r *ingestionRepo) SetStatus(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, status model.IngestionStatus, failureReason string) error {
	_, err := tx.Exec(ctx, `
		UPDATE ingestion_items
		   SET status = $3, failure_reason = $4, updated_at = now()
		 WHERE tenant_id = $1 AND id = $2`,
		tenantID, id, string(status), failureReason)
	return mapPgError(err)
}

func (r *ingestionRepo) List(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, status string, limit int) ([]model.IngestionItem, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT ` + ingestionCols + ` FROM ingestion_items WHERE tenant_id = $1`
	args := []any{tenantID}
	if status != "" {
		q += ` AND status = $2`
		args = append(args, status)
	}
	q += ` ORDER BY created_at DESC LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, limit)
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	var out []model.IngestionItem
	for rows.Next() {
		it, serr := scanIngestionItem(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, *it)
	}
	return out, mapPgError(rows.Err())
}
