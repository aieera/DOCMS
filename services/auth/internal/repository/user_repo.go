// Package repository is the auth service's Postgres data layer. Every
// method takes a pgx.Tx or *pgxpool.Pool so callers control the transaction.
// All tenant-scoped reads/writes must go through database.WithTenant or
// WithTenantTx so RLS is enforced.
package repository

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/auth/internal/model"
)

// UserRepository exposes user CRUD. Implementations must NEVER log password
// hashes or MFA secrets.
type UserRepository interface {
	FindOrganizationBySlug(ctx context.Context, pool *pgxpool.Pool, slug string) (*model.Organization, error)
	GetOrganizationByID(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (*model.Organization, error)
	Create(ctx context.Context, tx pgx.Tx, u *model.User) error
	GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.User, error)
	GetByEmail(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, email string) (*model.User, error)
	GetByInviteTokenHash(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, hash string) (*model.User, error)
	UpdateLastLogin(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, at time.Time) error
	SetPasswordHash(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, hash string) error
	ClearInviteToken(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error
	SetMFASecret(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, enc string, recoveryHashes []string) error
	SetMFAEnabled(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, enabled bool) error
	ClearMFA(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error
	ConsumeRecoveryHash(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, hashToRemove string) error
	SetStatus(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, s model.Status) error
}

type userRepo struct{}

// NewUserRepo constructs a stateless user repo.
func NewUserRepo() UserRepository { return &userRepo{} }

// FindOrganizationBySlug reads the tenants table directly. No RLS on
// organizations, so it uses the pool with no tenant GUC.
func (r *userRepo) FindOrganizationBySlug(ctx context.Context, pool *pgxpool.Pool, slug string) (*model.Organization, error) {
	row := pool.QueryRow(ctx, `
		SELECT id, slug, name, primary_region, deleted_at
		FROM organizations
		WHERE slug = $1
	`, strings.ToLower(strings.TrimSpace(slug)))

	var (
		o       model.Organization
		deleted *time.Time
	)
	if err := row.Scan(&o.ID, &o.Slug, &o.Name, &o.PrimaryRegion, &deleted); err != nil {
		return nil, mapPgError(err)
	}
	o.DeletedAt = deleted
	if deleted != nil {
		return nil, vdmserr.ErrNotFound
	}
	return &o, nil
}

// GetOrganizationByID is the cross-tenant counterpart to FindOrganizationBySlug;
// invite acceptance needs the slug→id lookup too.
func (r *userRepo) GetOrganizationByID(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (*model.Organization, error) {
	row := pool.QueryRow(ctx, `
		SELECT id, slug, name, primary_region, deleted_at
		FROM organizations
		WHERE id = $1
	`, id)
	var (
		o       model.Organization
		deleted *time.Time
	)
	if err := row.Scan(&o.ID, &o.Slug, &o.Name, &o.PrimaryRegion, &deleted); err != nil {
		return nil, mapPgError(err)
	}
	o.DeletedAt = deleted
	if deleted != nil {
		return nil, vdmserr.ErrNotFound
	}
	return &o, nil
}

func (r *userRepo) Create(ctx context.Context, tx pgx.Tx, u *model.User) error {
	settings, _ := json.Marshal(u.Settings)
	if len(settings) == 0 {
		settings = []byte("{}")
	}
	if u.Locale == "" {
		u.Locale = "en"
	}
	if u.Timezone == "" {
		u.Timezone = "UTC"
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO users (
			tenant_id, id, email, display_name, password_hash, avatar_url,
			role, status, mfa_enabled, mfa_secret_encrypted, mfa_recovery_hashes,
			locale, timezone, settings,
			created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10, $11,
			$12, $13, $14,
			$15, $15
		)`,
		u.TenantID, u.ID, strings.ToLower(u.Email), u.DisplayName, nullableStr(u.PasswordHash), nullableStr(u.AvatarURL),
		string(u.Role), string(u.Status), u.MFAEnabled, nullableStr(u.MFASecretEnc), u.MFARecoveryHashes,
		u.Locale, u.Timezone, settings,
		time.Now().UTC(),
	)
	return mapPgError(err)
}

func (r *userRepo) GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.User, error) {
	row := tx.QueryRow(ctx, selectUserSQL+` WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`, tenantID, id)
	return scanUser(row)
}

func (r *userRepo) GetByEmail(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, email string) (*model.User, error) {
	row := tx.QueryRow(ctx, selectUserSQL+` WHERE tenant_id = $1 AND lower(email) = lower($2) AND deleted_at IS NULL`,
		tenantID, email)
	return scanUser(row)
}

// GetByInviteTokenHash returns the (single) user in this tenant whose
// settings.invite_token_hash matches. Returns ErrNotFound if no match.
// The hash is sha256-hex of the plaintext token.
func (r *userRepo) GetByInviteTokenHash(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, hash string) (*model.User, error) {
	row := tx.QueryRow(ctx,
		selectUserSQL+` WHERE tenant_id = $1 AND settings->>'invite_token_hash' = $2 AND deleted_at IS NULL`,
		tenantID, hash)
	return scanUser(row)
}

// ClearInviteToken removes the invite-related keys from settings once a
// user has consumed their token. Idempotent.
func (r *userRepo) ClearInviteToken(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE users
		   SET settings = (settings - 'invite_token_hash' - 'invite_expires_at' - 'invited_by'),
		       updated_at = now()
		 WHERE tenant_id = $1 AND id = $2`,
		tenantID, id)
	return mapPgError(err)
}

func (r *userRepo) UpdateLastLogin(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, at time.Time) error {
	_, err := tx.Exec(ctx, `UPDATE users SET last_login_at = $3 WHERE tenant_id = $1 AND id = $2`, tenantID, id, at)
	return mapPgError(err)
}

func (r *userRepo) SetPasswordHash(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, hash string) error {
	_, err := tx.Exec(ctx, `UPDATE users SET password_hash = $3, updated_at = now() WHERE tenant_id = $1 AND id = $2`,
		tenantID, id, hash)
	return mapPgError(err)
}

func (r *userRepo) SetMFASecret(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, enc string, recoveryHashes []string) error {
	_, err := tx.Exec(ctx, `
		UPDATE users SET mfa_secret_encrypted = $3, mfa_recovery_hashes = $4, updated_at = now()
		WHERE tenant_id = $1 AND id = $2`,
		tenantID, id, enc, recoveryHashes)
	return mapPgError(err)
}

func (r *userRepo) SetMFAEnabled(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, enabled bool) error {
	_, err := tx.Exec(ctx, `UPDATE users SET mfa_enabled = $3, updated_at = now() WHERE tenant_id = $1 AND id = $2`,
		tenantID, id, enabled)
	return mapPgError(err)
}

func (r *userRepo) ClearMFA(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE users SET mfa_enabled = false, mfa_secret_encrypted = NULL,
		                 mfa_recovery_hashes = NULL, updated_at = now()
		WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	return mapPgError(err)
}

func (r *userRepo) ConsumeRecoveryHash(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, hashToRemove string) error {
	_, err := tx.Exec(ctx, `
		UPDATE users SET mfa_recovery_hashes = array_remove(mfa_recovery_hashes, $3), updated_at = now()
		WHERE tenant_id = $1 AND id = $2`, tenantID, id, hashToRemove)
	return mapPgError(err)
}

func (r *userRepo) SetStatus(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, s model.Status) error {
	_, err := tx.Exec(ctx, `UPDATE users SET status = $3, updated_at = now() WHERE tenant_id = $1 AND id = $2`,
		tenantID, id, string(s))
	return mapPgError(err)
}

const selectUserSQL = `
	SELECT tenant_id, id, email, display_name,
	       COALESCE(password_hash, ''), COALESCE(avatar_url, ''),
	       role, status, mfa_enabled,
	       COALESCE(mfa_secret_encrypted, ''),
	       COALESCE(mfa_recovery_hashes, '{}'::text[]),
	       last_login_at, locale, timezone, settings,
	       created_at, updated_at, deleted_at
	FROM users`

type scanner interface{ Scan(...any) error }

func scanUser(s scanner) (*model.User, error) {
	var (
		u         model.User
		role, st  string
		settings  []byte
		last      *time.Time
		deleted   *time.Time
	)
	if err := s.Scan(
		&u.TenantID, &u.ID, &u.Email, &u.DisplayName,
		&u.PasswordHash, &u.AvatarURL,
		&role, &st, &u.MFAEnabled,
		&u.MFASecretEnc, &u.MFARecoveryHashes,
		&last, &u.Locale, &u.Timezone, &settings,
		&u.CreatedAt, &u.UpdatedAt, &deleted,
	); err != nil {
		return nil, mapPgError(err)
	}
	u.Role = model.Role(role)
	u.Status = model.Status(st)
	u.LastLoginAt = last
	u.DeletedAt = deleted
	if len(settings) > 0 {
		_ = json.Unmarshal(settings, &u.Settings)
	}
	if u.Settings == nil {
		u.Settings = map[string]any{}
	}
	return &u, nil
}

func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
