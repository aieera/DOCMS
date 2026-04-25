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

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/storage/internal/model"
)

// Bundle wires all storage repos.
type Bundle struct {
	Pool         *pgxpool.Pool
	Uploads      UploadRepo
	Scans        ScanRepo
	Lifecycle    LifecycleRepo
	ContentBlobs ContentBlobRepo
	Quarantine   QuarantineRepo
}

// New constructs the bundle.
func New(pool *pgxpool.Pool) *Bundle {
	return &Bundle{
		Pool:         pool,
		Uploads:      &uploadRepo{},
		Scans:        &scanRepo{},
		Lifecycle:    &lifecycleRepo{},
		ContentBlobs: &contentBlobRepo{},
		Quarantine:   &quarantineRepo{},
	}
}

// ---- interfaces -----------------------------------------------------------

type UploadRepo interface {
	Create(ctx context.Context, tx pgx.Tx, u *model.UploadSession) error
	GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.UploadSession, error)
	UpdateStatus(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, status model.UploadStatus) error
	Complete(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, at time.Time) error
	MarkExpired(ctx context.Context, pool *pgxpool.Pool, before time.Time) (int64, error)
}

type ScanRepo interface {
	Record(ctx context.Context, tx pgx.Tx, r *model.ScanRecord) error
	GetByUpload(ctx context.Context, tx pgx.Tx, tenantID, uploadID uuid.UUID) (*model.ScanRecord, error)
	// ListStuckPending returns scan rows still pending older than `cutoff`.
	// Runs against a BYPASSRLS connection so the reconciliation worker can
	// sweep across tenants.
	ListStuckPending(ctx context.Context, pool *pgxpool.Pool, cutoff time.Time, limit int) ([]model.ScanRecord, error)
}

// QuarantineEvent is the audit row written every time the finalize path
// moves an object to the quarantine bucket. Reason distinguishes virus
// hits from static MIME rejections.
type QuarantineEvent struct {
	TenantID      uuid.UUID
	ID            uuid.UUID
	UploadID      uuid.UUID
	Reason        string // virus | blocked_mime | mime_mismatch
	Signature     string
	DeclaredMIME  string
	DetectedMIME  string
	StorageBucket string
	StorageKey    string
	CreatedAt     time.Time
}

type QuarantineRepo interface {
	Record(ctx context.Context, tx pgx.Tx, e *QuarantineEvent) error
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
