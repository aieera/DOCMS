package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/services/auth/internal/model"
)

// APIKeyRepository manages the api_keys table. key_hash is globally unique
// so lookup by hash does not need tenant scoping; the row carries tenant.
type APIKeyRepository interface {
	Create(ctx context.Context, tx pgx.Tx, k *model.APIKey) error
	GetByHash(ctx context.Context, pool *pgxpool.Pool, hash string) (*model.APIKey, error)
	ListByUser(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID) ([]model.APIKey, error)
	Revoke(ctx context.Context, tx pgx.Tx, tenantID, userID, id uuid.UUID) error
	TouchLastUsed(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, at time.Time) error
	CountActiveByUser(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID) (int, error)
}

type apiKeyRepo struct{}

func NewAPIKeyRepo() APIKeyRepository { return &apiKeyRepo{} }

func (r *apiKeyRepo) Create(ctx context.Context, tx pgx.Tx, k *model.APIKey) error {
	if k.Scopes == nil {
		k.Scopes = []string{}
	}
	var expires any
	if k.ExpiresAt != nil {
		expires = *k.ExpiresAt
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO api_keys (tenant_id, id, user_id, name, key_hash, key_prefix, scopes, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, k.TenantID, k.ID, k.UserID, k.Name, k.KeyHash, k.KeyPrefix, k.Scopes, expires, k.CreatedAt)
	return mapPgError(err)
}

func (r *apiKeyRepo) GetByHash(ctx context.Context, pool *pgxpool.Pool, hash string) (*model.APIKey, error) {
	row := pool.QueryRow(ctx, `
		SELECT tenant_id, id, COALESCE(user_id, '00000000-0000-0000-0000-000000000000'::uuid),
		       name, key_hash, key_prefix, scopes,
		       last_used_at, expires_at, created_at, revoked_at
		FROM api_keys
		WHERE key_hash = $1 AND revoked_at IS NULL
	`, hash)
	return scanAPIKey(row)
}

func (r *apiKeyRepo) ListByUser(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID) ([]model.APIKey, error) {
	rows, err := tx.Query(ctx, `
		SELECT tenant_id, id, user_id, name, key_hash, key_prefix, scopes,
		       last_used_at, expires_at, created_at, revoked_at
		FROM api_keys
		WHERE tenant_id = $1 AND user_id = $2
		ORDER BY created_at DESC
	`, tenantID, userID)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	var out []model.APIKey
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		// Never leak key_hash even to the owner.
		k.KeyHash = ""
		out = append(out, *k)
	}
	return out, mapPgError(rows.Err())
}

func (r *apiKeyRepo) Revoke(ctx context.Context, tx pgx.Tx, tenantID, userID, id uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE api_keys SET revoked_at = now()
		WHERE tenant_id = $1 AND user_id = $2 AND id = $3 AND revoked_at IS NULL
	`, tenantID, userID, id)
	return mapPgError(err)
}

func (r *apiKeyRepo) TouchLastUsed(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, at time.Time) error {
	_, err := pool.Exec(ctx, `UPDATE api_keys SET last_used_at = $2 WHERE id = $1`, id, at)
	return mapPgError(err)
}

func (r *apiKeyRepo) CountActiveByUser(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `
		SELECT count(*) FROM api_keys
		WHERE tenant_id = $1 AND user_id = $2 AND revoked_at IS NULL
		  AND (expires_at IS NULL OR expires_at > now())
	`, tenantID, userID).Scan(&n)
	return n, mapPgError(err)
}

func scanAPIKey(s scanner) (*model.APIKey, error) {
	var (
		k        model.APIKey
		lastUsed *time.Time
		expires  *time.Time
		revoked  *time.Time
	)
	if err := s.Scan(
		&k.TenantID, &k.ID, &k.UserID, &k.Name, &k.KeyHash, &k.KeyPrefix, &k.Scopes,
		&lastUsed, &expires, &k.CreatedAt, &revoked,
	); err != nil {
		return nil, mapPgError(err)
	}
	k.LastUsedAt = lastUsed
	k.ExpiresAt = expires
	k.RevokedAt = revoked
	return &k, nil
}
