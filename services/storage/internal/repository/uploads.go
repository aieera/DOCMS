package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/storage/internal/model"
)

type uploadRepo struct{}

func (r *uploadRepo) Create(ctx context.Context, tx pgx.Tx, u *model.UploadSession) error {
	var docID any
	if u.DocumentID != nil {
		docID = *u.DocumentID
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO upload_sessions (
			tenant_id, id, document_id, filename, total_size, mime_type,
			upload_type, storage_region, s3_upload_id, status,
			parts_completed, parts_total, created_by, created_at, expires_at,
			storage_bucket, storage_key
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10,
			$11, $12, $13, $14, $15,
			NULLIF($16, ''), NULLIF($17, '')
		)
	`,
		u.TenantID, u.ID, docID, u.Filename, u.TotalSize, u.MimeType,
		string(u.UploadType), u.StorageRegion, u.S3UploadID, string(u.Status),
		u.PartsCompleted, u.PartsTotal, u.CreatedBy, u.CreatedAt, u.ExpiresAt,
		u.StorageBucket, u.StorageKey,
	)
	return mapPgError(err)
}

const uploadSessionSelect = `
		SELECT tenant_id, id, document_id, filename, total_size,
		       COALESCE(mime_type, ''), upload_type, storage_region,
		       COALESCE(s3_upload_id, ''), status,
		       parts_completed, COALESCE(parts_total, 0), created_by,
		       created_at, completed_at, expires_at, content_blob_id,
		       COALESCE(storage_bucket, ''), COALESCE(storage_key, '')
		FROM upload_sessions
		WHERE tenant_id = $1 AND id = $2`

func (r *uploadRepo) GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.UploadSession, error) {
	return scanUploadSession(tx.QueryRow(ctx, uploadSessionSelect, tenantID, id))
}

// GetByIDForUpdate is GetByID with a row lock. CompleteUpload's claim
// transaction uses it so concurrent duplicate completes serialize on
// the session row instead of both reading `initiated` and racing the
// completion pipeline.
func (r *uploadRepo) GetByIDForUpdate(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.UploadSession, error) {
	return scanUploadSession(tx.QueryRow(ctx, uploadSessionSelect+` FOR UPDATE`, tenantID, id))
}

func scanUploadSession(row pgx.Row) (*model.UploadSession, error) {
	var (
		u         model.UploadSession
		docID     *uuid.UUID
		upType    string
		status    string
		completed *time.Time
		blobID    *uuid.UUID
	)
	if err := row.Scan(
		&u.TenantID, &u.ID, &docID, &u.Filename, &u.TotalSize,
		&u.MimeType, &upType, &u.StorageRegion,
		&u.S3UploadID, &status,
		&u.PartsCompleted, &u.PartsTotal, &u.CreatedBy,
		&u.CreatedAt, &completed, &u.ExpiresAt, &blobID,
		&u.StorageBucket, &u.StorageKey,
	); err != nil {
		return nil, mapPgError(err)
	}
	u.UploadType = model.UploadType(upType)
	u.Status = model.UploadStatus(status)
	u.DocumentID = docID
	u.CompletedAt = completed
	u.ContentBlobID = blobID
	return &u, nil
}

func (r *uploadRepo) UpdateStatus(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, status model.UploadStatus) error {
	ct, err := tx.Exec(ctx, `
		UPDATE upload_sessions SET status = $3
		WHERE tenant_id = $1 AND id = $2
	`, tenantID, id, string(status))
	if err != nil {
		return mapPgError(err)
	}
	if ct.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

// Fail moves an in-flight session to failed; completed, quarantined and
// already-failed sessions are untouched (0 rows is not an error).
func (r *uploadRepo) Fail(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE upload_sessions SET status = 'failed'
		WHERE tenant_id = $1 AND id = $2 AND status IN ('initiated','uploading','scanning')
	`, tenantID, id)
	return mapPgError(err)
}

// Abort fails an initiated/uploading session and pulls expires_at back to
// `at`: CompleteUpload re-claims failed sessions that are still within
// their window (retry after a scanner outage), and an aborted one must
// stay closed.
func (r *uploadRepo) Abort(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, at time.Time) (bool, error) {
	ct, err := tx.Exec(ctx, `
		UPDATE upload_sessions
		   SET status = 'failed', expires_at = LEAST(expires_at, $3)
		 WHERE tenant_id = $1 AND id = $2 AND status IN ('initiated','uploading')
	`, tenantID, id, at)
	if err != nil {
		return false, mapPgError(err)
	}
	return ct.RowsAffected() == 1, nil
}

// Complete finalizes the session, recording the blob it produced —
// the reference an idempotent re-complete replays (migration 000095).
func (r *uploadRepo) Complete(ctx context.Context, tx pgx.Tx, tenantID, id, blobID uuid.UUID, at time.Time) error {
	ct, err := tx.Exec(ctx, `
		UPDATE upload_sessions
		SET status = 'completed', completed_at = $3, content_blob_id = $4
		WHERE tenant_id = $1 AND id = $2 AND status IN ('initiated','uploading','scanning')
	`, tenantID, id, at, blobID)
	if err != nil {
		return mapPgError(err)
	}
	if ct.RowsAffected() == 0 {
		return vdmserr.Conflict("upload not in a completable state")
	}
	return nil
}

// MarkExpired marks one tenant's in-flight upload sessions as failed —
// initiated/uploading whose expires_at is before `before`, scanning whose
// expires_at is before `scanningBefore` (a claim keeps a grace period past
// expiry while its pipeline may still be running) — and returns the number
// of rows touched. It runs inside the tenant's
// WithTenantTx: upload_sessions is FORCE-RLS, so a cross-tenant UPDATE on
// the raw pool matches nothing under the NOBYPASSRLS app role (L-6).
func (r *uploadRepo) MarkExpired(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, before, scanningBefore time.Time) (int64, error) {
	ct, err := tx.Exec(ctx, `
		UPDATE upload_sessions
		SET status = 'failed'
		WHERE tenant_id = $1
		  AND (   (status IN ('initiated','uploading') AND expires_at < $2)
		       OR (status = 'scanning'                  AND expires_at < $3))
	`, tenantID, before, scanningBefore)
	if err != nil {
		return 0, mapPgError(err)
	}
	return ct.RowsAffected(), nil
}
