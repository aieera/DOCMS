// Package repository manages billing tables (subscriptions, usage_records).
package repository

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/services/billing/internal/model"
)

type Repository struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// ---- Organizations --------------------------------------------------------

func (r *Repository) CreateOrg(ctx context.Context, name, plan, region string) (string, error) {
	id, _ := uuid.NewV7()
	flags := model.DefaultFlagsByPlan(plan)
	settings, _ := json.Marshal(map[string]any{"features": flags})
	_, err := r.pool.Exec(ctx, `
		INSERT INTO organizations (id, name, plan, region, settings, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, id, name, plan, region, settings, time.Now().UTC())
	return id.String(), err
}

func (r *Repository) GetOrg(ctx context.Context, tenantID string) (map[string]any, error) {
	var name, plan, region string
	var settings json.RawMessage
	err := r.pool.QueryRow(ctx,
		`SELECT name, plan, region, COALESCE(settings, '{}') FROM organizations WHERE id = $1`, tenantID).
		Scan(&name, &plan, &region, &settings)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return map[string]any{"name": name, "plan": plan, "region": region, "settings": settings}, err
}

func (r *Repository) SuspendOrg(ctx context.Context, tenantID string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE organizations SET plan = plan || '_suspended', updated_at = $1 WHERE id = $2`,
		time.Now().UTC(), tenantID)
	return err
}

// ---- Subscriptions --------------------------------------------------------

func (r *Repository) UpsertSubscription(ctx context.Context, sub *model.Subscription) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO subscriptions (tenant_id, plan_id, stripe_customer_id, stripe_subscription_id, status,
			grace_period_ends, current_period_start, current_period_end, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (tenant_id) DO UPDATE SET
			plan_id = EXCLUDED.plan_id, stripe_customer_id = EXCLUDED.stripe_customer_id,
			stripe_subscription_id = EXCLUDED.stripe_subscription_id, status = EXCLUDED.status,
			grace_period_ends = EXCLUDED.grace_period_ends,
			current_period_start = EXCLUDED.current_period_start, current_period_end = EXCLUDED.current_period_end
	`, sub.TenantID, sub.PlanID, sub.StripeCustomerID, sub.StripeSubID, sub.Status,
		sub.GracePeriodEnds, sub.CurrentPeriodStart, sub.CurrentPeriodEnd, sub.CreatedAt)
	return err
}

func (r *Repository) GetSubscription(ctx context.Context, tenantID string) (*model.Subscription, error) {
	sub := &model.Subscription{}
	err := r.pool.QueryRow(ctx, `
		SELECT tenant_id, plan_id, stripe_customer_id, stripe_subscription_id, status,
			grace_period_ends, current_period_start, current_period_end, created_at
		FROM subscriptions WHERE tenant_id = $1`, tenantID).
		Scan(&sub.TenantID, &sub.PlanID, &sub.StripeCustomerID, &sub.StripeSubID, &sub.Status,
			&sub.GracePeriodEnds, &sub.CurrentPeriodStart, &sub.CurrentPeriodEnd, &sub.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return sub, err
}

func (r *Repository) GetSubscriptionByStripeID(ctx context.Context, stripeSubID string) (*model.Subscription, error) {
	sub := &model.Subscription{}
	err := r.pool.QueryRow(ctx, `
		SELECT tenant_id, plan_id, stripe_customer_id, stripe_subscription_id, status,
			grace_period_ends, current_period_start, current_period_end, created_at
		FROM subscriptions WHERE stripe_subscription_id = $1`, stripeSubID).
		Scan(&sub.TenantID, &sub.PlanID, &sub.StripeCustomerID, &sub.StripeSubID, &sub.Status,
			&sub.GracePeriodEnds, &sub.CurrentPeriodStart, &sub.CurrentPeriodEnd, &sub.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return sub, err
}

func (r *Repository) UpdateSubscriptionStatus(ctx context.Context, tenantID, status string, graceEnds *time.Time) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE subscriptions SET status = $1, grace_period_ends = $2 WHERE tenant_id = $3`,
		status, graceEnds, tenantID)
	return err
}

// ---- Usage ----------------------------------------------------------------

func (r *Repository) InsertUsage(ctx context.Context, u *model.UsageRecord) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO usage_records (tenant_id, period_start, period_end, storage_gb, ocr_pages, api_calls, active_users, ai_tokens)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (tenant_id, period_start) DO UPDATE SET
			storage_gb = EXCLUDED.storage_gb, ocr_pages = EXCLUDED.ocr_pages,
			api_calls = EXCLUDED.api_calls, active_users = EXCLUDED.active_users, ai_tokens = EXCLUDED.ai_tokens
	`, u.TenantID, u.PeriodStart, u.PeriodEnd, u.StorageGB, u.OCRPages, u.APICalls, u.ActiveUsers, u.AITokens)
	return err
}

// ---- Feature Flags --------------------------------------------------------

func (r *Repository) GetFeatureFlags(ctx context.Context, tenantID string) (*model.FeatureFlags, error) {
	var settings json.RawMessage
	err := r.pool.QueryRow(ctx, `SELECT COALESCE(settings, '{}') FROM organizations WHERE id = $1`, tenantID).Scan(&settings)
	if err != nil {
		return nil, err
	}
	var wrapper struct {
		Features model.FeatureFlags `json:"features"`
	}
	_ = json.Unmarshal(settings, &wrapper)
	return &wrapper.Features, nil
}

func (r *Repository) UpdateFeatureFlags(ctx context.Context, tenantID string, flags *model.FeatureFlags) error {
	flagsJSON, _ := json.Marshal(flags)
	_, err := r.pool.Exec(ctx, `
		UPDATE organizations SET settings = jsonb_set(COALESCE(settings, '{}'), '{features}', $1::jsonb)
		WHERE id = $2`, flagsJSON, tenantID)
	return err
}

// ---- Metering queries -----------------------------------------------------

func (r *Repository) MeterStorage(ctx context.Context, tenantID string) (float64, error) {
	var bytes int64
	err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(size_bytes), 0) FROM content_blobs WHERE tenant_id = $1`, tenantID).Scan(&bytes)
	return float64(bytes) / (1024 * 1024 * 1024), err
}

func (r *Repository) MeterOCRPages(ctx context.Context, tenantID string, since time.Time) (int64, error) {
	var count int64
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM ocr_results WHERE tenant_id = $1 AND created_at >= $2`, tenantID, since).Scan(&count)
	return count, err
}

func (r *Repository) MeterActiveUsers(ctx context.Context, tenantID string, since time.Time) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(DISTINCT user_id) FROM sessions WHERE tenant_id = $1 AND last_active_at >= $2`, tenantID, since).Scan(&count)
	return count, err
}

func (r *Repository) ListAllTenants(ctx context.Context) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT id FROM organizations ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
