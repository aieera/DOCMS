package regionenforcer

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// DocumentRegionLookup resolves a document id to its region_pin. The
// middleware doesn't reach into Postgres directly — services inject
// their own resolver so the middleware stays infrastructure-free.
type DocumentRegionLookup func(ctx context.Context, tenantID, documentID string) (string, error)

// MiddlewareOptions configures RegionEnforcerHTTP.
type MiddlewareOptions struct {
	Enforcer *Enforcer
	// Lookup returns the document's pinned region. Required when the
	// middleware is mounted on a path that has {document_id} in it.
	// Mux URL params are read via PathParam below.
	Lookup DocumentRegionLookup
	// PathParam reads the document id from the request (e.g. via
	// chi.URLParam or r.PathValue). Empty return ⇒ skip enforcement.
	PathParam func(*http.Request) string
	// TenantHeader names the request header that carries the caller's
	// tenant id. Defaults to X-Auth-Tenant-ID.
	TenantHeader string
	// Layer + EndpointHeader: when set, the middleware validates that
	// the request's chosen target endpoint (read from EndpointHeader,
	// e.g. "X-Force-Region" or "X-S3-Endpoint") matches the document's
	// region for the given layer. If EndpointHeader is empty, only the
	// region-itself check runs (caller declared a region, did it
	// match the doc's pin).
	Layer          Layer
	EndpointHeader string
}

// RegionEnforcerHTTP wraps write handlers and rejects with 451 when
// the request would cause cross-region data movement.
//
// Two checks, in order:
//
//  1. If `X-Force-Region` is present, compare it against the doc's
//     region_pin. Mismatch → 451 + REGION_VIOLATION.
//  2. If EndpointHeader is set, run Enforcer.Validate(docRegion, Layer,
//     headerValue). The enforcer's reverse-index decides whether the
//     declared endpoint resolves to the document's region.
//
// 451 body shape mirrors the rest of the document service: type,
// message, error_code, details.
func RegionEnforcerHTTP(opts MiddlewareOptions) func(http.Handler) http.Handler {
	tenantHeader := opts.TenantHeader
	if tenantHeader == "" {
		tenantHeader = "X-Auth-Tenant-ID"
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if opts.PathParam == nil || opts.Lookup == nil {
				next.ServeHTTP(w, r)
				return
			}
			docID := opts.PathParam(r)
			if docID == "" {
				next.ServeHTTP(w, r)
				return
			}
			tenantID := r.Header.Get(tenantHeader)
			docRegion, err := opts.Lookup(r.Context(), tenantID, docID)
			if err != nil || docRegion == "" {
				// Lookup failure shouldn't 451 — surface as 500 from
				// the next handler when it tries to load the doc, or
				// 404 if the doc legitimately doesn't exist.
				next.ServeHTTP(w, r)
				return
			}

			if forced := strings.TrimSpace(r.Header.Get("X-Force-Region")); forced != "" {
				if !strings.EqualFold(forced, docRegion) {
					reason := "layer_mismatch"
					if !SameBoundary(forced, docRegion) {
						reason = "cross_boundary"
					}
					writeRegionViolation(w, docRegion, forced, string(opts.Layer), reason)
					return
				}
			}

			if opts.EndpointHeader != "" && opts.Enforcer != nil {
				if ep := strings.TrimSpace(r.Header.Get(opts.EndpointHeader)); ep != "" {
					if err := opts.Enforcer.Validate(docRegion, opts.Layer, ep); err != nil {
						if rv, ok := err.(*ErrRegionViolation); ok {
							writeRegionViolation(w, rv.DocumentRegion, rv.TargetRegion, rv.Layer, rv.Reason)
							return
						}
						http.Error(w, "internal", http.StatusInternalServerError)
						return
					}
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}

func writeRegionViolation(w http.ResponseWriter, docRegion, targetRegion, layer, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnavailableForLegalReasons) // 451
	body := map[string]any{
		"type":       "REGION_VIOLATION",
		"error_code": "REGION_VIOLATION",
		"message":    "operation blocked: target region does not match document residency",
		"details": map[string]any{
			"document_region": docRegion,
			"target_region":   targetRegion,
			"layer":           layer,
			"reason":          reason,
		},
	}
	_ = json.NewEncoder(w).Encode(body)
}
