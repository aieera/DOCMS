package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/auth/internal/model"
)

// InsertPasswordResetToken stores sha256(token). PK (tenant_id, token_hash);
// a collision (astronomically unlikely with 256-bit tokens) surfaces as a
// conflict the service treats as a retry.
func (r *userRepo) InsertPasswordResetToken(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID, tokenHash string, expiresAt time.Time, ip string) error {
	var ipArg any
	if ip != "" {
		ipArg = ip
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO password_reset_tokens (tenant_id, user_id, token_hash, expires_at, ip_address)
		VALUES ($1, $2, $3, $4, $5)`,
		tenantID, userID, tokenHash, expiresAt, ipArg)
	return err
}

// FindPasswordResetToken returns the token row for the hash within the tenant.
// ErrNotFound when absent — the caller maps that to the generic 4xx so a bad
// token is indistinguishable from an expired/used one.
func (r *userRepo) FindPasswordResetToken(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, tokenHash string) (*model.PasswordResetToken, error) {
	row := tx.QueryRow(ctx, `
		SELECT tenant_id, user_id, expires_at, used_at
		FROM password_reset_tokens
		WHERE tenant_id = $1 AND token_hash = $2`,
		tenantID, tokenHash)
	var t model.PasswordResetToken
	if err := row.Scan(&t.TenantID, &t.UserID, &t.ExpiresAt, &t.UsedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, vdmserr.ErrNotFound
		}
		return nil, err
	}
	return &t, nil
}

// MarkPasswordResetTokenUsed sets used_at=now(), making the token single-use.
func (r *userRepo) MarkPasswordResetTokenUsed(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, tokenHash string) error {
	_, err := tx.Exec(ctx, `
		UPDATE password_reset_tokens SET used_at = now()
		WHERE tenant_id = $1 AND token_hash = $2 AND used_at IS NULL`,
		tenantID, tokenHash)
	return err
}
