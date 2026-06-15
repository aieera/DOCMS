package repository

import (
	"context"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/services/document/internal/model"
)

// reviewQueueRepo is the Postgres data-access for the WS4 review_queue table.
type reviewQueueRepo struct{}

const reviewCols = `tenant_id, id, ingestion_item_id, workspace_id, target_customer_ref,
	document_class, extracted_external_key, suggested_match_document_id, confidence, reason,
	status, blob_ref, blob_checksum, ocr_result_ref, notes, resolution,
	resulting_document_id, resulting_version_id, resolved_by, resolved_at, created_at`

func scanReviewItem(row pgx.Row) (*model.ReviewQueueItem, error) {
	var (
		it       model.ReviewQueueItem
		reasonS  string
		statusS  string
		resolutN *string
	)
	if err := row.Scan(
		&it.TenantID, &it.ID, &it.IngestionItemID, &it.WorkspaceID, &it.TargetCustomerRef,
		&it.DocumentClass, &it.ExtractedExternalKey, &it.SuggestedMatchDocumentID, &it.Confidence, &reasonS,
		&statusS, &it.BlobRef, &it.BlobChecksum, &it.OCRResultRef, &it.Notes, &resolutN,
		&it.ResultingDocumentID, &it.ResultingVersionID, &it.ResolvedBy, &it.ResolvedAt, &it.CreatedAt,
	); err != nil {
		return nil, mapPgError(err)
	}
	it.Reason = model.ReviewReason(reasonS)
	it.Status = model.ReviewStatus(statusS)
	if resolutN != nil {
		it.Resolution = *resolutN
	}
	return &it, nil
}

// Create inserts a review item. A collision on (tenant, ingestion_item_id) is a
// no-op so the route step can re-run safely (created=false).
func (r *reviewQueueRepo) Create(ctx context.Context, tx pgx.Tx, it *model.ReviewQueueItem) (bool, error) {
	if it.ID == uuid.Nil {
		it.ID = uuid.New()
	}
	if it.CreatedAt.IsZero() {
		it.CreatedAt = time.Now().UTC()
	}
	if it.Status == "" {
		it.Status = model.ReviewPending
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO review_queue (
			tenant_id, id, ingestion_item_id, workspace_id, target_customer_ref,
			document_class, extracted_external_key, suggested_match_document_id, confidence,
			reason, status, blob_ref, blob_checksum, ocr_result_ref, notes, created_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9,
			$10, $11, $12, $13, $14, $15, $16
		)
		ON CONFLICT (tenant_id, ingestion_item_id) DO NOTHING`,
		it.TenantID, it.ID, it.IngestionItemID, it.WorkspaceID, it.TargetCustomerRef,
		it.DocumentClass, it.ExtractedExternalKey, nullableUUID(it.SuggestedMatchDocumentID), it.Confidence,
		string(it.Reason), string(it.Status), it.BlobRef, it.BlobChecksum, nullableUUID(it.OCRResultRef), it.Notes, it.CreatedAt,
	)
	if err != nil {
		return false, mapPgError(err)
	}
	return tag.RowsAffected() == 1, nil
}

func (r *reviewQueueRepo) GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.ReviewQueueItem, error) {
	return scanReviewItem(tx.QueryRow(ctx,
		`SELECT `+reviewCols+` FROM review_queue WHERE tenant_id = $1 AND id = $2`,
		tenantID, id))
}

func (r *reviewQueueRepo) List(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, status string, limit int) ([]model.ReviewQueueItem, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT ` + reviewCols + ` FROM review_queue WHERE tenant_id = $1`
	args := []any{tenantID}
	if status != "" {
		q += ` AND status = $2`
		args = append(args, status)
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, limit)
	return r.queryList(ctx, tx, q, args)
}

// ListKeyset is the paginated read behind GET /api/v1/review-queue. Keyset on
// (created_at, id) DESC: pass the last row's (created_at, id) as the cursor to
// get the next page. cursorTime zero = first page.
func (r *reviewQueueRepo) ListKeyset(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, status string, cursorTime time.Time, cursorID uuid.UUID, limit int) ([]model.ReviewQueueItem, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT ` + reviewCols + ` FROM review_queue WHERE tenant_id = $1`
	args := []any{tenantID}
	if status != "" {
		q += ` AND status = $` + strconv.Itoa(len(args)+1)
		args = append(args, status)
	}
	if !cursorTime.IsZero() {
		// Row-value comparison gives a stable total order for the keyset.
		q += ` AND (created_at, id) < ($` + strconv.Itoa(len(args)+1) + `, $` + strconv.Itoa(len(args)+2) + `)`
		args = append(args, cursorTime, cursorID)
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, limit)
	return r.queryList(ctx, tx, q, args)
}

func (r *reviewQueueRepo) queryList(ctx context.Context, tx pgx.Tx, q string, args []any) ([]model.ReviewQueueItem, error) {
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	var out []model.ReviewQueueItem
	for rows.Next() {
		it, serr := scanReviewItem(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, *it)
	}
	return out, mapPgError(rows.Err())
}

// Resolve marks the review terminal. resolution + resulting ids are set only on
// a commit; a reject leaves them nil.
func (r *reviewQueueRepo) Resolve(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, status model.ReviewStatus, resolution string, resultingDoc, resultingVer *uuid.UUID, resolvedBy uuid.UUID, notes string) error {
	var resolutionArg any
	if resolution != "" {
		resolutionArg = resolution
	}
	_, err := tx.Exec(ctx, `
		UPDATE review_queue
		   SET status = $3, resolution = $4, resulting_document_id = $5,
		       resulting_version_id = $6, resolved_by = $7, resolved_at = now(),
		       notes = CASE WHEN $8 <> '' THEN $8 ELSE notes END
		 WHERE tenant_id = $1 AND id = $2`,
		tenantID, id, string(status), resolutionArg,
		nullableUUID(resultingDoc), nullableUUID(resultingVer), resolvedBy, notes)
	return mapPgError(err)
}
