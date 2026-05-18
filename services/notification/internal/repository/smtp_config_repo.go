// Per-tenant SMTP credentials. See migration 000044 for the table.
// password_sealed is the SealString output — callers seal/unseal at
// the service boundary.
//
// Both the notification service (transactional) and the auth service
// (email OTP) read this table. Notification owns the write path
// (admin endpoints); auth reads through its own thin repo wrapper
// against the same physical table.
package repository

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type SMTPConfig struct {
	TenantID       string
	Host           string
	Port           int
	Username       string
	PasswordSealed string
	FromAddr       string
	StartTLS       bool
	UpdatedBy      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (r *Repository) UpsertSMTPConfig(ctx context.Context, c *SMTPConfig) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO tenant_smtp_configs (
			tenant_id, host, port, username, password_sealed,
			from_addr, starttls, updated_by, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,now(),now())
		ON CONFLICT (tenant_id) DO UPDATE SET
			host = EXCLUDED.host,
			port = EXCLUDED.port,
			username = EXCLUDED.username,
			password_sealed = EXCLUDED.password_sealed,
			from_addr = EXCLUDED.from_addr,
			starttls = EXCLUDED.starttls,
			updated_by = EXCLUDED.updated_by,
			updated_at = now()`,
		c.TenantID, c.Host, c.Port, c.Username, c.PasswordSealed,
		c.FromAddr, c.StartTLS, nullableUUIDSMTP(c.UpdatedBy))
	return err
}

func (r *Repository) GetSMTPConfig(ctx context.Context, tenantID string) (*SMTPConfig, error) {
	c := &SMTPConfig{}
	var updatedBy *string
	err := r.pool.QueryRow(ctx, `
		SELECT tenant_id, host, port, username, password_sealed,
			from_addr, starttls, updated_by, created_at, updated_at
		FROM tenant_smtp_configs
		WHERE tenant_id = $1`,
		tenantID,
	).Scan(&c.TenantID, &c.Host, &c.Port, &c.Username, &c.PasswordSealed,
		&c.FromAddr, &c.StartTLS, &updatedBy, &c.CreatedAt, &c.UpdatedAt)
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

func (r *Repository) DeleteSMTPConfig(ctx context.Context, tenantID string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM tenant_smtp_configs WHERE tenant_id = $1`,
		tenantID)
	return err
}

func nullableUUIDSMTP(s string) any {
	if s == "" {
		return nil
	}
	return s
}
