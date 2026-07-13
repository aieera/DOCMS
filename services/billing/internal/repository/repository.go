// Package repository manages billing tables (subscriptions, usage_records).
//
// RLS contract (Wave A.1.c, issue #72): subscriptions and usage_records
// are FORCE ROW LEVEL SECURITY, and the meter reads touch document-owned
// FORCE-RLS tables (content_blobs, ocr_results, sessions). Every
// tenant-scoped method therefore runs inside database.WithTenantTx via
// withTenant — under the prod NOBYPASSRLS role a raw-pool query fails
// closed (rejected writes / 0-row reads), which is how billing was dead
// in prod. Deliberate raw-pool exceptions, each on the tenant REGISTRY
// (organizations carries no RLS): CreateOrg/GetOrg/SuspendOrg/
// GetFeatureFlags/UpdateFeatureFlags and the cron's ListAllTenants
// enumeration anchor. GetSubscriptionByStripeID is a documented
// pre-tenant exception (see its comment).
package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/services/billing/internal/model"
)

type Repository struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// withTenant opens a tenant-scoped transaction (SET LOCAL
// app.current_tenant) and runs fn inside it — the A.1.a template.
func (r *Repository) withTenant(ctx context.Context, tenantID string, fn func(tx pgx.Tx) error) error {
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		return fmt.Errorf("tenant_id: %w", err)
	}
	return database.WithTenantTx(ctx, r.pool, tid, fn)
}

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
	return r.withTenant(ctx, sub.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
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
	})
}

func (r *Repository) GetSubscription(ctx context.Context, tenantID string) (*model.Subscription, error) {
	var out *model.Subscription
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		sub := &model.Subscription{}
		err := tx.QueryRow(ctx, `
			SELECT tenant_id, plan_id, stripe_customer_id, stripe_subscription_id, status,
				grace_period_ends, current_period_start, current_period_end, created_at
			FROM subscriptions WHERE tenant_id = $1`, tenantID).
			Scan(&sub.TenantID, &sub.PlanID, &sub.StripeCustomerID, &sub.StripeSubID, &sub.Status,
				&sub.GracePeriodEnds, &sub.CurrentPeriodStart, &sub.CurrentPeriodEnd, &sub.CreatedAt)
		if err == pgx.ErrNoRows {
			return nil // preserve the (nil, nil) not-found contract
		}
		if err != nil {
			return err
		}
		out = sub
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetSubscriptionByStripeID is a PRE-TENANT lookup (the Stripe webhook
// derives the tenant FROM this row) and stays on the raw pool as a
// documented exception: under prod NOBYPASSRLS it fails closed, same
// class as auth's GetByHash. It is currently dead in practice anyway —
// stripe_subscription_id is never persisted (Wave D.1). The D.1 design
// must add a non-RLS stripe→tenant mapping (platform_admins pattern)
// rather than trying to read a FORCE-RLS table before the tenant is
// known. Tracked on issue #72.
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
	return r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE subscriptions SET status = $1, grace_period_ends = $2 WHERE tenant_id = $3`,
			status, graceEnds, tenantID)
		return err
	})
}

// ---- Stripe → tenant mapping (non-RLS, pre-tenant lookup) ------------------
//
// stripe_customer_map is platform metadata (no RLS), so these run on the
// raw pool and WORK under prod NOBYPASSRLS — unlike a read of the
// FORCE-RLS subscriptions table before the tenant is known. The webhook
// resolves the tenant here, then does the tenant-scoped subscription
// write via withTenant.

// UpsertCustomerMap records the Stripe customer → tenant mapping (and the
// subscription id when known). Written at checkout.session.completed and
// at provision time. Idempotent on stripe_customer_id.
func (r *Repository) UpsertCustomerMap(ctx context.Context, stripeCustomerID, stripeSubID, tenantID string) error {
	if stripeCustomerID == "" || tenantID == "" {
		return fmt.Errorf("customer map: stripe_customer_id and tenant_id required")
	}
	var subArg any
	if stripeSubID != "" {
		subArg = stripeSubID
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO stripe_customer_map (stripe_customer_id, stripe_subscription_id, tenant_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (stripe_customer_id) DO UPDATE SET
			-- keep an existing subscription id if this event didn't carry one
			stripe_subscription_id = COALESCE(EXCLUDED.stripe_subscription_id, stripe_customer_map.stripe_subscription_id),
			tenant_id  = EXCLUDED.tenant_id,
			updated_at = now()`,
		stripeCustomerID, subArg, tenantID)
	return err
}

