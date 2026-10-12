// Package repository is the storage service's Postgres data access layer.
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/storage/internal/model"
)

// Bundle wires all storage repos.
type Bundle struct {
	Pool           *pgxpool.Pool
	Uploads        UploadRepo
	Scans          ScanRepo
	Lifecycle      LifecycleRepo
	ContentBlobs   ContentBlobRepo
	UploadPolicies UploadPolicyRepo
}

// New constructs the bundle.
func New(pool *pgxpool.Pool) *Bundle {
	return &Bundle{
		Pool:           pool,
		Uploads:        &uploadRepo{},
		Scans:          &scanRepo{},
		Lifecycle:      &lifecycleRepo{},
		ContentBlobs:   &contentBlobRepo{},
		UploadPolicies: NewUploadPolicyRepo(),
	}
}

// ---- interfaces -----------------------------------------------------------

type UploadRepo interface {
	Create(ctx context.Context, tx pgx.Tx, u *model.UploadSession) error
	GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.UploadSession, error)
	// GetByIDForUpdate locks the session row (SELECT … FOR UPDATE) so
	// concurrent CompleteUpload claims serialize on it.
	GetByIDForUpdate(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.UploadSession, error)
	UpdateStatus(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, status model.UploadStatus) error
	// Fail moves a session that is still in flight (initiated /
	// uploading / scanning) to failed. Terminal sessions are left alone
	// and no error is returned, so it is safe after an ambiguous commit.
	Fail(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error
	// Abort fails a session that has not started completing (initiated /
	// uploading) and ends its validity window at `at`, so no later
	// CompleteUpload can re-claim it. Returns false when the session was
	// in any other state.
	Abort(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, at time.Time) (bool, error)
	// Complete finalizes the session and records the produced blob —
	// the reference idempotent re-completes replay.
	Complete(ctx context.Context, tx pgx.Tx, tenantID, id, blobID uuid.UUID, at time.Time) error
	// MarkExpired fails one tenant's in-flight sessions: initiated /
	// uploading with expires_at < before, scanning with expires_at <
	// scanningBefore (the caller subtracts the scanning grace). Call it
	// inside that tenant's WithTenantTx.
	MarkExpired(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, before, scanningBefore time.Time) (int64, error)
}

type ScanRepo interface {
	Record(ctx context.Context, tx pgx.Tx, r *model.ScanRecord) error
	GetByUpload(ctx context.Context, tx pgx.Tx, tenantID, uploadID uuid.UUID) (*model.ScanRecord, error)
}

type LifecycleRepo interface {
	Create(ctx context.Context, tx pgx.Tx, j *model.LifecycleJob) error
	GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.LifecycleJob, error)
	UpdateStatus(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, status, errMsg string, completed *time.Time) error
}

// ---- shared error mapping -------------------------------------------------

func mapPgError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return vdmserr.Wrap(vdmserr.ErrNotFound, err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return vdmserr.Wrap(vdmserr.ErrAlreadyExists, err)
		case "23503":
			return vdmserr.Wrap(vdmserr.Conflict("foreign key violation"), err)
		}
	}
	return fmt.Errorf("storage db: %w", err)
}
