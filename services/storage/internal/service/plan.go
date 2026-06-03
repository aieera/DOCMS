package service

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/aieera/sedoc/pkg/database"
)

// Plan identifiers persisted on organizations.plan.
const (
	planStandard   = "standard"
	planEnterprise = "enterprise"
	planDedicated  = "dedicated"

	planCachePrefix = "tenant_plan:"
	planCacheTTL    = 5 * time.Minute
)

// planSizeLimits maps plan → single-PUT upload ceiling. Unknown plans fall
// back to PlanLookup.fallback.
var planSizeLimits = map[string]int64{
	planStandard:   5 * 1024 * 1024 * 1024,
	planEnterprise: 50 * 1024 * 1024 * 1024,
	planDedicated:  100 * 1024 * 1024 * 1024,
}

// PlanLookup resolves a tenant's plan and the upload ceiling it implies.
// Reads organizations.plan through RLS (requires tenant tx) and caches the
// result in Redis for 5 minutes.
type PlanLookup struct {
	pool     *pgxpool.Pool
	redis    *redis.Client
	fallback int64
}

// NewPlanLookup constructs a PlanLookup. When redis is nil, cache is
// disabled and every call hits Postgres.
func NewPlanLookup(pool *pgxpool.Pool, rdb *redis.Client, fallback int64) *PlanLookup {
	return &PlanLookup{pool: pool, redis: rdb, fallback: fallback}
}

// MaxUploadSize returns the single-PUT ceiling for the tenant.
func (p *PlanLookup) MaxUploadSize(ctx context.Context, tenantID uuid.UUID) int64 {
	plan := p.loadPlan(ctx, tenantID)
	if limit, ok := planSizeLimits[plan]; ok {
		return limit
	}
	return p.fallback
}

func (p *PlanLookup) loadPlan(ctx context.Context, tenantID uuid.UUID) string {
	key := planCachePrefix + tenantID.String()
	if p.redis != nil {
		if cached, err := p.redis.Get(ctx, key).Result(); err == nil && cached != "" {
			return cached
		}
	}
	var plan string
	err := database.WithTenantTx(ctx, p.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT plan FROM organizations WHERE id = $1`, tenantID).Scan(&plan)
	})
	if err != nil || plan == "" {
		return planStandard
	}
	if _, ok := planSizeLimits[plan]; !ok {
		plan = planStandard
	}
	if p.redis != nil {
		_ = p.redis.Set(ctx, key, plan, planCacheTTL).Err()
	}
	return plan
}
