// Package flags provides per-tenant feature flag checking with Redis cache.
package flags

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/vaultdms/vaultdms/services/billing/internal/model"
	"github.com/vaultdms/vaultdms/services/billing/internal/repository"
)

const (
	cachePrefix = "feature_flags:"
	cacheTTL    = 5 * time.Minute
)

// Checker resolves feature flags from Redis cache (fast) → DB (fallback).
type Checker struct {
	repo *repository.Repository
	rdb  *redis.Client
}

// NewChecker creates a flag Checker.
func NewChecker(repo *repository.Repository, rdb *redis.Client) *Checker {
	return &Checker{repo: repo, rdb: rdb}
}

// Get returns the feature flags for a tenant.
func (c *Checker) Get(ctx context.Context, tenantID string) (*model.FeatureFlags, error) {
	// Try cache first.
	raw, err := c.rdb.Get(ctx, cachePrefix+tenantID).Bytes()
	if err == nil {
		var flags model.FeatureFlags
		if json.Unmarshal(raw, &flags) == nil {
			return &flags, nil
		}
	}
	// Fall back to DB.
	flags, err := c.repo.GetFeatureFlags(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	// Cache.
	if b, err := json.Marshal(flags); err == nil {
		_ = c.rdb.Set(ctx, cachePrefix+tenantID, b, cacheTTL).Err()
	}
	return flags, nil
}

// IsEnabled checks a single flag.
func (c *Checker) IsEnabled(ctx context.Context, tenantID, flag string) (bool, error) {
	flags, err := c.Get(ctx, tenantID)
	if err != nil {
		return false, err
	}
	switch flag {
	case "ai_enabled":
		return flags.AIEnabled, nil
	case "advanced_workflow":
		return flags.AdvancedWorkflow, nil
	case "sso_enabled":
		return flags.SSOEnabled, nil
	case "e_signatures":
		return flags.ESignatures, nil
	case "custom_branding":
		return flags.CustomBranding, nil
	case "api_access":
		return flags.APIAccess, nil
	case "data_rooms":
		return flags.DataRooms, nil
	default:
		return false, nil
	}
}

// Invalidate clears the cache for a tenant (call after updating flags).
func (c *Checker) Invalidate(ctx context.Context, tenantID string) {
	_ = c.rdb.Del(ctx, cachePrefix+tenantID).Err()
}
