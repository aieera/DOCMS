// Package service orchestrates billing operations.
package service

import (
	"context"

	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/services/billing/internal/flags"
	"github.com/aieera/sedoc/services/billing/internal/model"
	"github.com/aieera/sedoc/services/billing/internal/provisioner"
	"github.com/aieera/sedoc/services/billing/internal/repository"
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
