// Package service orchestrates billing operations.
package service

import (
	"context"

	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/services/billing/internal/flags"
	"github.com/vaultdms/vaultdms/services/billing/internal/model"
	"github.com/vaultdms/vaultdms/services/billing/internal/provisioner"
	"github.com/vaultdms/vaultdms/services/billing/internal/repository"
)

// Service is the billing facade.
type Service struct {
	repo        *repository.Repository
	provisioner *provisioner.Provisioner
	flags       *flags.Checker
	log         zerolog.Logger
}

// Config is DI.
type Config struct {
	Repo        *repository.Repository
	Provisioner *provisioner.Provisioner
	Flags       *flags.Checker
	Logger      zerolog.Logger
}

// New creates a Service.
func New(cfg Config) *Service {
	return &Service{repo: cfg.Repo, provisioner: cfg.Provisioner, flags: cfg.Flags, log: cfg.Logger}
}

// Provision creates a new tenant.
func (s *Service) Provision(ctx context.Context, req model.ProvisionRequest) (*model.ProvisionResult, error) {
	return s.provisioner.Provision(ctx, req)
}

// GetSubscription returns the current subscription.
func (s *Service) GetSubscription(ctx context.Context, tenantID string) (*model.Subscription, error) {
	return s.repo.GetSubscription(ctx, tenantID)
}

// GetFeatureFlags returns flags for a tenant.
func (s *Service) GetFeatureFlags(ctx context.Context, tenantID string) (*model.FeatureFlags, error) {
	return s.flags.Get(ctx, tenantID)
}

// UpdateFeatureFlags overrides feature flags.
func (s *Service) UpdateFeatureFlags(ctx context.Context, tenantID string, f *model.FeatureFlags) error {
	if err := s.repo.UpdateFeatureFlags(ctx, tenantID, f); err != nil {
		return err
	}
	s.flags.Invalidate(ctx, tenantID)
	return nil
}

// IsFeatureEnabled checks a single flag (for middleware).
func (s *Service) IsFeatureEnabled(ctx context.Context, tenantID, flag string) (bool, error) {
	return s.flags.IsEnabled(ctx, tenantID, flag)
}

// GetPlan returns plan details.
func (s *Service) GetPlan(planID string) *model.Plan {
	p, ok := model.DefaultPlans[planID]
	if !ok {
		return nil
	}
	return &p
}

// ---- Tenant lifecycle (Wave 20) -------------------------------------------

// disposalGraceDays is how long a soft-deleted tenant lingers before
// the nightly sweep promotes it to hard-dispose. 30 days per brief;
// matches most SaaS compliance undelete windows.
const disposalGraceDays = 30

// ListTenants returns every organization + its lifecycle state for
// the admin Tenants page.
func (s *Service) ListTenants(ctx context.Context) ([]map[string]any, error) {
	return s.repo.ListOrgs(ctx)
}

// Deprovision marks a tenant soft-deleted and schedules hard-dispose
// after the grace window. Idempotent.
func (s *Service) Deprovision(ctx context.Context, tenantID string) error {
	return s.repo.SoftDeleteOrg(ctx, tenantID, disposalGraceDays)
}

// UndoDeprovision reverses Deprovision iff hard-dispose hasn't run.
func (s *Service) UndoDeprovision(ctx context.Context, tenantID string) error {
	return s.repo.UndoSoftDeleteOrg(ctx, tenantID)
}
