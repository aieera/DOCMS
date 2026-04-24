package repository

// Wave 15.4 — signature_profiles repository.
//
// All reads/writes run inside database.WithTenantTx so RLS enforces
// tenant isolation. The DEK and nonce NEVER leak outside the
// repo/service/handler layer — ToPublic() scrubs them for wire.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/signature/internal/model"
)

// ProfileRepo is the CRUD surface. Stateless; share one per process.
type ProfileRepo interface {
	Insert(ctx context.Context, tx pgx.Tx, p *model.SignatureProfile) error
	Get(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.SignatureProfile, error)
	ListByUser(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID) ([]model.SignatureProfile, error)
	Rename(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, name string) error
	SetDefault(ctx context.Context, tx pgx.Tx, tenantID, userID, id uuid.UUID) error
	Revoke(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, at time.Time) error
	HardDelete(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error
}

type profileRepo struct{}

// NewProfileRepo returns a stateless repo.
func NewProfileRepo() ProfileRepo { return &profileRepo{} }

const selectProfileSQL = `
	SELECT tenant_id, id, user_id, name, kind, COALESCE(font_style, ''),
	       image_ref, COALESCE(initials_ref, ''),
	       wrapped_dek, kek_id, nonce, image_size_bytes,
	       is_default, created_at, updated_at, revoked_at
	  FROM signature_profiles`

func (r *profileRepo) Insert(ctx context.Context, tx pgx.Tx, p *model.SignatureProfile) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO signature_profiles (
			tenant_id, id, user_id, name, kind, font_style,
			image_ref, initials_ref, wrapped_dek, kek_id, nonce,
			image_size_bytes, is_default
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, NULLIF($8, ''), $9, $10, $11,
			$12, $13
		)`,
		p.TenantID, p.ID, p.UserID, p.Name, string(p.Kind), nullStr(p.FontStyle),
		p.ImageRef, p.InitialsRef, p.WrappedDEK, p.KEKID, p.Nonce,
		p.ImageSizeBytes, p.IsDefault)
	return mapProfileErr(err)
}

func (r *profileRepo) Get(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.SignatureProfile, error) {
	row := tx.QueryRow(ctx, selectProfileSQL+` WHERE tenant_id = $1 AND id = $2 AND revoked_at IS NULL`, tenantID, id)
	return scanProfile(row)
}

func (r *profileRepo) ListByUser(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID) ([]model.SignatureProfile, error) {
	rows, err := tx.Query(ctx, selectProfileSQL+`
		WHERE tenant_id = $1 AND user_id = $2 AND revoked_at IS NULL
		ORDER BY is_default DESC, created_at DESC`, tenantID, userID)
	if err != nil {
		return nil, mapProfileErr(err)
	}
	defer rows.Close()
	var out []model.SignatureProfile
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (r *profileRepo) Rename(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, name string) error {
	tag, err := tx.Exec(ctx, `
		UPDATE signature_profiles SET name = $3, updated_at = now()
		 WHERE tenant_id = $1 AND id = $2 AND revoked_at IS NULL`,
		tenantID, id, name)
	if err != nil {
		return mapProfileErr(err)
	}
	if tag.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

// SetDefault enforces "at most one default per user" by flipping the
// previous default off within the same tx, then setting the new one.
// The partial unique index on (tenant_id, user_id) WHERE is_default
// would reject a race, but the sequential update is simpler to reason
// about and keeps the final state deterministic.
func (r *profileRepo) SetDefault(ctx context.Context, tx pgx.Tx, tenantID, userID, id uuid.UUID) error {
	if _, err := tx.Exec(ctx, `
		UPDATE signature_profiles SET is_default = false, updated_at = now()
		 WHERE tenant_id = $1 AND user_id = $2 AND is_default = true AND id <> $3`,
		tenantID, userID, id); err != nil {
		return mapProfileErr(err)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE signature_profiles SET is_default = true, updated_at = now()
		 WHERE tenant_id = $1 AND id = $2 AND user_id = $3 AND revoked_at IS NULL`,
		tenantID, id, userID)
	if err != nil {
		return mapProfileErr(err)
	}
	if tag.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

// Revoke soft-deletes. Crypto-shred (dropping the row + S3 object)
// happens in HardDelete; the service layer typically calls both in
// sequence after clearing any envelope references.
func (r *profileRepo) Revoke(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, at time.Time) error {
	tag, err := tx.Exec(ctx, `
		UPDATE signature_profiles SET revoked_at = $3, is_default = false, updated_at = now()
		 WHERE tenant_id = $1 AND id = $2 AND revoked_at IS NULL`,
		tenantID, id, at)
	if err != nil {
		return mapProfileErr(err)
	}
	if tag.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

// HardDelete removes the row. Call AFTER the S3 object has been
// deleted so a failed row-delete doesn't leave orphaned ciphertext.
func (r *profileRepo) HardDelete(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error {
	_, err := tx.Exec(ctx, `DELETE FROM signature_profiles WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	return mapProfileErr(err)
}

// ---- scan + error -----------------------------------------------------------

type profileScanner interface{ Scan(...any) error }

func scanProfile(s profileScanner) (*model.SignatureProfile, error) {
	var (
		p         model.SignatureProfile
		kind      string
		revokedAt *time.Time
	)
	if err := s.Scan(
		&p.TenantID, &p.ID, &p.UserID, &p.Name, &kind, &p.FontStyle,
		&p.ImageRef, &p.InitialsRef, &p.WrappedDEK, &p.KEKID, &p.Nonce,
		&p.ImageSizeBytes, &p.IsDefault, &p.CreatedAt, &p.UpdatedAt, &revokedAt,
	); err != nil {
		return nil, mapProfileErr(err)
	}
	p.Kind = model.SignatureProfileKind(kind)
	p.RevokedAt = revokedAt
	return &p, nil
}

func mapProfileErr(err error) error {
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
		case "23514":
			return vdmserr.Wrap(vdmserr.Validation("kind", "invalid enum"), err)
		}
	}
	return fmt.Errorf("signature_profile db: %w", err)
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
