// Package middleware: SessionOrAPIKey lets a single route accept EITHER a
// session cookie OR a Bearer API key OR a trusted internal-service call, so
// the web UI (cookie), service-to-service integrations (key, e.g. the ERP
// outbound push), AND autonomous internal workers (email ingestion,
// watched-folder intake) can share the same handler without duplicating it.
//
// Dispatch is by header:
//   - X-Internal-Service-Key == SEDOC_INTERNAL_API_KEY → internal-service
//     path: the caller is a trusted backend acting on behalf of the identity
//     it names in X-Auth-Tenant-ID / X-User-ID / X-User-Role. Used by the
//     connector's email + intake workers, which have no user session. Safe
//     because the key is a deploy secret only internal services hold, and the
//     gateway strips any client-supplied X-Internal-Service-Key on inbound
//     (deploy/gateway/kong.yaml) so it can't be spoofed from outside.
//   - "Bearer vdms_..." → API-key path (APIKeyAuth — enforces the per-route
//     scope and stamps an api_key-role identity + scopes on ctx).
//   - anything else → session-cookie path (SessionAuth, required).
//
// All three stamp the same auth.UserInfo (tenant + user), so every downstream
// handler, policy check, and tenant predicate behaves identically.
package middleware

import (
	"crypto/subtle"
	"net/http"
	"os"
	"strings"

	"github.com/google/uuid"

	"github.com/aieera/sedoc/pkg/auth"
)

// InternalServiceKeyHeader carries the shared internal-service secret on
// service-to-service calls. The gateway removes it from inbound external
// requests, so a value here is proof the caller is internal.
const InternalServiceKeyHeader = "X-Internal-Service-Key"

// SessionOrAPIKey returns middleware that authenticates via internal-service
// key, session cookie, or Bearer API key. apiScope is the scope an API key
// MUST hold for this route (e.g. "documents:write"); it does not affect
// session-cookie or internal-service callers, whose authorization stays
// role/policy based.
func SessionOrAPIKey(cfg SessionAuthConfig, apiScope string) func(http.Handler) http.Handler {
	session := SessionAuth(cfg)
	apikey := APIKeyAuth(APIKeyAuthConfig{Pool: cfg.Pool, RequiredScope: apiScope})
	return func(next http.Handler) http.Handler {
		sessionNext := session(next)
		apiNext := apikey(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Trusted internal-service call (autonomous workers with no
			// session). Validated by the shared secret; identity is taken
			// from the headers the worker sets for the user it acts as.
			if u, ok := internalServiceIdentity(r); ok {
				next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), u)))
				return
			}
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

// internalServiceIdentity returns the acting identity when the request
// carries a valid internal-service key. The key is compared in constant
// time. The acting tenant is required (X-Auth-Tenant-ID, falling back to
// X-Tenant-ID); the acting user is optional (a worker may have no human
// actor) and the role defaults to admin so ingestion can write.
func internalServiceIdentity(r *http.Request) (auth.UserInfo, bool) {
	want := os.Getenv("SEDOC_INTERNAL_API_KEY")
	got := r.Header.Get(InternalServiceKeyHeader)
	if want == "" || got == "" || subtle.ConstantTimeCompare([]byte(want), []byte(got)) != 1 {
		return auth.UserInfo{}, false
	}
	rawTenant := r.Header.Get("X-Auth-Tenant-ID")
	if rawTenant == "" {
		rawTenant = r.Header.Get("X-Tenant-ID")
	}
	tenantID, err := uuid.Parse(rawTenant)
	if err != nil {
		return auth.UserInfo{}, false
	}
	userID, _ := uuid.Parse(r.Header.Get("X-User-ID")) // optional; uuid.Nil when absent
	role := r.Header.Get("X-User-Role")
	if role == "" {
		role = "admin"
	}
	return auth.UserInfo{ID: userID, TenantID: tenantID, Role: role}, true
}
