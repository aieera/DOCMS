// Read-only access to tenant_smtp_configs from the auth service so
// email OTP can pick up the same per-tenant credentials the
// notification service uses for transactional mail. Notification
// service owns the write side; auth only reads.
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
	UpdatedAt      time.Time
}

func (r *Repository) GetSMTPConfig(ctx context.Context, tenantID string) (*SMTPConfig, error) {
	c := &SMTPConfig{}
	err := r.pool.QueryRow(ctx, `
		SELECT tenant_id, host, port, username, password_sealed,
			from_addr, starttls, updated_at
		FROM tenant_smtp_configs
		WHERE tenant_id = $1`,
		tenantID,
	).Scan(&c.TenantID, &c.Host, &c.Port, &c.Username, &c.PasswordSealed,
		&c.FromAddr, &c.StartTLS, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

// UpsertSMTPConfig is the write side. SMTP is shared between
// auth (email OTP) and notification (transactional). Both services
// read this table; auth owns the write path because the admin UI
// for SMTP is wired through /api/v1/admin/notifications (auth router).
func (r *Repository) UpsertSMTPConfig(ctx context.Context, c *SMTPConfig, updatedBy string) error {
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
		c.FromAddr, c.StartTLS, nullableUUID(updatedBy))
	return err
}

func (r *Repository) DeleteSMTPConfig(ctx context.Context, tenantID string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM tenant_smtp_configs WHERE tenant_id = $1`,
		tenantID)
	return err
}
