package main

import (
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/middleware"
)

// newHTTPHandler wraps the notification REST mux with the gateway-signature
// and identity middleware. Split out of main so the auth wiring is testable.
//
// Identity order (DEC-3): SessionAuthOptional resolves the session cookie or
// the mobile app's Bearer session token first; IdentityHeadersHTTP inside it
// honours X-Auth-Tenant-ID / X-User-* only for a caller holding the internal
// service key; requireIdentity answers 401 when neither produced a tenant
// and a user, so no handler runs for an anonymous caller.
func newHTTPHandler(pool *pgxpool.Pool, mux http.Handler) http.Handler {
	return middleware.RequireGatewaySignature()(
		middleware.SessionAuthOptional(middleware.SessionAuthConfig{Pool: pool})(
			middleware.IdentityHeadersHTTP()(requireIdentity(mux))))
}

// requireIdentity answers 401 unless an upstream middleware put both a
// tenant and a user on the request context. Every notification route is
// scoped to the calling user.
func requireIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth.TenantIDString(r) == "" || auth.UserIDString(r) == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "authentication required"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
