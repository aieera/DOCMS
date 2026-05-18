// Per-tenant DocuSign / Adobe Sign OAuth client credentials. See the
// 000043 migration for the rationale on moving these from env vars
// to per-(tenant, provider) rows.
package repository

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ESignProviderConfig mirrors esign_provider_configs. ClientSecretSealed
// is the SealString output — callers pass ciphertext in, get ciphertext
// out. The service layer Seal/Unseals at the boundary.
type ESignProviderConfig struct {
	TenantID             string
	Provider             string
	ClientID             string
	ClientSecretSealed   string
	Environment          string // 'sandbox' | 'production'
	AuthorizeURLOverride string
	TokenURLOverride     string
	Region               string
	ConfiguredBy         string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

func (r *Repository) UpsertESignProviderConfig(ctx context.Context, c *ESignProviderConfig) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO esign_provider_configs (
			tenant_id, provider, client_id, client_secret_sealed,
			environment, authorize_url_override, token_url_override,
			region, configured_by, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,NULLIF($6,''),NULLIF($7,''),NULLIF($8,''),$9,now(),now())
		ON CONFLICT (tenant_id, provider) DO UPDATE SET
			client_id = EXCLUDED.client_id,
			client_secret_sealed = EXCLUDED.client_secret_sealed,
			environment = EXCLUDED.environment,
			authorize_url_override = EXCLUDED.authorize_url_override,
			token_url_override = EXCLUDED.token_url_override,
			region = EXCLUDED.region,
			configured_by = EXCLUDED.configured_by,
			updated_at = now()`,
		c.TenantID, c.Provider, c.ClientID, c.ClientSecretSealed,
		c.Environment, c.AuthorizeURLOverride, c.TokenURLOverride,
		c.Region, nullableUUID(c.ConfiguredBy))
	return err
}

// GetESignProviderConfig returns the row or (nil, nil) if no config
// has been saved yet for this (tenant, provider).
func (r *Repository) GetESignProviderConfig(ctx context.Context, tenantID, provider string) (*ESignProviderConfig, error) {
	c := &ESignProviderConfig{}
	var authzOverride, tokenOverride, region, configuredBy *string
	err := r.pool.QueryRow(ctx, `
		SELECT tenant_id, provider, client_id, client_secret_sealed,
			environment, authorize_url_override, token_url_override,
			region, configured_by, created_at, updated_at
		FROM esign_provider_configs
		WHERE tenant_id = $1 AND provider = $2`,
		tenantID, provider,
	).Scan(&c.TenantID, &c.Provider, &c.ClientID, &c.ClientSecretSealed,
		&c.Environment, &authzOverride, &tokenOverride, &region,
		&configuredBy, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if authzOverride != nil {
		c.AuthorizeURLOverride = *authzOverride
	}
	if tokenOverride != nil {
		c.TokenURLOverride = *tokenOverride
	}
	if region != nil {
		c.Region = *region
	}
	if configuredBy != nil {
		c.ConfiguredBy = *configuredBy
	}
	return c, nil
}

func (r *Repository) DeleteESignProviderConfig(ctx context.Context, tenantID, provider string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM esign_provider_configs WHERE tenant_id = $1 AND provider = $2`,
		tenantID, provider)
	return err
}
