// Package repository persists notifications and user preferences.
package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/services/notification/internal/model"
)

// Repository manages the notifications + notification_preferences tables.
// RLS contract (Wave A.1, issue #73): notifications and the ADR-0086
// preference tables are FORCE ROW LEVEL SECURITY — every method on them
// runs inside database.WithTenantTx via withTenant (A.1.a template).
// Deliberate raw-pool exceptions, documented in their DDL: push_devices
// and notification_user_prefs (no RLS by convention).
type Repository struct{ pool *pgxpool.Pool }

// New constructs a Repository.
func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// withTenant opens a tenant-scoped transaction (SET LOCAL
// app.current_tenant) and runs fn inside it.
func (r *Repository) withTenant(ctx context.Context, tenantID string, fn func(tx pgx.Tx) error) error {
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		return fmt.Errorf("tenant_id: %w", err)
	}
	return database.WithTenantTx(ctx, r.pool, tid, fn)
}

// Insert stores a notification.
func (r *Repository) Insert(ctx context.Context, n *model.Notification) error {
	if n.ID == "" {
		id, _ := uuid.NewV7()
		n.ID = id.String()
	}
	return r.withTenant(ctx, n.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO notifications (id, tenant_id, user_id, type, title, body, resource_type, resource_id, channel, read, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		`, n.ID, n.TenantID, n.UserID, n.Type, n.Title, n.Body, n.ResourceType, n.ResourceID, n.Channel, false, n.CreatedAt)
		return err
	})
}

// List returns notifications for a user, newest first.
func (r *Repository) List(ctx context.Context, tenantID, userID string, readFilter *bool, limit int) ([]*model.Notification, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	// Nullable text columns (type, body, resource_type, resource_id, channel)
	// are COALESCE'd to '' so the plain-string scan destinations don't
	// fail with "cannot scan NULL into *string". delivered_at + read_at
	// stay nullable timestamps (scanned into *time.Time, NULL safe).
	q := `SELECT id, tenant_id, user_id,
		COALESCE(type, ''), title, COALESCE(body, ''),
		COALESCE(resource_type, ''), COALESCE(resource_id::text, ''),
		COALESCE(channel, ''),
		read, delivered_at, read_at, created_at
		FROM notifications WHERE tenant_id = $1 AND user_id = $2`
	args := []any{tenantID, userID}
	if readFilter != nil {
		q += ` AND read = $3`
		args = append(args, *readFilter)
	}
	q += ` ORDER BY created_at DESC LIMIT ` + itoa(limit)
	var out []*model.Notification
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			n := &model.Notification{}
			if err := rows.Scan(&n.ID, &n.TenantID, &n.UserID, &n.Type, &n.Title, &n.Body,
				&n.ResourceType, &n.ResourceID, &n.Channel, &n.Read, &n.DeliveredAt, &n.ReadAt, &n.CreatedAt); err != nil {
				return err
			}
			out = append(out, n)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// MarkRead marks one notification as read.
func (r *Repository) MarkRead(ctx context.Context, tenantID, userID, id string) error {
	return r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE notifications SET read = true, read_at = $1 WHERE tenant_id = $2 AND user_id = $3 AND id = $4`,
			time.Now().UTC(), tenantID, userID, id)
		return err
	})
}

// MarkAllRead marks all user's notifications as read.
func (r *Repository) MarkAllRead(ctx context.Context, tenantID, userID string) error {
	return r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE notifications SET read = true, read_at = $1 WHERE tenant_id = $2 AND user_id = $3 AND read = false`,
			time.Now().UTC(), tenantID, userID)
		return err
	})
}

// UnreadCount returns the number of unread notifications.
func (r *Repository) UnreadCount(ctx context.Context, tenantID, userID string) (int, error) {
	var count int
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM notifications WHERE tenant_id = $1 AND user_id = $2 AND read = false`,
			tenantID, userID).Scan(&count)
	})
	return count, err
}

// GetPreference returns user notification preferences.
func (r *Repository) GetPreference(ctx context.Context, tenantID, userID string) (*model.UserPreference, error) {
	// notification_user_prefs is the FLAT per-user row (notification
	// migration 000003). The similarly-named notification_preferences
	// table is the ADR 0086 MATRIX — selecting flat columns from it
	// errored on every call and silently zeroed the email/push gates.
	p := &model.UserPreference{}
	err := r.pool.QueryRow(ctx,
		`SELECT tenant_id, user_id, email_enabled, push_enabled, slack_enabled, sms_enabled, quiet_hours_from, quiet_hours_to
		 FROM notification_user_prefs WHERE tenant_id = $1 AND user_id = $2`,
		tenantID, userID).Scan(&p.TenantID, &p.UserID, &p.EmailEnabled, &p.PushEnabled, &p.SlackEnabled, &p.SMSEnabled, &p.QuietHoursFrom, &p.QuietHoursTo)
	if err == pgx.ErrNoRows {
		return &model.UserPreference{TenantID: tenantID, UserID: userID, EmailEnabled: true, PushEnabled: true}, nil
	}
	if err != nil {
		// Surface hard failures as (nil, err) so callers can apply an
		// explicit default instead of trusting a zero-value struct
		// whose false switches silently disable delivery.
		return nil, err
	}
	return p, nil
}

// UpsertPreference saves user notification preferences.
func (r *Repository) UpsertPreference(ctx context.Context, p *model.UserPreference) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO notification_user_prefs (tenant_id, user_id, email_enabled, push_enabled, slack_enabled, sms_enabled, quiet_hours_from, quiet_hours_to)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (tenant_id, user_id) DO UPDATE SET
			email_enabled = EXCLUDED.email_enabled, push_enabled = EXCLUDED.push_enabled,
			slack_enabled = EXCLUDED.slack_enabled, sms_enabled = EXCLUDED.sms_enabled,
			quiet_hours_from = EXCLUDED.quiet_hours_from, quiet_hours_to = EXCLUDED.quiet_hours_to
	`, p.TenantID, p.UserID, p.EmailEnabled, p.PushEnabled, p.SlackEnabled, p.SMSEnabled, p.QuietHoursFrom, p.QuietHoursTo)
	return err
}

func itoa(n int) string {
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}
