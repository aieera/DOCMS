package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

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
			parts_completed, parts_total, created_by, created_at, expires_at
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10,
			$11, $12, $13, $14, $15
		)
	`,
		u.TenantID, u.ID, docID, u.Filename, u.TotalSize, u.MimeType,
		string(u.UploadType), u.StorageRegion, u.S3UploadID, string(u.Status),
		u.PartsCompleted, u.PartsTotal, u.CreatedBy, u.CreatedAt, u.ExpiresAt,
	)
	return mapPgError(err)
}

const uploadSessionSelect = `
		SELECT tenant_id, id, document_id, filename, total_size,
		       COALESCE(mime_type, ''), upload_type, storage_region,
		       COALESCE(s3_upload_id, ''), status,
		       parts_completed, COALESCE(parts_total, 0), created_by,
		       created_at, completed_at, expires_at, content_blob_id
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

// MarkExpired is called by a background sweeper. Marks any upload_session
// that never reached completed/quarantined before its expires_at as
// failed. Returns the number of rows touched. No tenant scoping — this
// is operator tooling and runs as the table owner.
func (r *uploadRepo) MarkExpired(ctx context.Context, pool *pgxpool.Pool, before time.Time) (int64, error) {
	ct, err := pool.Exec(ctx, `
		UPDATE upload_sessions
		SET status = 'failed'
		WHERE status IN ('initiated','uploading','scanning')
		  AND expires_at < $1
	`, before)
	if err != nil {
		return 0, mapPgError(err)
	}
	return ct.RowsAffected(), nil
}
