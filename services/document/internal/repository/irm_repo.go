package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/services/document/internal/model"
)

// IRMRepository backs the protected-container export (§5/§8). Tenant-scoped ops
// take a tx opened inside database.WithTenantTx; the license-check callback path
// (LookupByToken / RecordOpen) is cross-tenant-by-token and takes the pool
// directly, resolving tenant_id from the row (the token_hash is the
// high-entropy authenticator, mirroring the zero-trust share model).
type IRMRepository interface {
	CreateContainer(ctx context.Context, tx pgx.Tx, c model.IRMContainer) (uuid.UUID, error)
	CreateLicense(ctx context.Context, tx pgx.Tx, l model.IRMLicense) (uuid.UUID, error)
	InsertEvent(ctx context.Context, tx pgx.Tx, e model.IRMLicenseEvent) error

	// LookupByToken finds a license + its container by token hash WITHOUT a
	// request tenant context (the callback caller has none). Returns (nil, nil,
	// nil) when no license matches.
	LookupByToken(ctx context.Context, pool *pgxpool.Pool, tokenHash []byte) (*model.IRMLicense, *model.IRMContainer, error)
	// IncrementOpen bumps open_count/last_opened_at (tenant-scoped tx opened by
	// the service after resolving the license tenant).
	IncrementOpen(ctx context.Context, tx pgx.Tx, tenantID, licenseID uuid.UUID) error

	GetContainer(ctx context.Context, tx pgx.Tx, tenantID, containerID uuid.UUID) (*model.IRMContainer, error)
	ListContainers(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]model.IRMContainerSummary, error)
	ListLicenses(ctx context.Context, tx pgx.Tx, tenantID, containerID uuid.UUID) ([]model.IRMLicense, error)
	GetLicense(ctx context.Context, tx pgx.Tx, tenantID, licenseID uuid.UUID) (*model.IRMLicense, error)
	RevokeLicense(ctx context.Context, tx pgx.Tx, tenantID, licenseID, revokedBy uuid.UUID) (bool, error)
	// RevokeContainer sets the container-level kill switch (revokes all its
	// licenses at once). Returns false if already revoked / not found.
	RevokeContainer(ctx context.Context, tx pgx.Tx, tenantID, containerID uuid.UUID) (bool, error)
}

type irmRepo struct{}

func NewIRMRepo() IRMRepository { return &irmRepo{} }

func (r *irmRepo) CreateContainer(ctx context.Context, tx pgx.Tx, c model.IRMContainer) (uuid.UUID, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `
		INSERT INTO irm_containers
		    (tenant_id, document_id, version_id, sealed_bucket, sealed_key, payload_nonce,
		     payload_sha256, mime, title, allowed_actions, expires_at, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		RETURNING id`,
		c.TenantID, c.DocumentID, c.VersionID, c.SealedBucket, c.SealedKey, c.PayloadNonce,
		c.PayloadSHA256, c.Mime, c.Title, c.AllowedActions, c.ExpiresAt, c.CreatedBy).Scan(&id)
	if err != nil {
		return uuid.Nil, mapPgError(err)
	}
	return id, nil
}

func (r *irmRepo) CreateLicense(ctx context.Context, tx pgx.Tx, l model.IRMLicense) (uuid.UUID, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `
		INSERT INTO irm_licenses
		    (tenant_id, id, container_id, recipient_type, recipient_ref, token_hash, wrapped_dek, key_ref, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		RETURNING id`,
		l.TenantID, l.ID, l.ContainerID, l.RecipientType, l.RecipientRef, l.TokenHash, l.WrappedDEK, l.KeyRef, l.CreatedBy).Scan(&id)
	if err != nil {
		return uuid.Nil, mapPgError(err)
	}
	return id, nil
}

func (r *irmRepo) InsertEvent(ctx context.Context, tx pgx.Tx, e model.IRMLicenseEvent) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO irm_license_events
		    (tenant_id, container_id, license_id, event_type, recipient_ref, ip_hash, user_agent, detail)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		e.TenantID, e.ContainerID, nullableUUID(e.LicenseID), e.EventType, e.RecipientRef, e.IPHash, e.UserAgent, e.Detail)
	if err != nil {
		return mapPgError(err)
	}
	return nil
}

func (r *irmRepo) LookupByToken(ctx context.Context, pool *pgxpool.Pool, tokenHash []byte) (*model.IRMLicense, *model.IRMContainer, error) {
	var l model.IRMLicense
	var c model.IRMContainer
	err := pool.QueryRow(ctx, `
		SELECT l.tenant_id, l.id, l.container_id, l.recipient_type, l.recipient_ref, l.wrapped_dek,
		       l.key_ref, l.open_count, l.revoked_at,
		       c.id, c.document_id, c.version_id, c.sealed_bucket, c.sealed_key, c.payload_nonce,
		       c.payload_sha256, c.mime, c.title, c.allowed_actions, c.expires_at, c.created_by, c.revoked_at
		FROM irm_licenses l
		JOIN irm_containers c ON c.tenant_id = l.tenant_id AND c.id = l.container_id
		WHERE l.token_hash = $1`, tokenHash).Scan(
		&l.TenantID, &l.ID, &l.ContainerID, &l.RecipientType, &l.RecipientRef, &l.WrappedDEK,
		&l.KeyRef, &l.OpenCount, &l.RevokedAt,
		&c.ID, &c.DocumentID, &c.VersionID, &c.SealedBucket, &c.SealedKey, &c.PayloadNonce,
		&c.PayloadSHA256, &c.Mime, &c.Title, &c.AllowedActions, &c.ExpiresAt, &c.CreatedBy, &c.RevokedAt)
	if err == pgx.ErrNoRows {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, mapPgError(err)
	}
	c.TenantID = l.TenantID
	l.TokenHash = tokenHash
	return &l, &c, nil
}