// TenantByStripeCustomer resolves the tenant (+ subscription id) for a
// Stripe customer. found=false when unmapped.
func (r *Repository) TenantByStripeCustomer(ctx context.Context, stripeCustomerID string) (tenantID, subID string, found bool, err error) {
	var sub *string
	err = r.pool.QueryRow(ctx,
		`SELECT tenant_id::text, stripe_subscription_id FROM stripe_customer_map WHERE stripe_customer_id = $1`,
		stripeCustomerID).Scan(&tenantID, &sub)
	if err == pgx.ErrNoRows {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	if sub != nil {
		subID = *sub
	}
	return tenantID, subID, true, nil
}

// TenantByStripeSubscription resolves the tenant for a Stripe
// subscription id (subscription.*/invoice.* events carry the sub, not the
// customer). found=false when unmapped.
func (r *Repository) TenantByStripeSubscription(ctx context.Context, stripeSubID string) (tenantID string, found bool, err error) {
	err = r.pool.QueryRow(ctx,
		`SELECT tenant_id::text FROM stripe_customer_map WHERE stripe_subscription_id = $1`,
		stripeSubID).Scan(&tenantID)
	if err == pgx.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return tenantID, true, nil
}

// MarkStripeEventProcessed records a fully-processed Stripe event id.
// Returns firstTime=false when the id was already recorded (a redelivery)
// so the caller can no-op. Non-RLS.
func (r *Repository) StripeEventAlreadyProcessed(ctx context.Context, eventID string) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM stripe_processed_events WHERE event_id = $1)`, eventID).Scan(&exists)
	return exists, err
}

// MarkStripeEventProcessed records the event id after successful
// processing. Idempotent (ON CONFLICT DO NOTHING).
func (r *Repository) MarkStripeEventProcessed(ctx context.Context, eventID, eventType string) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO stripe_processed_events (event_id, event_type) VALUES ($1, $2)
		 ON CONFLICT (event_id) DO NOTHING`, eventID, eventType)
	return err
}

// ---- Usage ----------------------------------------------------------------

func (r *Repository) InsertUsage(ctx context.Context, u *model.UsageRecord) error {
	return r.withTenant(ctx, u.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
		INSERT INTO usage_records (tenant_id, period_start, period_end, storage_gb, ocr_pages, api_calls, active_users, ai_tokens)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (tenant_id, period_start) DO UPDATE SET
			storage_gb = EXCLUDED.storage_gb, ocr_pages = EXCLUDED.ocr_pages,
			api_calls = EXCLUDED.api_calls, active_users = EXCLUDED.active_users, ai_tokens = EXCLUDED.ai_tokens
	`, u.TenantID, u.PeriodStart, u.PeriodEnd, u.StorageGB, u.OCRPages, u.APICalls, u.ActiveUsers, u.AITokens)
		return err
	})
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
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT COALESCE(SUM(size_bytes), 0) FROM content_blobs WHERE tenant_id = $1`, tenantID).Scan(&bytes)
	})
	return float64(bytes) / (1024 * 1024 * 1024), err
}

func (r *Repository) MeterOCRPages(ctx context.Context, tenantID string, since time.Time) (int64, error) {
	var count int64
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM ocr_results WHERE tenant_id = $1 AND created_at >= $2`, tenantID, since).Scan(&count)
	})
	return count, err
}

func (r *Repository) MeterActiveUsers(ctx context.Context, tenantID string, since time.Time) (int, error) {
	var count int
	// Column is last_activity_at (document 000001) — the previous
	// spelling errored on every cron cycle and the error was silently
	// discarded, so active_users metered 0 forever.
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT COUNT(DISTINCT user_id) FROM sessions WHERE tenant_id = $1 AND last_activity_at >= $2`, tenantID, since).Scan(&count)
	})
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
