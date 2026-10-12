package main

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/middleware"
)

// newHTTPHandler wraps the search REST mux with the gateway-signature and
// identity middleware. Split out of main so the auth wiring is testable.
//
// Identity order (DEC-3): the session / API-key middleware runs first, so a
// resolved session always wins; IdentityHeadersHTTP runs inside it and
// honours X-Auth-Tenant-ID / X-User-* only for a caller holding the internal
// service key; requireIdentity then answers 401 when neither produced a
// tenant and a user. Forged identity headers alone never reach a handler.
func newHTTPHandler(pool *pgxpool.Pool, mux http.Handler) http.Handler {
	sessionCfg := middleware.SessionAuthConfig{Pool: pool}
	authed := func(h http.Handler) http.Handler {
		return middleware.SessionAuthOptional(sessionCfg)(
			middleware.IdentityHeadersHTTP()(requireIdentity(h)))
	}

	root := http.NewServeMux()
	// ERP outbound-push dedup hits POST /api/v1/search with a Bearer API key
	// (scope "search:read"); the same route serves the cookie-authed web UI
	// and internal workers (internal service key). SessionOrAPIKey answers
	// 401 itself when none of the three is present.
	root.Handle("POST /api/v1/search",
		middleware.SessionOrAPIKey(sessionCfg, "search:read")(mux))
	// WS5 — idempotency on the saved-search mutations the integration drives.
	// create is a resource-creating POST (IdempotencyRequired); promote toggles
	// an existing row (Idempotency, honoured-when-supplied). The idempotency
	// store is document migration 000066's idempotency_keys in the shared
	// database. More specific patterns than "/" so they win the mux match.
	root.Handle("POST /api/v1/saved-searches", authed(middleware.IdempotencyRequired(pool)(mux)))
	root.Handle("POST /api/v1/saved-searches/{id}/promote", authed(middleware.Idempotency(pool)(mux)))
	// M-13: service-to-service endpoints (DSR subject erase). The gateway
	// signature alone is not caller identity — the Vite host-mode proxy
	// adds it to any request — so these require the internal service key.
	// IdentityHeadersHTTP then picks up the tenant the caller acts for,
	// which the handler matches against the body.
	root.Handle("/internal/v1/", requireInternalKey(middleware.IdentityHeadersHTTP()(mux)))
	root.Handle("/", authed(mux))
	// DEC-19: every search route takes JSON or no body — cap at 1 MiB ahead
	// of any authentication.
	return middleware.RequireGatewaySignature()(middleware.MaxBody(middleware.MaxJSONBody)(root))
}

// requireInternalKey answers 401 unless the request carries the internal
// service key (SEDOC_INTERNAL_API_KEY, compared in constant time; an unset
// key never matches).
func requireInternalKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := os.Getenv("SEDOC_INTERNAL_API_KEY")
		got := r.Header.Get(middleware.InternalServiceKeyHeader)
		if want == "" || got == "" || subtle.ConstantTimeCompare([]byte(want), []byte(got)) != 1 {
			writeJSONError(w, http.StatusUnauthorized, "internal service key required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireIdentity answers 401 unless an upstream middleware put both a
// tenant and a user on the request context.
func requireIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth.TenantIDString(r) == "" || auth.UserIDString(r) == "" {
			writeJSONError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
