// Per-tenant Twilio (SMS MFA) credentials. See migration 000044 for
// table definition. auth_token_sealed is the SealString output —
// callers seal/unseal at the service boundary.
package repository

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type TwilioConfig struct {
	TenantID         string
	AccountSID       string
	AuthTokenSealed  string
	VerifyServiceSID string
	UpdatedBy        string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (r *Repository) UpsertTwilioConfig(ctx context.Context, c *TwilioConfig) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO tenant_twilio_configs (
			tenant_id, account_sid, auth_token_sealed, verify_service_sid,
			updated_by, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,now(),now())
		ON CONFLICT (tenant_id) DO UPDATE SET
			account_sid = EXCLUDED.account_sid,
			auth_token_sealed = EXCLUDED.auth_token_sealed,
			verify_service_sid = EXCLUDED.verify_service_sid,
			updated_by = EXCLUDED.updated_by,
			updated_at = now()`,
		c.TenantID, c.AccountSID, c.AuthTokenSealed, c.VerifyServiceSID,
		nullableUUID(c.UpdatedBy))
	return err
}

func (r *Repository) GetTwilioConfig(ctx context.Context, tenantID string) (*TwilioConfig, error) {
	c := &TwilioConfig{}
	var updatedBy *string
	err := r.pool.QueryRow(ctx, `
		SELECT tenant_id, account_sid, auth_token_sealed, verify_service_sid,
			updated_by, created_at, updated_at
		FROM tenant_twilio_configs
		WHERE tenant_id = $1`,
		tenantID,
	).Scan(&c.TenantID, &c.AccountSID, &c.AuthTokenSealed, &c.VerifyServiceSID,
		&updatedBy, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if updatedBy != nil {
		c.UpdatedBy = *updatedBy
	}
	return c, nil
}

func (r *Repository) DeleteTwilioConfig(ctx context.Context, tenantID string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM tenant_twilio_configs WHERE tenant_id = $1`,
		tenantID)
	return err
}
