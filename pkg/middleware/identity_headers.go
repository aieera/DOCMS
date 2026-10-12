package middleware

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/aieera/sedoc/pkg/auth"
)

// IdentityHeadersHTTP lets a trusted INTERNAL caller (a backend worker with
// no user session) name the identity it acts for, via X-Auth-Tenant-ID
// (or the legacy X-Tenant-ID), X-User-ID and X-User-Role (DEC-3, C-6).
//
// The headers are honoured only when BOTH hold:
//   - the request carries a valid X-Internal-Service-Key (the
//     SEDOC_INTERNAL_API_KEY deploy secret, compared in constant time; the
//     gateway strips any client-supplied copy), and
//   - no identity is on the context yet (a session / API key resolved by
//     SessionAuth* / APIKeyAuth / SessionOrAPIKey always wins).
//
// Otherwise the headers are ignored. They used to be trusted on the
// assumption that only the gateway could set them, but the gateway
// signature alone is not identity — the Vite host-mode proxy forwards
// client headers WITH a valid signature, so any caller could name any
// tenant, user and role.
//
// It never rejects; downstream handlers/middleware decide what a missing
// identity means (401). Chain it INSIDE the session/API-key middleware:
//
//	RequireGatewaySignature()(SessionOrAPIKey(cfg, scope)(IdentityHeadersHTTP()(mux)))
func IdentityHeadersHTTP() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			if hasContextIdentity(ctx) || !validInternalServiceKey(r) {
				next.ServeHTTP(w, r)
				return
			}
			tid, err := uuid.Parse(TenantFromHeaders(r))
			if err != nil || tid == uuid.Nil {
				next.ServeHTTP(w, r)
				return
			}
			ctx = auth.SetTenantID(ctx, tid)
			if uid, e := uuid.Parse(r.Header.Get("X-User-ID")); e == nil && uid != uuid.Nil {
				ctx = auth.WithUser(ctx, auth.UserInfo{
					ID:       uid,
					TenantID: tid,
					Role:     r.Header.Get("X-User-Role"),
				})
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// hasContextIdentity reports whether an upstream middleware already
// established who the caller is (tenant or user on ctx).
func hasContextIdentity(ctx context.Context) bool {
	if tid, err := auth.GetTenantID(ctx); err == nil && tid != uuid.Nil {
		return true
	}
	if uid, err := auth.GetUserID(ctx); err == nil && uid != uuid.Nil {
		return true
	}
	return false
}
