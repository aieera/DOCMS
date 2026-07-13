// Package repository persists webhook subscriptions, deliveries, and
// connector configs.
//
// RLS contract (Wave A.1, issue #71): webhook_subscriptions,
// webhook_deliveries, and connector_configs are FORCE ROW LEVEL
// SECURITY, so every method here runs inside database.WithTenantTx via
// withTenant — under the prod NOBYPASSRLS role a raw-pool query fails
// closed (0-row reads / rejected writes), which is how webhook delivery
// and OAuth-token loads were silently dead in prod. The tenant_id SQL
// predicates stay as defense-in-depth. The delivery worker's pending
// scan is legitimately cross-tenant and uses the sanctioned
// enumerate-tenants shape (database.ListTenantIDs) — never
// row_security=off.
package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/services/connector/internal/model"
)

type Repository struct {
	pool   *pgxpool.Pool
	outbox *database.OutboxRepository
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, outbox: database.NewOutboxRepository()}
}

// withTenant opens a tenant-scoped transaction (SET LOCAL
// app.current_tenant) and runs fn inside it — the A.1.a template.
func (r *Repository) withTenant(ctx context.Context, tenantID string, fn func(tx pgx.Tx) error) error {
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		return fmt.Errorf("tenant_id: %w", err)
	}
	return database.WithTenantTx(ctx, r.pool, tid, fn)
}

// EmitOutbox writes a domain event to the transactional outbox (§4.7), which
// the connector's NewOutboxPublisher forwards to NATS. Used for
// dms.connector.synced.v1 — never a direct NATS publish. Runs in a tenant tx
// so the outbox table's RLS insert policy passes.
func (r *Repository) EmitOutbox(ctx context.Context, tenantID uuid.UUID, eventType, aggregateType string, aggregateID uuid.UUID, payload []byte) error {
	ev := database.NewOutboxEvent(tenantID, eventType, aggregateType, aggregateID, payload)
	return database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		return r.outbox.Insert(ctx, tx, ev)
	})
}

func newID() string { id, _ := uuid.NewV7(); return id.String() }

// ---- Webhook Subscriptions ------------------------------------------------

func (r *Repository) CreateWebhook(ctx context.Context, wh *model.WebhookSubscription) error {
	eventsJSON, _ := json.Marshal(wh.Events)
	return r.withTenant(ctx, wh.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO webhook_subscriptions (id, tenant_id, url, secret, events, active, created_by, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		`, wh.ID, wh.TenantID, wh.URL, wh.Secret, eventsJSON, wh.Active, wh.CreatedBy, wh.CreatedAt)
		return err
	})
}

func (r *Repository) ListWebhooks(ctx context.Context, tenantID string) ([]*model.WebhookSubscription, error) {
	var out []*model.WebhookSubscription
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, tenant_id, url, events, active, created_by, created_at
			FROM webhook_subscriptions WHERE tenant_id = $1 ORDER BY created_at DESC`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			wh := &model.WebhookSubscription{}
			var eventsJSON []byte
			if err := rows.Scan(&wh.ID, &wh.TenantID, &wh.URL, &eventsJSON, &wh.Active, &wh.CreatedBy, &wh.CreatedAt); err != nil {
				return err
			}
			_ = json.Unmarshal(eventsJSON, &wh.Events)
			out = append(out, wh)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Repository) GetWebhook(ctx context.Context, tenantID, id string) (*model.WebhookSubscription, error) {
	var out *model.WebhookSubscription
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		wh := &model.WebhookSubscription{}
		var eventsJSON []byte
		err := tx.QueryRow(ctx, `
			SELECT id, tenant_id, url, secret, events, active, created_by, created_at
			FROM webhook_subscriptions WHERE tenant_id = $1 AND id = $2`, tenantID, id).
			Scan(&wh.ID, &wh.TenantID, &wh.URL, &wh.Secret, &eventsJSON, &wh.Active, &wh.CreatedBy, &wh.CreatedAt)
		if err == pgx.ErrNoRows {
			return nil // preserve (nil, nil) not-found contract
		}
		if err != nil {
			return err
		}
		_ = json.Unmarshal(eventsJSON, &wh.Events)
		out = wh
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Repository) DeleteWebhook(ctx context.Context, tenantID, id string) error {
	return r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM webhook_subscriptions WHERE tenant_id = $1 AND id = $2`, tenantID, id)
		return err
	})
}

// RotateSecret atomically swaps the webhook's HMAC secret. Returns
// the new secret (never log this). Used when an operator suspects a
// secret leak; old deliveries keep their signatures (already sent),
// new deliveries use the rotated secret.
func (r *Repository) RotateSecret(ctx context.Context, tenantID, id, newSecret string) error {
	return r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE webhook_subscriptions SET secret = $1
			 WHERE tenant_id = $2 AND id = $3`, newSecret, tenantID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
		return nil
	})
}

