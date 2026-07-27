package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/services/auth/internal/model"
)

// SessionRepository manages the sessions table. Reads by token_hash DO NOT
// require a tenant GUC — token_hash is globally unique and the row carries
// the tenant. After reading, the service layer should set the tenant GUC
// before touching any other RLS'd table.
type SessionRepository interface {
	Create(ctx context.Context, tx pgx.Tx, s *model.Session) error
	GetByTokenHash(ctx context.Context, pool *pgxpool.Pool, tokenHash string) (*model.Session, error)
	ExtendExpiry(ctx context.Context, pool *pgxpool.Pool, tenantID, id uuid.UUID, newExpiresAt time.Time) error
	TouchActivity(ctx context.Context, pool *pgxpool.Pool, tenantID, id uuid.UUID, at time.Time) error
	RevokeByID(ctx context.Context, tx pgx.Tx, tenantID, userID, id uuid.UUID) error
	RevokeByTokenHash(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, tokenHash string) error
	RevokeAllForUser(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID, exceptID *uuid.UUID) (int64, error)
	ListActiveForUser(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID) ([]model.Session, error)
	CountActiveForUser(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID) (int, error)
	DeleteOldestForUser(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID) error
}

type sessionRepo struct{}

func NewSessionRepo() SessionRepository { return &sessionRepo{} }

func (r *sessionRepo) Create(ctx context.Context, tx pgx.Tx, s *model.Session) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO sessions (id, tenant_id, user_id, token_hash, ip_address, user_agent, expires_at, last_activity_at, created_at)
		VALUES ($1, $2, $3, $4, NULLIF($5,'')::inet, $6, $7, $8, $9)
	`, s.ID, s.TenantID, s.UserID, s.TokenHash, s.IPAddress, s.UserAgent,
		s.ExpiresAt, s.LastActivityAt, s.CreatedAt)
	return mapPgError(err)
}

func (r *sessionRepo) GetByTokenHash(ctx context.Context, pool *pgxpool.Pool, tokenHash string) (*model.Session, error) {
	// PRE-TENANT lookup (token_hash IS how the tenant is learned) on the
	// FORCE-RLS sessions table. A raw read fails closed under dms_app
	// (NOBYPASSRLS) — a Redis cache-miss would then spuriously log the
	// user out. Route through the SECURITY DEFINER exact-match function
	// (auth migration 000001, issue #75).
	row := pool.QueryRow(ctx,
		`SELECT id, tenant_id, user_id, token_hash, ip_address, user_agent,
		        expires_at, last_activity_at, created_at, revoked_at
		 FROM auth_lookup_session_by_token($1)`, tokenHash)
	return scanSession(row)
}

// ExtendExpiry / TouchActivity / RevokeByTokenHash write to the FORCE-RLS
// sessions table, so under the NOBYPASSRLS app role they MUST run inside a
// tenant tx (SET LOCAL app.current_tenant) — a bare-pool UPDATE matches 0 rows
// and silently no-ops (sliding-expiry + activity never persist; and, worst,
// logout never revokes). The read path is RLS-safe via a SECURITY DEFINER
// function; these writes were left on the raw pool.
func (r *sessionRepo) ExtendExpiry(ctx context.Context, pool *pgxpool.Pool, tenantID, id uuid.UUID, newExpiresAt time.Time) error {
	return database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE sessions SET expires_at = $2 WHERE id = $1`, id, newExpiresAt)
		return mapPgError(err)
	})
}

func (r *sessionRepo) TouchActivity(ctx context.Context, pool *pgxpool.Pool, tenantID, id uuid.UUID, at time.Time) error {
	return database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE sessions SET last_activity_at = $2 WHERE id = $1 AND revoked_at IS NULL`, id, at)
		return mapPgError(err)
	})
}

func (r *sessionRepo) RevokeByID(ctx context.Context, tx pgx.Tx, tenantID, userID, id uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE sessions SET revoked_at = now()
		WHERE id = $1 AND tenant_id = $2 AND user_id = $3 AND revoked_at IS NULL
	`, id, tenantID, userID)
	return mapPgError(err)
}

func (r *sessionRepo) RevokeByTokenHash(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, tokenHash string) error {
	return database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at = now() WHERE token_hash = $1 AND revoked_at IS NULL`,
			tokenHash)
		return mapPgError(err)
	})
}

func (r *sessionRepo) RevokeAllForUser(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID, exceptID *uuid.UUID) (int64, error) {
	var (
		ct  pgconn.CommandTag
		err error
	)
	if exceptID != nil {
		ct, err = tx.Exec(ctx, `
			UPDATE sessions SET revoked_at = now()
			WHERE tenant_id = $1 AND user_id = $2 AND revoked_at IS NULL AND id <> $3
		`, tenantID, userID, *exceptID)
	} else {
		ct, err = tx.Exec(ctx, `
			UPDATE sessions SET revoked_at = now()
			WHERE tenant_id = $1 AND user_id = $2 AND revoked_at IS NULL
		`, tenantID, userID)
	}
	if err != nil {
		return 0, mapPgError(err)
	}
	return ct.RowsAffected(), nil
}

func (r *sessionRepo) ListActiveForUser(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID) ([]model.Session, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, user_id, token_hash,
		       COALESCE(host(ip_address), ''), COALESCE(user_agent, ''),
		       expires_at, last_activity_at, created_at, revoked_at
		FROM sessions
		WHERE tenant_id = $1 AND user_id = $2 AND revoked_at IS NULL AND expires_at > now()
		ORDER BY last_activity_at DESC
	`, tenantID, userID)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	var out []model.Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, mapPgError(rows.Err())
}

func (r *sessionRepo) CountActiveForUser(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `
		SELECT count(*) FROM sessions
		WHERE tenant_id = $1 AND user_id = $2 AND revoked_at IS NULL AND expires_at > now()
	`, tenantID, userID).Scan(&n)
	return n, mapPgError(err)
}

func (r *sessionRepo) DeleteOldestForUser(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE sessions SET revoked_at = now()
		WHERE id = (
			SELECT id FROM sessions
			WHERE tenant_id = $1 AND user_id = $2 AND revoked_at IS NULL AND expires_at > now()
			ORDER BY created_at ASC LIMIT 1
		)
	`, tenantID, userID)
	return mapPgError(err)
}

func scanSession(s scanner) (*model.Session, error) {
	var (
		ss      model.Session
		revoked *time.Time
	)
	if err := s.Scan(
		&ss.ID, &ss.TenantID, &ss.UserID, &ss.TokenHash,
		&ss.IPAddress, &ss.UserAgent,
		&ss.ExpiresAt, &ss.LastActivityAt, &ss.CreatedAt, &revoked,
	); err != nil {
		return nil, mapPgError(err)
	}
	ss.RevokedAt = revoked
	return &ss, nil
}
