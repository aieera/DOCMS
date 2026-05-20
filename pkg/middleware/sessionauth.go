// Package middleware: SessionAuth — a session-cookie → UserInfo middleware
// shared by every non-auth service that exposes user-facing REST endpoints.
//
// The auth service has its own richer middleware (Redis fast-path + API-key
// support) in services/auth/internal/handler/middleware.go. SessionAuth here
// is the minimal server-side session validator used by policy, billing, and
// any future service that needs to know "who is this caller?" without
// depending on the auth service's internal packages.
//
// How it works:
//  1. Read the session cookie (default name: "dms_session").
//  2. SHA-256 the plaintext token.
//  3. SELECT sessions JOIN users on the shared Postgres by token_hash.
//     token_hash is globally unique, so no tenant GUC is needed.
//  4. Populate auth.UserInfo on the context via auth.WithUser.
//
// Callers that need to refuse requests for non-admins chain
// middleware.RequireRole after SessionAuth.
package middleware

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vaultdms/vaultdms/pkg/auth"
)

// SessionAuthConfig bundles SessionAuth's dependencies.
type SessionAuthConfig struct {
	Pool       *pgxpool.Pool
	CookieName string // default "dms_session"
}

// SessionAuth returns an http middleware that validates the session cookie
// and attaches a UserInfo to the request context. Unauthenticated requests
// are rejected with 401.
func SessionAuth(cfg SessionAuthConfig) func(http.Handler) http.Handler {
	cookie := cfg.CookieName
	if cookie == "" {
		cookie = "dms_session"
	}
	pool := cfg.Pool
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie(cookie)
			if err != nil || c.Value == "" {
				writeUnauthorized(w, r, "authentication required")
				return
			}
			hash := sha256HexSession(c.Value)

			// token_hash is globally unique; tenant GUC not required.
			var (
				tenantID uuid.UUID
				userID   uuid.UUID
				email    string
				role     string
				expires  time.Time
			)
			err = pool.QueryRow(r.Context(), `
				SELECT s.tenant_id, s.user_id, u.email, u.role, s.expires_at
				FROM sessions s
				JOIN users u ON u.tenant_id = s.tenant_id AND u.id = s.user_id
				WHERE s.token_hash = $1
				  AND s.revoked_at IS NULL
				  AND s.expires_at > now()
				  AND u.deleted_at IS NULL
			`, hash).Scan(&tenantID, &userID, &email, &role, &expires)
			if err != nil {
				if err == pgx.ErrNoRows {
					writeUnauthorized(w, r, "authentication required")
					return
				}
				writeUnauthorized(w, r, "authentication required")
				return
			}

			ctx := auth.WithUser(r.Context(), auth.UserInfo{
				ID:       userID,
				TenantID: tenantID,
				Email:    email,
				Role:     role,
			})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// SessionAuthOptional is the same as SessionAuth but doesn't reject requests
// that lack a valid session cookie — it just passes them through unchanged.
// Use this when you want to populate auth context for cookie-bearing
// requests (so TenantHTTP can fall back to ctx) without forcing every
// upstream caller to also send a cookie. Typical wiring:
//
//	rootMux.Handle("/", RequestLogHTTP(log)(CorrelationHTTP(
//	    SessionAuthOptional(SessionAuthConfig{Pool: pool})(
//	        TenantHTTP(pool)(next)))))
//
// Result: browser AJAX with X-Tenant-ID header keeps working (header is
// consulted first); browser `<img>`/`<video>` requests with only a session
// cookie also work (cookie → ctx → TenantHTTP fallback resolves it).
func SessionAuthOptional(cfg SessionAuthConfig) func(http.Handler) http.Handler {
	cookie := cfg.CookieName
	if cookie == "" {
		cookie = "dms_session"
	}
	pool := cfg.Pool
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie(cookie)
			if err != nil || c.Value == "" {
				next.ServeHTTP(w, r)
				return
			}
			hash := sha256HexSession(c.Value)
			var (
				tenantID uuid.UUID
				userID   uuid.UUID
				email    string
				role     string
				expires  time.Time
			)
			err = pool.QueryRow(r.Context(), `
				SELECT s.tenant_id, s.user_id, u.email, u.role, s.expires_at
				FROM sessions s
				JOIN users u ON u.tenant_id = s.tenant_id AND u.id = s.user_id
				WHERE s.token_hash = $1
				  AND s.revoked_at IS NULL
				  AND s.expires_at > now()
				  AND u.deleted_at IS NULL
			`, hash).Scan(&tenantID, &userID, &email, &role, &expires)
			if err != nil {
				// Bad cookie → don't block. The downstream
				// middleware (e.g. TenantHTTP) will 401 if it
				// can't resolve the tenant from anywhere else.
				next.ServeHTTP(w, r)
				return
			}
			ctx := auth.WithUser(r.Context(), auth.UserInfo{
				ID:       userID,
				TenantID: tenantID,
				Email:    email,
				Role:     role,
			})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireRole rejects requests whose authenticated caller doesn't hold one
// of the allowed roles with 403. Chain this AFTER SessionAuth.
func RequireRole(roles ...string) func(http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(roles))
	for _, r := range roles {
		allowed[r] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role := auth.GetUserRole(r.Context())
			if _, ok := allowed[role]; !ok {
				writeForbidden(w, r, "role not permitted")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func sha256HexSession(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

func writeForbidden(w http.ResponseWriter, r *http.Request, msg string) {
	writeJSON(w, http.StatusForbidden, map[string]any{
		"type":           "FORBIDDEN",
		"message":        msg,
		"correlation_id": auth.GetCorrelationID(r.Context()),
	})
}