// GetDelivery returns a single delivery row for re-delivery.
//
// Schema column-name mapping (the model fields kept their original
// Go names for back-compat with handlers, but the table uses the
// per-000037-migration names):
//
//	model.StatusCode    ← http_status
//	model.ResponseBody  ← error_message  (closest semantic match)
//	model.DeadLettered  ← (status = 'dead_letter')
//	model.DeliveredAt   ← (status = 'delivered' ? last_attempt_at : NULL)
func (r *Repository) GetDelivery(ctx context.Context, tenantID, deliveryID string) (*model.WebhookDelivery, error) {
	var out *model.WebhookDelivery
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		d := &model.WebhookDelivery{}
		var status string
		var lastAttempt *time.Time
		err := tx.QueryRow(ctx, `
			SELECT id, subscription_id, tenant_id, event_type, payload,
			       COALESCE(http_status, 0), COALESCE(error_message, ''),
			       attempts, next_retry_at, status, created_at, last_attempt_at
			  FROM webhook_deliveries
			 WHERE tenant_id = $1 AND id = $2`, tenantID, deliveryID,
		).Scan(&d.ID, &d.SubscriptionID, &d.TenantID, &d.EventType, &d.Payload,
			&d.StatusCode, &d.ResponseBody, &d.Attempts, &d.NextRetryAt,
			&status, &d.CreatedAt, &lastAttempt)
		if err == pgx.ErrNoRows {
			return nil // preserve (nil, nil) not-found contract
		}
		if err != nil {
			return err
		}
		d.DeadLettered = status == "dead_letter"
		if status == "delivered" {
			d.DeliveredAt = lastAttempt
		}
		out = d
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Repository) GetActiveWebhooksForEvent(ctx context.Context, tenantID, eventType string) ([]*model.WebhookSubscription, error) {
	var out []*model.WebhookSubscription
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, tenant_id, url, secret, events, active, created_by, created_at
			FROM webhook_subscriptions
			WHERE tenant_id = $1 AND active = true AND events @> $2::jsonb`, tenantID, `"`+eventType+`"`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			wh := &model.WebhookSubscription{}
			var eventsJSON []byte
			if err := rows.Scan(&wh.ID, &wh.TenantID, &wh.URL, &wh.Secret, &eventsJSON, &wh.Active, &wh.CreatedBy, &wh.CreatedAt); err != nil {
				return err
			}
			_ = json.Unmarshal(eventsJSON, &wh.Events)
			out = append(out, wh)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ---- Webhook Deliveries ---------------------------------------------------

// statusFromModel projects the model's two bools onto the actual
// status column. Order matters: dead-letter is terminal, delivered
// next, then pending vs failed (failed if there's an http_status).
func statusFromModel(d *model.WebhookDelivery) string {
	if d.DeadLettered {
		return "dead_letter"
	}
	if d.DeliveredAt != nil {
		return "delivered"
	}
	if d.Attempts > 0 {
		return "failed"
	}
	return "pending"
}

func (r *Repository) InsertDelivery(ctx context.Context, d *model.WebhookDelivery) error {
	// event_id is NOT NULL in the schema. The model carries the
	// delivery's own ID at insert time; the source event id flows
	// through the EventType / Payload pair, so reuse the delivery
	// id as the event id when no separate one is supplied.
	return r.withTenant(ctx, d.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO webhook_deliveries (
				id, subscription_id, tenant_id, event_type, event_id, payload,
				status, http_status, error_message, attempts, next_retry_at,
				last_attempt_at, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,NULLIF($8,0),NULLIF($9,''),$10,$11,$12,$13)
		`, d.ID, d.SubscriptionID, d.TenantID, d.EventType, d.ID, d.Payload,
			statusFromModel(d), d.StatusCode, d.ResponseBody, d.Attempts,
			d.NextRetryAt, d.DeliveredAt, d.CreatedAt)
		return err
	})
}

