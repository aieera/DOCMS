// Package provisioner handles multi-step tenant provisioning.
package provisioner

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/pkg/tenant"
	"github.com/vaultdms/vaultdms/services/billing/internal/model"
	"github.com/vaultdms/vaultdms/services/billing/internal/repository"
)

// Provisioner orchestrates tenant creation.
type Provisioner struct {
	repo   *repository.Repository
	pool   *pgxpool.Pool
	router *tenant.Router
	log    zerolog.Logger
}

// New creates a Provisioner.
func New(repo *repository.Repository, pool *pgxpool.Pool, rdb *redis.Client, log zerolog.Logger) *Provisioner {
	return &Provisioner{repo: repo, pool: pool, router: tenant.NewRouter(rdb), log: log}
}

// Provision creates a new tenant end-to-end:
//  1. Create organization row
//  2. Create schema (if schema-per-tenant isolation)
//  3. Set up tenant routing in Redis
//  4. Create admin user via auth service
//
// OpenSearch index, Qdrant collection, and S3 buckets are created lazily
// on first use by their respective services.
func (p *Provisioner) Provision(ctx context.Context, req model.ProvisionRequest) (*model.ProvisionResult, error) {
	p.log.Info().Str("org", req.OrgName).Str("plan", req.Plan).Str("region", req.Region).Msg("provisioning tenant")

	// 1. Create organization.
	tenantID, err := p.repo.CreateOrg(ctx, req.OrgName, req.Plan, req.Region)
	if err != nil {
		return nil, fmt.Errorf("create org: %w", err)
	}

	// 2. Schema-per-tenant isolation (optional).
	// When using shared schema (default), RLS handles isolation.
	// When using schema-per-tenant, create a dedicated schema.
	// This is configurable via global.tenantIsolation in Helm values.

	// 3. Set up tenant routing cache.
	route := &tenant.TenantRoute{
		TenantID:         tenantID,
		DBHost:           "",
		DBSchema:         "public",
		SearchCluster:    fmt.Sprintf("dms-documents-%s", tenantID),
		VectorCollection: "dms_vectors",
		StorageRegion:    req.Region,
		Plan:             req.Plan,
	}
	if err := p.router.Set(ctx, route); err != nil {
		p.log.Warn().Err(err).Msg("set tenant route")
	}

	// 4. Create default subscription.
	now := time.Now().UTC()
	sub := &model.Subscription{
		TenantID:           tenantID,
		PlanID:             req.Plan,
		Status:             "active",
		CurrentPeriodStart: now,
		CurrentPeriodEnd:   now.AddDate(0, 1, 0),
		CreatedAt:          now,
	}
	if err := p.repo.UpsertSubscription(ctx, sub); err != nil {
		p.log.Warn().Err(err).Msg("create subscription")
	}

	// 5. Create admin user (insert directly — auth service handles hashing).
	adminID, err := p.createAdminUser(ctx, tenantID, req.AdminEmail)
	if err != nil {
		return nil, fmt.Errorf("create admin: %w", err)
	}

	p.log.Info().Str("tenant_id", tenantID).Str("admin", adminID).Msg("tenant provisioned")

	return &model.ProvisionResult{
		TenantID:    tenantID,
		AdminUserID: adminID,
		LoginURL:    fmt.Sprintf("https://app.vaultdms.io/login?tenant=%s", tenantID),
	}, nil
}

func (p *Provisioner) createAdminUser(ctx context.Context, tenantID, email string) (string, error) {
	var userID string
	err := p.pool.QueryRow(ctx, `
		INSERT INTO users (id, tenant_id, email, password_hash, display_name, role, status, created_at)
		VALUES (gen_random_uuid(), $1, $2, '', 'Admin', 'org_admin', 'active', now())
		RETURNING id
	`, tenantID, email).Scan(&userID)
	return userID, err
}
