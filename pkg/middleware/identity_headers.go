package middleware

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/aieera/sedoc/pkg/auth"
)

// IdentityHeadersHTTP populates the request context with the
// gateway-injected identity (X-Auth-Tenant-ID, X-User-ID, X-User-Role)
// when an upstream auth middleware hasn't already done so.
//
// Why it exists: handlers historically read X-Auth-Tenant-ID / X-User-ID
// straight off the request, which scatters the same parse + the trust
// assumption across ~90 call sites. The standardization (blueprint §24.1
// "tenant-id-from-context sweep") moves them to auth.TenantIDString(r) /
// auth.UserIDString(r), which read from the context. SessionAuthOptional
// fills the context from the session COOKIE, but cookieless callers
// (API-key, service-to-service) carry only the gateway headers — without
// this middleware their context would be empty and the converted handlers
// would see no identity. This gap-fills from the headers so the converted
// handlers are byte-for-byte equivalent to the old direct reads.
//
// Trust: the gateway (Kong / the Vite dev proxy) strips any
// client-supplied X-Auth-* / X-User-* and re-injects the trusted values
// before forwarding, and RequireGatewaySignature gates the boundary — so
// a header reaching a backend is gateway-attested, exactly as the old
// direct reads assumed.
//
// It NEVER overwrites identity an upstream middleware already set (the
// cookie-validated session wins over the header), and it never rejects —
// downstream handlers/middleware decide what to do with a missing
// identity, preserving today's 400/401 behavior.
//
// Chain it directly inside the auth middleware, e.g.:
//
//	RequireGatewaySignature()(SessionAuthOptional(cfg)(IdentityHeadersHTTP()(mux)))
func IdentityHeadersHTTP() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			// Tenant — gap-fill only. TenantFromHeaders prefers
			// X-Auth-Tenant-ID, then the legacy X-Tenant-ID.
			if tid, err := auth.GetTenantID(ctx); err != nil || tid == uuid.Nil {
				if id, e := uuid.Parse(TenantFromHeaders(r)); e == nil && id != uuid.Nil {
					ctx = auth.SetTenantID(ctx, id)
				}
			}

			// User — gap-fill only. WithUser also stamps the tenant on
			// UserInfo, so resolve tenant from the (possibly just-set)
			// context.
			if uid, err := auth.GetUserID(ctx); err != nil || uid == uuid.Nil {
				if id, e := uuid.Parse(r.Header.Get("X-User-ID")); e == nil && id != uuid.Nil {
					tid, _ := auth.GetTenantID(ctx)
					ctx = auth.WithUser(ctx, auth.UserInfo{
						ID:       id,
						TenantID: tid,
						Role:     r.Header.Get("X-User-Role"),
					})
				}
			}

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
