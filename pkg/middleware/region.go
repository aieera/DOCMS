package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/auth"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
)

// RegionEnforcer validates that a resource's region pin is compatible with
// the storage bucket / search index / cache keyspace actually being
// addressed. The naming convention is "<product>-<region>-<tier>", e.g.
// "dms-us-east-1-hot". If any component's region disagrees with pin, the
// call is rejected with ErrRegionViolation.
type RegionEnforcer struct {
	defaultRegion string
}

// NewRegionEnforcer constructs an enforcer scoped to a default region. The
// default is used when an operation doesn't specify a region pin explicitly.
func NewRegionEnforcer(defaultRegion string) *RegionEnforcer {
	return &RegionEnforcer{defaultRegion: defaultRegion}
}

// CheckBucket validates that bucketName belongs to the pinned region.
func (r *RegionEnforcer) CheckBucket(_ context.Context, bucketName, pin string) error {
	if pin == "" {
		pin = r.defaultRegion
	}
	region := regionFromBucket(bucketName)
	if region == "" {
		return fmt.Errorf("cannot determine region from bucket %q: %w", bucketName, vdmserr.ErrRegionViolation)
	}
	if region != pin {
		return fmt.Errorf("bucket %s is in %s but tenant pin is %s: %w",
			bucketName, region, pin, vdmserr.ErrRegionViolation)
	}
	return nil
}

// CheckIndex validates that an OpenSearch / Qdrant collection name is in pin.
// Index naming follows "vaultdms-<region>-<logical>".
func (r *RegionEnforcer) CheckIndex(_ context.Context, indexName, pin string) error {
	if pin == "" {
		pin = r.defaultRegion
	}
	region := regionFromIndex(indexName)
	if region == "" {
		return fmt.Errorf("cannot determine region from index %q: %w", indexName, vdmserr.ErrRegionViolation)
	}
	if region != pin {
		return fmt.Errorf("index %s is in %s but tenant pin is %s: %w",
			indexName, region, pin, vdmserr.ErrRegionViolation)
	}
	return nil
}

// regionFromBucket extracts "us-east-1" from "dms-us-east-1-hot".
func regionFromBucket(name string) string {
	// drop product prefix
	name = strings.TrimPrefix(name, "dms-")
	// drop tier suffix (last segment)
	i := strings.LastIndex(name, "-")
	if i < 0 {
		return ""
	}
	return name[:i]
}

// regionFromIndex extracts "us-east-1" from "vaultdms-us-east-1-documents".
func regionFromIndex(name string) string {
	parts := strings.SplitN(strings.TrimPrefix(name, "vaultdms-"), "-", 4)
	if len(parts) < 4 {
		return ""
	}
	return strings.Join(parts[:3], "-")
}

// =====================================================================
// HTTP-layer residency enforcement (ADR 0110 — UAE deployment).
// =====================================================================
//
// The CheckBucket / CheckIndex helpers above are data-layer checks
// invoked by callers that already hold a region pin. EnforceRegion
// is the complementary HTTP middleware — it rejects entire requests
// whose tenant's primary_region doesn't match the cluster's
// configured VAULTDMS_REGION_ID before the request reaches a
// handler. Both layers exist because:
//
//   1. Data-layer alone can't stop a tenant request from hitting
//      the wrong region's binary in the first place — a misrouted
//      load balancer or kubectl-context-mixup lands the call at
//      our front door.
//   2. HTTP-layer alone can't stop a same-region request from
//      reaching a foreign bucket if a handler is buggy.
//
// We respond with 451 Unavailable For Legal Reasons + a small
// X-DMS-Region-Block header so the client can redirect to the
// correct cluster without us leaking the full topology.

// RegionResolver looks up the tenant's `primary_region`. An interface
// so tests stub it without standing up Postgres.
type RegionResolver interface {
	Resolve(ctx context.Context, tenantID uuid.UUID) (string, error)
}

