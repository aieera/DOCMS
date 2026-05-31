// Package middleware: SessionOrAPIKey lets a single route accept EITHER a
// session cookie OR a Bearer API key, so the web UI (cookie) and
// service-to-service integrations (key, e.g. the ERP outbound push) can share
// the same handler without duplicating it.
//
// Dispatch is by the Authorization header: "Bearer vdms_..." takes the
// API-key path (APIKeyAuth — enforces the per-route scope and stamps an
// api_key-role identity + scopes on ctx); anything else takes the
// session-cookie path (SessionAuth, required). Both stamp the same
// auth.UserInfo (tenant + user), so every downstream handler, policy check,
// and tenant predicate behaves identically regardless of which path ran.
package middleware

import (
	"net/http"
	"strings"
)

// SessionOrAPIKey returns middleware that authenticates via session cookie or
// Bearer API key. apiScope is the scope an API key MUST hold for this route
// (e.g. "documents:write"); it does not affect session-cookie callers, whose
// authorization stays role/policy based.
func SessionOrAPIKey(cfg SessionAuthConfig, apiScope string) func(http.Handler) http.Handler {
	session := SessionAuth(cfg)
	apikey := APIKeyAuth(APIKeyAuthConfig{Pool: cfg.Pool, RequiredScope: apiScope})
	return func(next http.Handler) http.Handler {
		sessionNext := session(next)
		apiNext := apikey(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Bearer vdms_* is unambiguously an API-key caller — they
			// never hold a session cookie, and APIKeyAuth rejects a
			// malformed/!vdms_ token itself.
			if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer vdms_") {
				apiNext.ServeHTTP(w, r)
				return
			}
			sessionNext.ServeHTTP(w, r)
		})
	}
}
