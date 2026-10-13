package sso

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
)

// ConfigRepository reads per-tenant SSO configurations. Writes (admin
// config-management endpoints) land in Phase A2.1; for now admins populate
// the table via SQL fixtures.
type ConfigRepository interface {
	GetActiveByTenantProvider(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, p ProviderType) (*SSOConfig, error)
	GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*SSOConfig, error)
}

type configRepo struct{}

// NewConfigRepo constructs a stateless repo.
func NewConfigRepo() ConfigRepository { return &configRepo{} }

// GetActiveByTenantProvider is the hot-path lookup called by the SAML
// handler on every SP-initiated login: "for this tenant, what's the live
// SAML config?" Returns ErrNotFound when no active row exists.
//
// sso_configs is FORCE ROW LEVEL SECURITY: the read runs in the tenant's
// transaction (app.current_tenant set) — on the raw pool the production
// NOBYPASSRLS role sees no rows (M-27). tenant_id stays in the WHERE
// clause for defense-in-depth.
func (r *configRepo) GetActiveByTenantProvider(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, p ProviderType) (*SSOConfig, error) {
	var cfg *SSOConfig
	err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			SELECT tenant_id, id, provider_type, display_name, config,
			       is_active, created_at, updated_at
			FROM sso_configs
			WHERE tenant_id = $1 AND provider_type = $2 AND is_active
			ORDER BY updated_at DESC
			LIMIT 1
		`, tenantID, string(p))
		c, err := scanSSOConfig(row)
		cfg = c
		return err
	})
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

func (r *configRepo) GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*SSOConfig, error) {
	row := tx.QueryRow(ctx, `
		SELECT tenant_id, id, provider_type, display_name, config,
		       is_active, created_at, updated_at
		FROM sso_configs
		WHERE tenant_id = $1 AND id = $2
	`, tenantID, id)
	return scanSSOConfig(row)
}

// ---- scan helper ----------------------------------------------------------

type rowScanner interface{ Scan(...any) error }

func scanSSOConfig(s rowScanner) (*SSOConfig, error) {
	var (
		c   SSOConfig
		pt  string
		raw []byte
	)
	if err := s.Scan(&c.TenantID, &c.ID, &pt, &c.DisplayName, &raw,
		&c.IsActive, &c.CreatedAt, &c.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, vdmserr.Wrap(vdmserr.ErrNotFound, err)
		}
		return nil, fmt.Errorf("sso_configs scan: %w", err)
	}
	c.Provider = ProviderType(pt)
	c.Config = raw
	return &c, nil
}
