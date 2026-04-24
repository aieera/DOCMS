// Package repository manages billing tables (subscriptions, usage_records).
package repository

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vaultdms/vaultdms/pkg/database"
	"github.com/vaultdms/vaultdms/services/billing/internal/model"
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

// ---- Stripe idempotency ---------------------------------------------------

// InsertStripeEvent records a Stripe webhook event. Returns true when
// the row is new, false when it's a duplicate (idempotency skip).
// Writing the full payload lets ops replay the handler in a debugger
// without talking to Stripe again.
func (r *Repository) InsertStripeEvent(ctx context.Context, stripeEventID, eventType string, payload []byte) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO stripe_events (stripe_event_id, event_type, payload)
		VALUES ($1, $2, $3)
		ON CONFLICT (stripe_event_id) DO NOTHING
	`, stripeEventID, eventType, payload)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ListOrgs returns a thin projection of every organization row for
// the admin Tenants page. Returns soft-deleted rows too — the page
// badges them; the operator still needs to see them until they
// hard-dispose.
func (r *Repository) ListOrgs(ctx context.Context) ([]map[string]any, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id::text, name, slug, plan, primary_region,
		       created_at, deleted_at, dispose_scheduled_at, disposed_at
		  FROM organizations
		 ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, name, slug, plan, region string
		var created time.Time
		var deleted, scheduled, disposed *time.Time
		if err := rows.Scan(&id, &name, &slug, &plan, &region, &created, &deleted, &scheduled, &disposed); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"id":                   id,
			"name":                 name,
			"slug":                 slug,
			"plan":                 plan,
			"primary_region":       region,
			"created_at":           created,
			"deleted_at":           deleted,
			"dispose_scheduled_at": scheduled,
			"disposed_at":          disposed,
		})
	}
	return out, rows.Err()
}

// ---- Tenant lifecycle -----------------------------------------------------

// SoftDeleteOrg marks a tenant deleted and schedules hard-dispose.
// `graceDays` is the dual-validity window before crypto-shred. Idempotent:
// re-calling against an already-soft-deleted tenant leaves the existing
// schedule in place.
func (r *Repository) SoftDeleteOrg(ctx context.Context, tenantID string, graceDays int) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE organizations
		   SET deleted_at           = COALESCE(deleted_at, now()),
		       dispose_scheduled_at = COALESCE(dispose_scheduled_at, now() + make_interval(days => $2))
		 WHERE id = $1::uuid
	`, tenantID, graceDays)
	return err
}

// UndoSoftDeleteOrg reverses SoftDeleteOrg as long as hard-dispose
// hasn't fired. Returns ErrNoRows-equivalent when nothing to undo.
func (r *Repository) UndoSoftDeleteOrg(ctx context.Context, tenantID string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE organizations
		   SET deleted_at = NULL, dispose_scheduled_at = NULL
		 WHERE id = $1::uuid AND disposed_at IS NULL
	`, tenantID)
	return err
}

// DueForDispose returns tenant ids whose grace period elapsed and
// that haven't yet been hard-disposed. The `limit` guard keeps the
// nightly sweep bounded; the idx_organizations_dispose_due index
// makes this O(log n) in due rows only.
func (r *Repository) DueForDispose(ctx context.Context, limit int) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id::text FROM organizations
		 WHERE disposed_at IS NULL
		   AND dispose_scheduled_at IS NOT NULL
		   AND dispose_scheduled_at <= now()
		 ORDER BY dispose_scheduled_at
		 LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// MarkDisposed sets disposed_at. Expected to be called at the end of
// a hard-dispose workflow after the CMK shred + data-purge activities
// have all succeeded.
func (r *Repository) MarkDisposed(ctx context.Context, tenantID string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE organizations SET disposed_at = now()
		 WHERE id = $1::uuid AND disposed_at IS NULL
	`, tenantID)
	return err
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

// MarkUsageReported stamps reported_at = now() for the given
// (tenant_id, period_start). Called after Stripe Meter push succeeds
// so the next sweep skips already-reported rows.
func (r *Repository) MarkUsageReported(ctx context.Context, tenantID string, periodStart time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE usage_records SET reported_at = now()
		 WHERE tenant_id = $1 AND period_start = $2
	`, tenantID, periodStart)
	return err
}

// StripeCustomerIDFor returns the Stripe customer id for a tenant
// from the subscriptions row, or "" if not yet linked. Used by the
// metering cron to feed the Stripe Meter reporter.
func (r *Repository) StripeCustomerIDFor(ctx context.Context, tenantID string) (string, error) {
	var s *string
	err := r.pool.QueryRow(ctx,
		`SELECT stripe_customer_id FROM subscriptions WHERE tenant_id = $1`, tenantID).Scan(&s)
	if err == pgx.ErrNoRows || s == nil {
		return "", nil
	}
	return *s, err
}

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
//
// All three target tables (content_blobs, ocr_results, sessions) have
// RLS USING `tenant_id = current_setting('app.current_tenant', true)::uuid`.
// Direct pool queries without that GUC return ZERO rows — which is
// what made every billing dashboard show 0 before this fix. Each
// metering query now wraps in `WithTenantTx` so RLS admits the row
// set. The WHERE tenant_id=$1 also kept as defense-in-depth.

func (r *Repository) MeterStorage(ctx context.Context, tenantID string) (float64, error) {
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return 0, err
	}
	var bytes int64
	err = database.WithTenantTx(ctx, r.pool, tenantUUID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT COALESCE(SUM(size_bytes), 0) FROM content_blobs WHERE tenant_id = $1`, tenantID).Scan(&bytes)
	})
	return float64(bytes) / (1024 * 1024 * 1024), err
}

func (r *Repository) MeterOCRPages(ctx context.Context, tenantID string, since time.Time) (int64, error) {
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return 0, err
	}
	var count int64
	err = database.WithTenantTx(ctx, r.pool, tenantUUID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM ocr_results WHERE tenant_id = $1 AND created_at >= $2`, tenantID, since).Scan(&count)
	})
	return count, err
}

func (r *Repository) MeterActiveUsers(ctx context.Context, tenantID string, since time.Time) (int, error) {
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return 0, err
	}
	var count int
	err = database.WithTenantTx(ctx, r.pool, tenantUUID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT COUNT(DISTINCT user_id) FROM sessions WHERE tenant_id = $1 AND last_active_at >= $2`, tenantID, since).Scan(&count)
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