func (r *irmRepo) IncrementOpen(ctx context.Context, tx pgx.Tx, tenantID, licenseID uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE irm_licenses SET open_count = open_count + 1, last_opened_at = now()
		 WHERE tenant_id = $1 AND id = $2`, tenantID, licenseID)
	if err != nil {
		return mapPgError(err)
	}
	return nil
}

func (r *irmRepo) GetContainer(ctx context.Context, tx pgx.Tx, tenantID, containerID uuid.UUID) (*model.IRMContainer, error) {
	var c model.IRMContainer
	err := tx.QueryRow(ctx, `
		SELECT tenant_id, id, document_id, version_id, sealed_bucket, sealed_key, payload_nonce,
		       payload_sha256, mime, title, allowed_actions, expires_at, created_by, created_at, revoked_at
		FROM irm_containers WHERE tenant_id = $1 AND id = $2`, tenantID, containerID).Scan(
		&c.TenantID, &c.ID, &c.DocumentID, &c.VersionID, &c.SealedBucket, &c.SealedKey, &c.PayloadNonce,
		&c.PayloadSHA256, &c.Mime, &c.Title, &c.AllowedActions, &c.ExpiresAt, &c.CreatedBy, &c.CreatedAt, &c.RevokedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, mapPgError(err)
	}
	return &c, nil
}

func (r *irmRepo) ListContainers(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]model.IRMContainerSummary, error) {
	rows, err := tx.Query(ctx, `
		SELECT c.id, c.document_id, c.title, c.created_at, c.expires_at, c.revoked_at,
		       (SELECT count(*) FROM irm_licenses l WHERE l.tenant_id = c.tenant_id AND l.container_id = c.id)
		FROM irm_containers c WHERE c.tenant_id = $1 ORDER BY c.created_at DESC`, tenantID)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	out := []model.IRMContainerSummary{}
	for rows.Next() {
		var s model.IRMContainerSummary
		if err := rows.Scan(&s.ID, &s.DocumentID, &s.Title, &s.CreatedAt, &s.ExpiresAt, &s.RevokedAt, &s.RecipientCount); err != nil {
			return nil, mapPgError(err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *irmRepo) ListLicenses(ctx context.Context, tx pgx.Tx, tenantID, containerID uuid.UUID) ([]model.IRMLicense, error) {
	rows, err := tx.Query(ctx, `
		SELECT tenant_id, id, container_id, recipient_type, recipient_ref, key_ref, open_count,
		       last_opened_at, revoked_at, revoked_by, created_by, created_at
		FROM irm_licenses WHERE tenant_id = $1 AND container_id = $2 ORDER BY created_at`, tenantID, containerID)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	out := []model.IRMLicense{}
	for rows.Next() {
		var l model.IRMLicense
		if err := rows.Scan(&l.TenantID, &l.ID, &l.ContainerID, &l.RecipientType, &l.RecipientRef, &l.KeyRef,
			&l.OpenCount, &l.LastOpenedAt, &l.RevokedAt, &l.RevokedBy, &l.CreatedBy, &l.CreatedAt); err != nil {
			return nil, mapPgError(err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (r *irmRepo) GetLicense(ctx context.Context, tx pgx.Tx, tenantID, licenseID uuid.UUID) (*model.IRMLicense, error) {
	var l model.IRMLicense
	err := tx.QueryRow(ctx, `
		SELECT tenant_id, id, container_id, recipient_type, recipient_ref, key_ref, open_count,
		       last_opened_at, revoked_at, revoked_by, created_by, created_at
		FROM irm_licenses WHERE tenant_id = $1 AND id = $2`, tenantID, licenseID).Scan(
		&l.TenantID, &l.ID, &l.ContainerID, &l.RecipientType, &l.RecipientRef, &l.KeyRef, &l.OpenCount,
		&l.LastOpenedAt, &l.RevokedAt, &l.RevokedBy, &l.CreatedBy, &l.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, mapPgError(err)
	}
	return &l, nil
}

func (r *irmRepo) RevokeContainer(ctx context.Context, tx pgx.Tx, tenantID, containerID uuid.UUID) (bool, error) {
	ct, err := tx.Exec(ctx, `
		UPDATE irm_containers SET revoked_at = now()
		 WHERE tenant_id = $1 AND id = $2 AND revoked_at IS NULL`, tenantID, containerID)
	if err != nil {
		return false, mapPgError(err)
	}
	return ct.RowsAffected() > 0, nil
}

func (r *irmRepo) RevokeLicense(ctx context.Context, tx pgx.Tx, tenantID, licenseID, revokedBy uuid.UUID) (bool, error) {
	ct, err := tx.Exec(ctx, `
		UPDATE irm_licenses SET revoked_at = now(), revoked_by = $3
		 WHERE tenant_id = $1 AND id = $2 AND revoked_at IS NULL`, tenantID, licenseID, revokedBy)
	if err != nil {
		return false, mapPgError(err)
	}
	return ct.RowsAffected() > 0, nil
}
