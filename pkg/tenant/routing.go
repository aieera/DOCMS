// Package tenant provides tenant routing via Redis. Every service reads the
// routing record on each request to determine the tenant's database, search
// cluster, vector collection, and storage region.
//
// Key format: tenant_route:{tenant_id}
// Value: JSON TenantRoute
package tenant

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	routePrefix = "tenant_route:"
	routeTTL    = 5 * time.Minute
)

// TenantRoute describes where a tenant's data lives.
type TenantRoute struct {
	TenantID         string `json:"tenant_id"`
	DBHost           string `json:"db_host"`
	DBSchema         string `json:"db_schema"`
	SearchCluster    string `json:"search_cluster"`
	VectorCollection string `json:"vector_collection"`
	StorageRegion    string `json:"storage_region"`
	Plan             string `json:"plan"`
	Suspended        bool   `json:"suspended"`
}

// Router reads and writes tenant routing records.
type Router struct {
	rdb *redis.Client
}

// NewRouter creates a Router.
func NewRouter(rdb *redis.Client) *Router {
	return &Router{rdb: rdb}
}

// Get returns the routing record for a tenant. Returns nil if not cached
// (caller should fall back to DB lookup and call Set).
func (r *Router) Get(ctx context.Context, tenantID string) (*TenantRoute, error) {
	raw, err := r.rdb.Get(ctx, routePrefix+tenantID).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("redis get tenant route: %w", err)
	}
	var route TenantRoute
	if err := json.Unmarshal(raw, &route); err != nil {
		return nil, err
	}
	return &route, nil
}

// Set caches a tenant routing record.
func (r *Router) Set(ctx context.Context, route *TenantRoute) error {
	raw, err := json.Marshal(route)
	if err != nil {
		return err
	}
	return r.rdb.Set(ctx, routePrefix+route.TenantID, raw, routeTTL).Err()
}

// Delete removes a tenant routing record.
func (r *Router) Delete(ctx context.Context, tenantID string) error {
	return r.rdb.Del(ctx, routePrefix+tenantID).Err()
}

// IsSuspended checks if a tenant is suspended (fast path for middleware).
func (r *Router) IsSuspended(ctx context.Context, tenantID string) (bool, error) {
	route, err := r.Get(ctx, tenantID)
	if err != nil || route == nil {
		return false, err
	}
	return route.Suspended, nil
}

// Suspend marks a tenant as suspended in the routing cache.
func (r *Router) Suspend(ctx context.Context, tenantID string) error {
	route, err := r.Get(ctx, tenantID)
	if err != nil {
		return err
	}
	if route == nil {
		route = &TenantRoute{TenantID: tenantID}
	}
	route.Suspended = true
	return r.Set(ctx, route)
}

// Unsuspend marks a tenant as active.
func (r *Router) Unsuspend(ctx context.Context, tenantID string) error {
	route, err := r.Get(ctx, tenantID)
	if err != nil || route == nil {
		return err
	}
	route.Suspended = false
	return r.Set(ctx, route)
}
