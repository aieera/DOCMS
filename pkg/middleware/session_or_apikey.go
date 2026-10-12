// Package middleware: SessionOrAPIKey lets a single route accept EITHER a
// session cookie OR a Bearer API key OR a trusted internal-service call, so
// the web UI (cookie), service-to-service integrations (key, e.g. the ERP
// outbound push), AND autonomous internal workers (email ingestion,
// watched-folder intake) can share the same handler without duplicating it.
//
// Dispatch (DEC-3 — a resolved session or API key always beats the
// identity headers):
//   - "Bearer vdms_..." → API-key path (APIKeyAuth — enforces the per-route
//     scope and stamps an api_key-role identity + scopes on ctx).
//   - a session token (dms_session cookie, or a non-vdms_ Bearer) that
//     resolves to a live session → that session's identity. Any identity
//     headers on the request are ignored. A session lookup that fails for
//     infrastructure reasons is 503, never a fall-through.
//   - otherwise (no session token, or one with no live session), a valid
//     X-Internal-Service-Key == SEDOC_INTERNAL_API_KEY → internal-service
//     path: the caller is a trusted backend acting on behalf of the identity
//     it names in X-Auth-Tenant-ID / X-User-ID / X-User-Role. Used by the
//     connector's email + intake workers, which have no user session. Safe
//     because the key is a deploy secret only internal services hold, and the
//     gateway strips any client-supplied X-Internal-Service-Key on inbound
//     (deploy/gateway/kong.yaml) so it can't be spoofed from outside.
//     X-User-Role is REQUIRED and must be a known role (auth.IsKnownRole);
//     a missing or unknown role is 401 — there is no default role.
//   - anything else → 401.
//
// All three stamp the same auth.UserInfo (tenant + user), so every downstream
// handler, policy check, and tenant predicate behaves identically.
package middleware

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/auth"
)

// InternalServiceKeyHeader carries the shared internal-service secret on
// service-to-service calls. The gateway removes it from inbound external
// requests, so a value here is proof the caller is internal.
const InternalServiceKeyHeader = "X-Internal-Service-Key"

// sessionResolver resolves a session token to its identity. A variable so
// the dispatch-order tests can stand in for the database lookup.
var sessionResolver func(ctx context.Context, pool *pgxpool.Pool, token string) (auth.UserInfo, error) = resolveSession

// SessionOrAPIKey returns middleware that authenticates via Bearer API key,
// session (cookie or Bearer), or internal-service key — in that order of
// precedence. apiScope is the scope an API key MUST hold for this route
// (e.g. "documents:write"); it does not affect session-cookie or
// internal-service callers, whose authorization stays role/policy based.
func SessionOrAPIKey(cfg SessionAuthConfig, apiScope string) func(http.Handler) http.Handler {
	cookie := cfg.CookieName
	if cookie == "" {
		cookie = "dms_session"
	}
	apikey := APIKeyAuth(APIKeyAuthConfig{Pool: cfg.Pool, RequiredScope: apiScope})
	return func(next http.Handler) http.Handler {
		apiNext := apikey(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Bearer vdms_* is unambiguously an API-key caller — they
			// never hold a session cookie, and APIKeyAuth rejects a
			// malformed/!vdms_ token itself.
			if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer vdms_") {
				apiNext.ServeHTTP(w, r)
				return
			}
			// Session first (DEC-3): a live session's identity wins over
			// any identity headers, even alongside a valid internal key.
			if token := sessionTokenFromRequest(r, cookie); token != "" {
				user, err := sessionResolver(r.Context(), cfg.Pool, token)
				if err == nil {
					next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), user)))
					return
				}
				if !errors.Is(err, pgx.ErrNoRows) {
					// Infrastructure, not an auth verdict (see SessionAuth).
					writeUnavailable(w, r, "session validation unavailable")
					return
				}
				// No live session: an internal caller may still authenticate
				// with the service key below; anyone else gets 401.
			}
			// Trusted internal-service call (autonomous workers with no
			// session). Validated by the shared secret; identity is taken
			// from the headers the worker sets for the user it acts as.
			if validInternalServiceKey(r) {
				u, ok := internalServiceIdentity(r)
				if !ok {
					writeUnauthorized(w, r, "internal service call requires X-Auth-Tenant-ID and a known X-User-Role")
					return
				}
				next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), u)))
				return
			}
			writeUnauthorized(w, r, "authentication required")
		})
	}
}

// internalServiceIdentity returns the acting identity named by a caller
// that already presented a valid internal-service key. The acting tenant is
// required (X-Auth-Tenant-ID, falling back to X-Tenant-ID); the acting user
// is optional (a worker may have no human actor); the role is REQUIRED and
// must be a known role — a missing or unknown X-User-Role is rejected, never
// defaulted (DEC-3).
func internalServiceIdentity(r *http.Request) (auth.UserInfo, bool) {
	rawTenant := r.Header.Get("X-Auth-Tenant-ID")
	if rawTenant == "" {
		rawTenant = r.Header.Get("X-Tenant-ID")
	}
	tenantID, err := uuid.Parse(rawTenant)
	if err != nil || tenantID == uuid.Nil {
		return auth.UserInfo{}, false
	}
	role := r.Header.Get("X-User-Role")
	if !auth.IsKnownRole(role) {
		return auth.UserInfo{}, false
	}
	userID, _ := uuid.Parse(r.Header.Get("X-User-ID")) // optional; uuid.Nil when absent
	return auth.UserInfo{ID: userID, TenantID: tenantID, Role: role}, true
}

// validInternalServiceKey reports whether r carries the internal-service
// secret (SEDOC_INTERNAL_API_KEY) in X-Internal-Service-Key, compared in
// constant time. An unset secret never matches.
func validInternalServiceKey(r *http.Request) bool {
	want := os.Getenv("SEDOC_INTERNAL_API_KEY")
	got := r.Header.Get(InternalServiceKeyHeader)
	return want != "" && got != "" && subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}
