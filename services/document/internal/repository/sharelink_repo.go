package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/document/internal/model"
)

type shareLinkRepo struct{}

func (r *shareLinkRepo) Create(ctx context.Context, tx pgx.Tx, l *model.ShareLink) error {
	var pwd any
	if l.PasswordHash != "" {
		pwd = l.PasswordHash
	}
	var expires any
	if l.ExpiresAt != nil {
		expires = *l.ExpiresAt
	}
	if l.Permissions == nil {
		l.Permissions = []string{"view"}
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO share_links (
			id, tenant_id, document_id, token_hash, password_hash,
			expires_at, max_views, view_count, permissions, is_active,
			created_by, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, 0, $8, true, $9, $10)
	`, l.ID, l.TenantID, l.DocumentID, l.Token /* caller passes token_hash here */, pwd,
		expires, l.MaxViews, l.Permissions, l.CreatedBy, l.CreatedAt)
	return mapPgError(err)
}

// GetByTokenHash does NOT filter by tenant — share link tokens are globally
// unique and the access path is deliberately unauthenticated. The service
// layer reads tenant_id from the returned record and sets the session GUC
// accordingly for downstream queries.
func (r *shareLinkRepo) GetByTokenHash(ctx context.Context, tx pgx.Tx, tokenHash string) (*model.ShareLink, error) {
	row := tx.QueryRow(ctx, `
		SELECT id, tenant_id, document_id, token_hash,
		       COALESCE(password_hash, ''), expires_at, max_views, view_count,
		       permissions, is_active, created_by, created_at, accessed_at
		FROM share_links
		WHERE token_hash = $1
	`, tokenHash)
	return scanShareLink(row)
}

func (r *shareLinkRepo) ListByDocument(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID) ([]model.ShareLink, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, document_id, token_hash,
		       COALESCE(password_hash, ''), expires_at, max_views, view_count,
		       permissions, is_active, created_by, created_at, accessed_at
		FROM share_links
		WHERE tenant_id = $1 AND document_id = $2
		ORDER BY created_at DESC
	`, tenantID, documentID)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	var out []model.ShareLink
	for rows.Next() {
		l, err := scanShareLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, mapPgError(rows.Err())
}

// ListByTenant returns all share links for a tenant. When onlyActive
// is true, the query filters to is_active = true AND non-expired.
// Returns document titles via a LEFT JOIN so the admin UI can group
// links by document without a second round trip.
func (r *shareLinkRepo) ListByTenant(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, onlyActive bool) ([]model.ShareLinkAdmin, error) {
	q := `
		SELECT l.id, l.tenant_id, l.document_id, l.token_hash,
		       COALESCE(l.password_hash, ''), l.expires_at, l.max_views, l.view_count,
		       l.permissions, l.is_active, l.created_by, l.created_at, l.accessed_at,
		       COALESCE(d.title, '')
		  FROM share_links l
		  LEFT JOIN documents d
		    ON d.tenant_id = l.tenant_id AND d.id = l.document_id
		 WHERE l.tenant_id = $1`
	if onlyActive {
		q += ` AND l.is_active = true AND (l.expires_at IS NULL OR l.expires_at > now())`
	}
	q += ` ORDER BY l.created_at DESC LIMIT 500`

	rows, err := tx.Query(ctx, q, tenantID)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	var out []model.ShareLinkAdmin
	for rows.Next() {
		var (
			l        model.ShareLinkAdmin
			expires  *time.Time
			accessed *time.Time
		)
		if err := rows.Scan(
			&l.ID, &l.TenantID, &l.DocumentID, &l.Token, &l.PasswordHash,
			&expires, &l.MaxViews, &l.ViewCount, &l.Permissions, &l.IsActive,
			&l.CreatedBy, &l.CreatedAt, &accessed, &l.DocumentTitle,
		); err != nil {
			return nil, mapPgError(err)
		}
		l.ExpiresAt = expires
		l.AccessedAt = accessed
		out = append(out, l)
	}
	return out, mapPgError(rows.Err())
}

// RevokeAllForDocument deactivates every active link attached to
// documentID. Returns the count of rows affected.
func (r *shareLinkRepo) RevokeAllForDocument(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID) (int64, error) {
	ct, err := tx.Exec(ctx, `
		UPDATE share_links SET is_active = false
		 WHERE tenant_id = $1 AND document_id = $2 AND is_active = true`,
		tenantID, documentID)
	if err != nil {
		return 0, mapPgError(err)
	}
	return ct.RowsAffected(), nil
}

func (r *shareLinkRepo) Deactivate(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error {
	ct, err := tx.Exec(ctx, `
		UPDATE share_links SET is_active = false
		WHERE tenant_id = $1 AND id = $2 AND is_active
	`, tenantID, id)
	if err != nil {
		return mapPgError(err)
	}
	if ct.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

// IncrementViewCount bumps view_count and sets accessed_at. No tenant clause
// on purpose — the caller has already resolved the link via its token hash,
// and a rogue tenant cannot forge another tenant's token_hash anyway.
func (r *shareLinkRepo) IncrementViewCount(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE share_links SET view_count = view_count + 1, accessed_at = now()
		WHERE id = $1
	`, id)
	return mapPgError(err)
}

func scanShareLink(r rowScanner) (*model.ShareLink, error) {
	var (
		l        model.ShareLink
		expires  *time.Time
		accessed *time.Time
	)
	if err := r.Scan(
		&l.ID, &l.TenantID, &l.DocumentID, &l.Token, &l.PasswordHash,
		&expires, &l.MaxViews, &l.ViewCount, &l.Permissions, &l.IsActive,
		&l.CreatedBy, &l.CreatedAt, &accessed,
	); err != nil {
		return nil, mapPgError(err)
	}
	l.ExpiresAt = expires
	l.AccessedAt = accessed
	return &l, nil
}
