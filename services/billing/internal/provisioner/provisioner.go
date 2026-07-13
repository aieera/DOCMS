// Package provisioner handles multi-step tenant provisioning.
package provisioner

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/tenant"
	"github.com/aieera/sedoc/services/billing/internal/model"
	"github.com/aieera/sedoc/services/billing/internal/repository"
)

// Provisioner orchestrates tenant creation.
//
// ADR 0110 — `clusterRegion` is the SEDOC_REGION_ID of the
// billing service's cluster. Provision() refuses to create a tenant
// whose requested data_residency_region doesn't match — a UAE
// tenant must be provisioned by the UAE cluster's billing service,
// not by the US one. This catches Stripe webhook routing bugs
// before the org row lands in the wrong region's database.
type Provisioner struct {
	repo          *repository.Repository
	pool          *pgxpool.Pool
	router        *tenant.Router
	log           zerolog.Logger
	clusterRegion string
}

// New creates a Provisioner.
//
// Deprecated: prefer NewWithRegion so residency mismatches fail
// fast at Provision() time. Kept for the two existing callers
// (billing test harness, dev seeder) that don't care about region.
func New(repo *repository.Repository, pool *pgxpool.Pool, rdb *redis.Client, log zerolog.Logger) *Provisioner {
	return &Provisioner{repo: repo, pool: pool, router: tenant.NewRouter(rdb), log: log}
}

// NewWithRegion is the production constructor since ADR 0110.
// `clusterRegion` is the running cluster's SEDOC_REGION_ID;
// `req.Region` is checked against it before the tenant is created.
func NewWithRegion(clusterRegion string, repo *repository.Repository, pool *pgxpool.Pool, rdb *redis.Client, log zerolog.Logger) *Provisioner {
	return &Provisioner{
		repo:          repo,
		pool:          pool,
		router:        tenant.NewRouter(rdb),
		log:           log,
		clusterRegion: clusterRegion,
	}
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

	// ADR 0110 — refuse to provision a tenant whose residency
	// doesn't match the cluster running this billing service. The
	// clusterRegion is empty when the deprecated New() constructor
	// is used (dev seeds, tests); we skip the check there so the
	// test harness doesn't grow a residency mock.
	if p.clusterRegion != "" {
		want := strings.ToLower(strings.TrimSpace(p.clusterRegion))
		got := strings.ToLower(strings.TrimSpace(req.Region))
		if got == "" {
			return nil, fmt.Errorf("provision: data_residency_region required (cluster=%s)", want)
		}
		if got != want {
			return nil, fmt.Errorf(
				"provision: residency mismatch — tenant requested %q but cluster serves %q. "+
					"Route this checkout to the correct region's billing service.",
				got, want)
		}
	}

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

	// 4. Create default subscription — carrying the Stripe ids when this
	// provision was driven by a completed checkout, so who-is-subscribed
	// is recorded at provision time (not silently dropped).
	now := time.Now().UTC()
	sub := &model.Subscription{
		TenantID:           tenantID,
		PlanID:             req.Plan,
		StripeCustomerID:   req.StripeCustomerID,
		StripeSubID:        req.StripeSubID,
		Status:             "active",
		CurrentPeriodStart: now,
		CurrentPeriodEnd:   now.AddDate(0, 1, 0),
		CreatedAt:          now,
	}
	if err := p.repo.UpsertSubscription(ctx, sub); err != nil {
		p.log.Warn().Err(err).Msg("create subscription")
	}
	// Write the Stripe → tenant mapping so webhooks can resolve this
	// tenant (the pre-tenant lookup anchor). Hard failure: without it the
	// money-path webhooks are dead for this tenant.
	if req.StripeCustomerID != "" {
		if err := p.repo.UpsertCustomerMap(ctx, req.StripeCustomerID, req.StripeSubID, tenantID); err != nil {
			return nil, fmt.Errorf("persist stripe customer map: %w", err)
		}
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
	// users is FORCE RLS — the insert must run under the new tenant's
	// context or it is rejected in prod (Wave A.1.c, issue #72).
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		return "", fmt.Errorf("tenant_id: %w", err)
	}
	var userID string
	err = database.WithTenantTx(ctx, p.pool, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO users (id, tenant_id, email, password_hash, display_name, role, status, created_at)
			VALUES (gen_random_uuid(), $1, $2, '', 'Admin', 'org_admin', 'active', now())
			RETURNING id
		`, tenantID, email).Scan(&userID)
	})
	return userID, err
}