func (r *Repository) UpdateDelivery(ctx context.Context, d *model.WebhookDelivery) error {
	// Tenant predicate added alongside the tenant tx: the previous
	// WHERE id-only form relied on nothing at all under the dev bypass.
	return r.withTenant(ctx, d.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE webhook_deliveries SET
				status = $1,
				http_status = NULLIF($2, 0),
				error_message = NULLIF($3, ''),
				attempts = $4,
				next_retry_at = $5,
				last_attempt_at = $6
			WHERE id = $7 AND tenant_id = $8`,
			statusFromModel(d), d.StatusCode, d.ResponseBody, d.Attempts,
			d.NextRetryAt, d.DeliveredAt, d.ID, d.TenantID)
		return err
	})
}

// ListPendingDeliveries feeds the delivery worker. It is legitimately
// cross-tenant, so it uses the sanctioned shape (Wave A.1, issue #71):
// enumerate tenants from the organizations registry, then read each
// tenant's due deliveries under that tenant's RLS context — the raw
// cross-tenant scan returned 0 rows under prod NOBYPASSRLS, silently
// stopping webhook delivery for every tenant. Results are merged
// oldest-first across tenants and trimmed to limit.
func (r *Repository) ListPendingDeliveries(ctx context.Context, limit int) ([]*model.WebhookDelivery, error) {
	tenants, err := database.ListTenantIDs(ctx, r.pool)
	if err != nil {
		return nil, err
	}
	var out []*model.WebhookDelivery
	for _, tid := range tenants {
		err := database.WithTenantTx(ctx, r.pool, tid, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `
				SELECT id, subscription_id, tenant_id, event_type, payload,
				       COALESCE(http_status, 0), COALESCE(error_message, ''),
				       attempts, next_retry_at, status, created_at, last_attempt_at
				FROM webhook_deliveries
				WHERE tenant_id = $1
				  AND status IN ('pending', 'failed')
				  AND (next_retry_at IS NULL OR next_retry_at <= now())
				ORDER BY created_at ASC LIMIT $2`, tid, limit)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				d := &model.WebhookDelivery{}
				var status string
				var lastAttempt *time.Time
				if err := rows.Scan(&d.ID, &d.SubscriptionID, &d.TenantID, &d.EventType, &d.Payload,
					&d.StatusCode, &d.ResponseBody, &d.Attempts, &d.NextRetryAt,
					&status, &d.CreatedAt, &lastAttempt); err != nil {
					return err
				}
				d.DeadLettered = status == "dead_letter"
				if status == "delivered" {
					d.DeliveredAt = lastAttempt
				}
				out = append(out, d)
			}
			return rows.Err()
		})
		if err != nil {
			return nil, fmt.Errorf("pending deliveries for tenant %s: %w", tid, err)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *Repository) ListDeliveries(ctx context.Context, tenantID, subID string, limit int) ([]*model.WebhookDelivery, error) {
	if limit <= 0 {
		limit = 50
	}
	var out []*model.WebhookDelivery
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, subscription_id, tenant_id, event_type, payload,
			       COALESCE(http_status, 0), COALESCE(error_message, ''),
			       attempts, next_retry_at, status, created_at, last_attempt_at
			FROM webhook_deliveries
			WHERE tenant_id = $1 AND subscription_id = $2
			ORDER BY created_at DESC LIMIT $3`, tenantID, subID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d := &model.WebhookDelivery{}
			var status string
			var lastAttempt *time.Time
			if err := rows.Scan(&d.ID, &d.SubscriptionID, &d.TenantID, &d.EventType, &d.Payload,
				&d.StatusCode, &d.ResponseBody, &d.Attempts, &d.NextRetryAt,
				&status, &d.CreatedAt, &lastAttempt); err != nil {
				return err
			}
			d.DeadLettered = status == "dead_letter"
			if status == "delivered" {
				d.DeliveredAt = lastAttempt
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ---- Connector Configs ----------------------------------------------------

// UpsertConnector inserts or updates a connector_configs row. Uniqueness
// is keyed on (tenant_id, connector_type) — a tenant gets one config per
// vendor. config_encrypted + oauth_tokens_encrypted are JSONB columns
// wrapping sealed bytes; callers pass already-sealed values.
func (r *Repository) UpsertConnector(ctx context.Context, cc *model.ConnectorConfig) error {
	if cc.ID == "" {
		cc.ID = newID()
	}
	// Wrap sealed bytes as {"sealed":"<base64>"} so the JSONB column
	// stays valid JSON and we can update only the secret payload
	// without touching the rest of the row's metadata.
	cfgJSON := []byte(`{"sealed":""}`)
	if len(cc.ConfigEncrypted) > 0 {
		cfgJSON = wrapSealed(cc.ConfigEncrypted)
	}
	tokJSON := []byte(`{"sealed":""}`)
	if len(cc.OAuthTokensEncrypted) > 0 {
		tokJSON = wrapSealed(cc.OAuthTokensEncrypted)
	}
	return r.withTenant(ctx, cc.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO connector_configs (
				id, tenant_id, connector_type, display_name,
				config_encrypted, oauth_tokens_encrypted, is_active,
				created_by, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5::jsonb,$6::jsonb,$7,$8,now(),now())
			ON CONFLICT (tenant_id, connector_type) DO UPDATE SET
				display_name = EXCLUDED.display_name,
				config_encrypted = EXCLUDED.config_encrypted,
				oauth_tokens_encrypted = EXCLUDED.oauth_tokens_encrypted,
				is_active = EXCLUDED.is_active,
				updated_at = now()`,
			cc.ID, cc.TenantID, cc.ConnectorType, cc.DisplayName,
			string(cfgJSON), string(tokJSON), cc.IsActive,
			nullableUUIDConnector(cc.CreatedBy))
		return err
	})
}

// UpdateConnectorTokens overwrites just the OAuth tokens — used post-callback
// and on token-refresh. Skips touching the config + display_name.
func (r *Repository) UpdateConnectorTokens(ctx context.Context, tenantID, connectorType string, sealedTokens []byte) error {
	return r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE connector_configs
			   SET oauth_tokens_encrypted = $3::jsonb, sync_status = 'authorized', updated_at = now()
			 WHERE tenant_id = $1 AND connector_type = $2`,
			tenantID, connectorType, string(wrapSealed(sealedTokens)))
		return err
	})
}

func (r *Repository) GetConnector(ctx context.Context, tenantID, connectorType string) (*model.ConnectorConfig, error) {
	var out *model.ConnectorConfig
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		cc := &model.ConnectorConfig{}
		var cfgJSON, tokJSON []byte
		var syncStatus, createdBy *string
		err := tx.QueryRow(ctx, `
			SELECT id, tenant_id, connector_type, display_name,
				config_encrypted, oauth_tokens_encrypted, is_active,
				last_sync_at, sync_status, created_by, created_at, updated_at
			FROM connector_configs WHERE tenant_id = $1 AND connector_type = $2`,
			tenantID, connectorType,
		).Scan(&cc.ID, &cc.TenantID, &cc.ConnectorType, &cc.DisplayName,
			&cfgJSON, &tokJSON, &cc.IsActive,
			&cc.LastSyncAt, &syncStatus, &createdBy, &cc.CreatedAt, &cc.UpdatedAt)
		if err == pgx.ErrNoRows {
			return nil // preserve (nil, nil) not-found contract
		}
		if err != nil {
			return err
		}
		if syncStatus != nil {
			cc.SyncStatus = *syncStatus
		}
		if createdBy != nil {
			cc.CreatedBy = *createdBy
		}
		cc.ConfigEncrypted = unwrapSealed(cfgJSON)
		cc.OAuthTokensEncrypted = unwrapSealed(tokJSON)
		out = cc
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Repository) ListConnectors(ctx context.Context, tenantID string) ([]*model.ConnectorConfig, error) {
	var out []*model.ConnectorConfig
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, tenant_id, connector_type, display_name, is_active,
				last_sync_at, sync_status, created_at, updated_at
			FROM connector_configs WHERE tenant_id = $1
			ORDER BY connector_type`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			cc := &model.ConnectorConfig{}
			var syncStatus *string
			if err := rows.Scan(&cc.ID, &cc.TenantID, &cc.ConnectorType, &cc.DisplayName,
				&cc.IsActive, &cc.LastSyncAt, &syncStatus, &cc.CreatedAt, &cc.UpdatedAt); err != nil {
				return err
			}
			if syncStatus != nil {
				cc.SyncStatus = *syncStatus
			}
			out = append(out, cc)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ---- helpers --------------------------------------------------------------

// wrapSealed packs raw sealed bytes into {"sealed":"<base64>"} so the
// JSONB column stores valid JSON. The service layer seals/unseals at
// its boundary; this layer just transports opaque bytes.
func wrapSealed(sealed []byte) []byte {
	if len(sealed) == 0 {
		return []byte(`{"sealed":""}`)
	}
	payload := map[string]string{"sealed": base64.StdEncoding.EncodeToString(sealed)}
	out, _ := json.Marshal(payload)
	return out
}

// unwrapSealed reverses wrapSealed. Empty / malformed JSON yields nil
// so callers see "no value" rather than a decode error.
func unwrapSealed(jsonb []byte) []byte {
	if len(jsonb) == 0 {
		return nil
	}
	var w struct {
		Sealed string `json:"sealed"`
	}
	if err := json.Unmarshal(jsonb, &w); err != nil {
		return nil
	}
	if w.Sealed == "" {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(w.Sealed)
	if err != nil {
		return nil
	}
	return raw
}

// nullableUUIDConnector returns nil for empty strings so an FK column
// can be inserted as NULL when no caller is recorded.
func nullableUUIDConnector(s string) any {
	if s == "" {
		return nil
	}
	return s
}
