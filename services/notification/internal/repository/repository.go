// Package repository persists notifications and user preferences.
package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vaultdms/vaultdms/services/notification/internal/model"
)

// Repository manages the notifications + notification_preferences tables.
type Repository struct{ pool *pgxpool.Pool }

// New constructs a Repository.
func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// Insert stores a notification.
func (r *Repository) Insert(ctx context.Context, n *model.Notification) error {
	if n.ID == "" {
		id, _ := uuid.NewV7()
		n.ID = id.String()
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO notifications (id, tenant_id, user_id, type, title, body, resource_type, resource_id, channel, read, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
	`, n.ID, n.TenantID, n.UserID, n.Type, n.Title, n.Body, n.ResourceType, n.ResourceID, n.Channel, false, n.CreatedAt)
	return err
}

// List returns notifications for a user, newest first.
func (r *Repository) List(ctx context.Context, tenantID, userID string, readFilter *bool, limit int) ([]*model.Notification, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	q := `SELECT id, tenant_id, user_id, type, title, body, resource_type, resource_id, channel, read, delivered_at, read_at, created_at
		FROM notifications WHERE tenant_id = $1 AND user_id = $2`
	args := []any{tenantID, userID}
	if readFilter != nil {
		q += ` AND read = $3`
		args = append(args, *readFilter)
	}
	q += ` ORDER BY created_at DESC LIMIT ` + itoa(limit)
	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Notification
	for rows.Next() {
		n := &model.Notification{}
		if err := rows.Scan(&n.ID, &n.TenantID, &n.UserID, &n.Type, &n.Title, &n.Body,
			&n.ResourceType, &n.ResourceID, &n.Channel, &n.Read, &n.DeliveredAt, &n.ReadAt, &n.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// MarkRead marks one notification as read.
func (r *Repository) MarkRead(ctx context.Context, tenantID, userID, id string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE notifications SET read = true, read_at = $1 WHERE tenant_id = $2 AND user_id = $3 AND id = $4`,
		time.Now().UTC(), tenantID, userID, id)
	return err
}

// MarkAllRead marks all user's notifications as read.
func (r *Repository) MarkAllRead(ctx context.Context, tenantID, userID string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE notifications SET read = true, read_at = $1 WHERE tenant_id = $2 AND user_id = $3 AND read = false`,
		time.Now().UTC(), tenantID, userID)
	return err
}

// UnreadCount returns the number of unread notifications.
func (r *Repository) UnreadCount(ctx context.Context, tenantID, userID string) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM notifications WHERE tenant_id = $1 AND user_id = $2 AND read = false`,
		tenantID, userID).Scan(&count)
	return count, err
}

// GetPreference returns user notification preferences.
func (r *Repository) GetPreference(ctx context.Context, tenantID, userID string) (*model.UserPreference, error) {
	p := &model.UserPreference{}
	err := r.pool.QueryRow(ctx,
		`SELECT tenant_id, user_id, email_enabled, push_enabled, slack_enabled, sms_enabled, quiet_hours_from, quiet_hours_to
		 FROM notification_preferences WHERE tenant_id = $1 AND user_id = $2`,
		tenantID, userID).Scan(&p.TenantID, &p.UserID, &p.EmailEnabled, &p.PushEnabled, &p.SlackEnabled, &p.SMSEnabled, &p.QuietHoursFrom, &p.QuietHoursTo)
	if err == pgx.ErrNoRows {
		return &model.UserPreference{TenantID: tenantID, UserID: userID, EmailEnabled: true, PushEnabled: true}, nil
	}
	return p, err
}

// UpsertPreference saves user notification preferences.
func (r *Repository) UpsertPreference(ctx context.Context, p *model.UserPreference) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO notification_preferences (tenant_id, user_id, email_enabled, push_enabled, slack_enabled, sms_enabled, quiet_hours_from, quiet_hours_to)
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
