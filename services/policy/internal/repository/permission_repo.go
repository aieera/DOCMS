package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/policy/internal/model"
)

type permissionRepo struct{}

// Column list used by every SELECT. The 000001 schema stores capability as a
// single TEXT and has no effect column (every grant is implicitly "allow" —
// deny is modeled by the absence of the grant, not a row with effect=deny).
const selectPermissionCols = `
	tenant_id, id, resource_type, resource_id,
	principal_type, principal_id,
	capability,
	COALESCE(granted_by, '00000000-0000-0000-0000-000000000000'::uuid),
	granted_at, valid_from, valid_to, expires_at`

func (r *permissionRepo) ListByResource(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, kind model.ResourceType, id uuid.UUID, at time.Time) ([]model.Permission, error) {
	rows, err := tx.Query(ctx, `
		SELECT `+selectPermissionCols+`
		FROM permissions
		WHERE tenant_id = $1
		  AND resource_type = $2 AND resource_id = $3
		  AND valid_from <= $4
		  AND (valid_to IS NULL OR valid_to > $4)
		  AND (expires_at IS NULL OR expires_at > $4)
	`, tenantID, string(kind), id, at)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	return scanPermissions(rows)
}

func (r *permissionRepo) ListByResourceAsOf(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, kind model.ResourceType, id uuid.UUID, asOf time.Time) ([]model.Permission, error) {
	rows, err := tx.Query(ctx, `
		SELECT `+selectPermissionCols+`
		FROM permissions
		WHERE tenant_id = $1
		  AND resource_type = $2 AND resource_id = $3
		  AND valid_from <= $4
		  AND (valid_to IS NULL OR valid_to >= $4)
	`, tenantID, string(kind), id, asOf)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	return scanPermissions(rows)
}

func (r *permissionRepo) ListByPrincipal(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, kind model.PrincipalType, id uuid.UUID, at time.Time) ([]model.Permission, error) {
	rows, err := tx.Query(ctx, `
		SELECT `+selectPermissionCols+`
		FROM permissions
		WHERE tenant_id = $1
		  AND principal_type = $2 AND principal_id = $3
		  AND valid_from <= $4
		  AND (valid_to IS NULL OR valid_to > $4)
		  AND (expires_at IS NULL OR expires_at > $4)
	`, tenantID, string(kind), id, at)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	return scanPermissions(rows)
}

// Insert writes a permission. Schema has capability as a single TEXT value
// (CHECK constraint limits the domain); a grant with model.Effect=deny is
// not representable in the 000001 schema and is rejected up front.
func (r *permissionRepo) Insert(ctx context.Context, tx pgx.Tx, p *model.Permission) error {
	if p.Effect != model.EffectAllow {
		return vdmserr.Validation("effect", "deny grants not supported in current schema")
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO permissions (
			tenant_id, id, resource_type, resource_id,
			principal_type, principal_id,
			capability,
			granted_by, granted_at, valid_from, valid_to, expires_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`,
		p.TenantID, p.ID, string(p.ResourceType), p.ResourceID,
		string(p.PrincipalType), p.PrincipalID,
		string(p.Capability),
		p.GrantedBy, p.GrantedAt, p.ValidFrom, nullable(p.ValidTo), nullable(p.ExpiresAt),
	)
	return mapPgError(err)
}

// Revoke sets valid_to = at, returning the row so the caller can invalidate
// the right cache keys and emit the right event.
func (r *permissionRepo) Revoke(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, at time.Time) (*model.Permission, error) {
	row := tx.QueryRow(ctx, `
		UPDATE permissions
		SET valid_to = $3
		WHERE tenant_id = $1 AND id = $2
		  AND (valid_to IS NULL OR valid_to > $3)
		RETURNING `+selectPermissionCols+`
	`, tenantID, id, at)

	var p model.Permission
	var validTo, expiresAt *time.Time
	var capability string
	if err := row.Scan(
		&p.TenantID, &p.ID, (*string)(&p.ResourceType), &p.ResourceID,
		(*string)(&p.PrincipalType), &p.PrincipalID,
		&capability, &p.GrantedBy,
		&p.GrantedAt, &p.ValidFrom, &validTo, &expiresAt,
	); err != nil {
		return nil, mapPgError(err)
	}
	p.Capability = model.Capability(capability)
	p.Effect = model.EffectAllow
	p.ValidTo = validTo
	p.ExpiresAt = expiresAt
	return &p, nil
}

// ---- scanning helpers -----------------------------------------------------

func scanPermissions(rows pgx.Rows) ([]model.Permission, error) {
	var out []model.Permission
	for rows.Next() {
		var (
			p          model.Permission
			capability string
			validTo    *time.Time
			expires    *time.Time
		)
		if err := rows.Scan(
			&p.TenantID, &p.ID, (*string)(&p.ResourceType), &p.ResourceID,
			(*string)(&p.PrincipalType), &p.PrincipalID,
			&capability, &p.GrantedBy,
			&p.GrantedAt, &p.ValidFrom, &validTo, &expires,
		); err != nil {
			return nil, mapPgError(err)
		}
		p.Capability = model.Capability(capability)
		p.Effect = model.EffectAllow
		p.ValidTo = validTo
		p.ExpiresAt = expires
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, mapPgError(err)
	}
	return out, nil
}

func nullable(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}

// compile-time assertion
var _ PermissionRepo = (*permissionRepo)(nil)