// PgxRegionResolver reads primary_region from the organizations
// table via the shared pgx pool. The result is cached in-process
// per tenant for the lifetime of the binary — primary_region is
// immutable after first upload (ADR 0007), so the cache never goes
// stale within a process. A tenant boundary-change (cross-region
// migration) requires a full restart, which matches the operational
// model already documented in docs/runbooks/residency-migration.md.
type PgxRegionResolver struct {
	pool  *pgxpool.Pool
	mu    sync.RWMutex
	cache map[uuid.UUID]string
}

// NewPgxRegionResolver constructs a resolver backed by the given pool.
func NewPgxRegionResolver(pool *pgxpool.Pool) *PgxRegionResolver {
	return &PgxRegionResolver{pool: pool, cache: make(map[uuid.UUID]string)}
}

// ErrRegionUnknown is returned when the resolver cannot find the
// tenant. Callers translate this to 404 — NOT 451 — an unknown
// tenant is not a residency violation.
var ErrRegionUnknown = errors.New("region: tenant not found")

// Resolve returns the tenant's primary_region, lowercased + trimmed.
func (r *PgxRegionResolver) Resolve(ctx context.Context, tenantID uuid.UUID) (string, error) {
	r.mu.RLock()
	if v, ok := r.cache[tenantID]; ok {
		r.mu.RUnlock()
		return v, nil
	}
	r.mu.RUnlock()

	var region string
	err := r.pool.QueryRow(ctx,
		`SELECT primary_region FROM organizations WHERE id = $1 AND deleted_at IS NULL`,
		tenantID).Scan(&region)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrRegionUnknown
		}
		return "", err
	}
	region = strings.ToLower(strings.TrimSpace(region))

	r.mu.Lock()
	r.cache[tenantID] = region
	r.mu.Unlock()
	return region, nil
}

// EnforceRegion returns an http.Handler middleware that rejects any
// request whose tenant.primary_region doesn't match clusterRegion.
// Wires after the auth middleware so the tenant ID is already on
// the request context.
//
// 451 is the correct status code: the request is well-formed but
// cannot be served from this region by policy.
func EnforceRegion(clusterRegion string, resolver RegionResolver, log zerolog.Logger) func(http.Handler) http.Handler {
	cluster := strings.ToLower(strings.TrimSpace(clusterRegion))
	if cluster == "" {
		// Fail-closed. A service that boots with no region MUST NOT
		// serve traffic — that's the worst-case residency mistake.
		panic("middleware.EnforceRegion: cluster region is empty; set VAULTDMS_REGION_ID")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tid, err := auth.GetTenantID(r.Context())
			if err != nil || tid == uuid.Nil {
				// No tenant on the request: either the route is
				// tenant-agnostic (e.g. /auth/login pre-tenant) or
				// the auth middleware will reject it before us.
				// Either way, no residency check applies.
				next.ServeHTTP(w, r)
				return
			}
			tenantRegion, err := resolver.Resolve(r.Context(), tid)
			if err != nil {
				if errors.Is(err, ErrRegionUnknown) {
					// 404, not 451 — leaking that a tenant exists
					// across regions would be a worse compliance
					// outcome than a plain not-found.
					http.NotFound(w, r)
					return
				}
				log.Error().Err(err).Str("tenant_id", tid.String()).Msg("region resolve failed")
				http.Error(w, "region resolve failed", http.StatusInternalServerError)
				return
			}
			// Normalize the resolver's output too — PgxRegionResolver
			// already trims, but a buggy stub or future resolver
			// must not produce false positives via whitespace.
			tenantRegion = strings.ToLower(strings.TrimSpace(tenantRegion))
			if tenantRegion != cluster {
				log.Warn().
					Str("tenant_id", tid.String()).
					Str("tenant_region", tenantRegion).
					Str("cluster_region", cluster).
					Str("path", r.URL.Path).
					Msg("residency block: tenant served from wrong region")
				w.Header().Set("X-DMS-Region-Block",
					"tenant-region="+tenantRegion+";cluster-region="+cluster)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnavailableForLegalReasons) // 451
				_, _ = w.Write([]byte(`{"error":"residency_violation","tenant_region":"` +
					tenantRegion + `","cluster_region":"` + cluster + `"}`))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
