// Package repository persists webhook subscriptions, deliveries, and
// connector configs.
package repository

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vaultdms/vaultdms/services/connector/internal/model"
)

type Repository struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func newID() string { id, _ := uuid.NewV7(); return id.String() }

// ---- Webhook Subscriptions ------------------------------------------------

func (r *Repository) CreateWebhook(ctx context.Context, wh *model.WebhookSubscription) error {
	eventsJSON, _ := json.Marshal(wh.Events)
	_, err := r.pool.Exec(ctx, `
		INSERT INTO webhook_subscriptions (id, tenant_id, url, secret, events, active, created_by, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
	`, wh.ID, wh.TenantID, wh.URL, wh.Secret, eventsJSON, wh.Active, wh.CreatedBy, wh.CreatedAt)
	return err
}

func (r *Repository) ListWebhooks(ctx context.Context, tenantID string) ([]*model.WebhookSubscription, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, tenant_id, url, events, active, created_by, created_at
		FROM webhook_subscriptions WHERE tenant_id = $1 ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.WebhookSubscription
	for rows.Next() {
		wh := &model.WebhookSubscription{}
		var eventsJSON []byte
		if err := rows.Scan(&wh.ID, &wh.TenantID, &wh.URL, &eventsJSON, &wh.Active, &wh.CreatedBy, &wh.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(eventsJSON, &wh.Events)
		out = append(out, wh)
	}
	return out, rows.Err()
}

func (r *Repository) GetWebhook(ctx context.Context, tenantID, id string) (*model.WebhookSubscription, error) {
	wh := &model.WebhookSubscription{}
	var eventsJSON []byte
	err := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, url, secret, events, active, created_by, created_at
		FROM webhook_subscriptions WHERE tenant_id = $1 AND id = $2`, tenantID, id).
		Scan(&wh.ID, &wh.TenantID, &wh.URL, &wh.Secret, &eventsJSON, &wh.Active, &wh.CreatedBy, &wh.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	_ = json.Unmarshal(eventsJSON, &wh.Events)
	return wh, err
}

func (r *Repository) DeleteWebhook(ctx context.Context, tenantID, id string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM webhook_subscriptions WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	return err
}

// RotateSecret atomically swaps the webhook's HMAC secret. Returns
// the new secret (never log this). Used when an operator suspects a
// secret leak; old deliveries keep their signatures (already sent),
// new deliveries use the rotated secret.
func (r *Repository) RotateSecret(ctx context.Context, tenantID, id, newSecret string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE webhook_subscriptions SET secret = $1
		 WHERE tenant_id = $2 AND id = $3`, newSecret, tenantID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// GetDelivery returns a single delivery row for re-delivery.
func (r *Repository) GetDelivery(ctx context.Context, tenantID, deliveryID string) (*model.WebhookDelivery, error) {
	d := &model.WebhookDelivery{}
	err := r.pool.QueryRow(ctx, `
		SELECT id, subscription_id, tenant_id, event_type, payload, status_code,
		       response_body, attempts, next_retry_at, dead_lettered, created_at, delivered_at
		  FROM webhook_deliveries
		 WHERE tenant_id = $1 AND id = $2`, tenantID, deliveryID,
	).Scan(&d.ID, &d.SubscriptionID, &d.TenantID, &d.EventType, &d.Payload, &d.StatusCode,
		&d.ResponseBody, &d.Attempts, &d.NextRetryAt, &d.DeadLettered, &d.CreatedAt, &d.DeliveredAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return d, err
}

func (r *Repository) GetActiveWebhooksForEvent(ctx context.Context, tenantID, eventType string) ([]*model.WebhookSubscription, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, tenant_id, url, secret, events, active, created_by, created_at
		FROM webhook_subscriptions
		WHERE tenant_id = $1 AND active = true AND events @> $2::jsonb`, tenantID, `"`+eventType+`"`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.WebhookSubscription
	for rows.Next() {
		wh := &model.WebhookSubscription{}
		var eventsJSON []byte
		if err := rows.Scan(&wh.ID, &wh.TenantID, &wh.URL, &wh.Secret, &eventsJSON, &wh.Active, &wh.CreatedBy, &wh.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(eventsJSON, &wh.Events)
		out = append(out, wh)
	}
	return out, rows.Err()
}

// ---- Webhook Deliveries ---------------------------------------------------

func (r *Repository) InsertDelivery(ctx context.Context, d *model.WebhookDelivery) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO webhook_deliveries (id, subscription_id, tenant_id, event_type, payload, status_code,
			response_body, attempts, next_retry_at, dead_lettered, created_at, delivered_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
	`, d.ID, d.SubscriptionID, d.TenantID, d.EventType, d.Payload, d.StatusCode,
		d.ResponseBody, d.Attempts, d.NextRetryAt, d.DeadLettered, d.CreatedAt, d.DeliveredAt)
	return err
}

func (r *Repository) UpdateDelivery(ctx context.Context, d *model.WebhookDelivery) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE webhook_deliveries SET status_code=$1, response_body=$2, attempts=$3,
			next_retry_at=$4, dead_lettered=$5, delivered_at=$6
		WHERE id = $7`,
		d.StatusCode, d.ResponseBody, d.Attempts, d.NextRetryAt, d.DeadLettered, d.DeliveredAt, d.ID)
	return err
}

func (r *Repository) ListPendingDeliveries(ctx context.Context, limit int) ([]*model.WebhookDelivery, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, subscription_id, tenant_id, event_type, payload, status_code,
			response_body, attempts, next_retry_at, dead_lettered, created_at, delivered_at
		FROM webhook_deliveries
		WHERE delivered_at IS NULL AND dead_lettered = false AND (next_retry_at IS NULL OR next_retry_at <= now())
		ORDER BY created_at ASC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.WebhookDelivery
	for rows.Next() {
		d := &model.WebhookDelivery{}
		if err := rows.Scan(&d.ID, &d.SubscriptionID, &d.TenantID, &d.EventType, &d.Payload, &d.StatusCode,
			&d.ResponseBody, &d.Attempts, &d.NextRetryAt, &d.DeadLettered, &d.CreatedAt, &d.DeliveredAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *Repository) ListDeliveries(ctx context.Context, tenantID, subID string, limit int) ([]*model.WebhookDelivery, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, subscription_id, tenant_id, event_type, payload, status_code,
			response_body, attempts, next_retry_at, dead_lettered, created_at, delivered_at
		FROM webhook_deliveries
		WHERE tenant_id = $1 AND subscription_id = $2
		ORDER BY created_at DESC LIMIT $3`, tenantID, subID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.WebhookDelivery
	for rows.Next() {
		d := &model.WebhookDelivery{}
		if err := rows.Scan(&d.ID, &d.SubscriptionID, &d.TenantID, &d.EventType, &d.Payload, &d.StatusCode,
			&d.ResponseBody, &d.Attempts, &d.NextRetryAt, &d.DeadLettered, &d.CreatedAt, &d.DeliveredAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ---- Connector Configs ----------------------------------------------------

func (r *Repository) UpsertConnector(ctx context.Context, cc *model.ConnectorConfig) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO connector_configs (id, tenant_id, provider, config, status, created_at)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (tenant_id, provider) DO UPDATE SET config=EXCLUDED.config, status=EXCLUDED.status`,
		cc.ID, cc.TenantID, cc.Provider, cc.Config, cc.Status, cc.CreatedAt)
	return err
}

func (r *Repository) GetConnector(ctx context.Context, tenantID, provider string) (*model.ConnectorConfig, error) {
	cc := &model.ConnectorConfig{}
	err := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, provider, config, status, last_sync_at, error_message, created_at
		FROM connector_configs WHERE tenant_id = $1 AND provider = $2`, tenantID, provider).
		Scan(&cc.ID, &cc.TenantID, &cc.Provider, &cc.Config, &cc.Status, &cc.LastSyncAt, &cc.ErrorMessage, &cc.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return cc, err
}

func (r *Repository) ListConnectors(ctx context.Context, tenantID string) ([]*model.ConnectorConfig, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, tenant_id, provider, status, last_sync_at, error_message, created_at
		FROM connector_configs WHERE tenant_id = $1`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.ConnectorConfig
	for rows.Next() {
		cc := &model.ConnectorConfig{}
		if err := rows.Scan(&cc.ID, &cc.TenantID, &cc.Provider, &cc.Status, &cc.LastSyncAt, &cc.ErrorMessage, &cc.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, cc)
	}
	return out, rows.Err()
}
