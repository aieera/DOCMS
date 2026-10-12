// Package middleware: SessionAuth — a session → UserInfo middleware
// shared by every non-auth service that exposes user-facing REST endpoints.
//
// The auth service has its own richer middleware (Redis fast-path + API-key
// support) in services/auth/internal/handler/middleware.go. SessionAuth here
// is the minimal server-side session validator used by policy, billing, and
// any future service that needs to know "who is this caller?" without
// depending on the auth service's internal packages.
//
// How it works:
//  1. Read the session token: the session cookie (default name:
//     "dms_session") first; when no cookie is present, fall back to
//     `Authorization: Bearer <token>` for cookie-less clients (the mobile
//     app, ADR 0117; the Office/Google add-ins hold the same shape of
//     token). `Bearer vdms_…` is NEVER treated as a session — that prefix
//     is reserved for API keys (APIKeyAuth / SessionOrAPIKey dispatch).
//  2. SHA-256 the plaintext token.
//  3. Resolve it through auth_session_identity($hash) — the SECURITY
//     DEFINER pre-tenant lookup from services/auth/migrations/000003
//     (C-3). sessions and users are FORCE RLS and the tenant is not
//     known yet, so a raw-pool SELECT reads zero rows under the
//     production dms_app (NOBYPASSRLS) role. The function only returns
//     live sessions of ACTIVE, non-deleted users (M-8).
//  4. Load the user's groups inside database.WithTenantTx (group_members
//     is FORCE RLS too) and populate auth.UserInfo via auth.WithUser.
//
// Callers that need to refuse requests for non-admins chain
// middleware.RequireRole after SessionAuth.
package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
)

// SessionAuthConfig bundles SessionAuth's dependencies.
type SessionAuthConfig struct {
	Pool       *pgxpool.Pool
	CookieName string // default "dms_session"
}

// loadUserGroups returns the group UUIDs the user belongs to in their
// tenant, read inside the tenant transaction (group_members is FORCE RLS).
// Empty slice on error (best-effort — middleware shouldn't 500 a request
// because a group lookup hiccups; the downstream gates just see no groups
// and fall back to direct-grant / member checks).
func loadUserGroups(ctx context.Context, pool *pgxpool.Pool, tenantID, userID uuid.UUID) []uuid.UUID {
	var out []uuid.UUID
	_ = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT group_id FROM group_members
			 WHERE tenant_id = $1 AND user_id = $2
		`, tenantID, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var g uuid.UUID
			if err := rows.Scan(&g); err != nil {
				return err
			}
			out = append(out, g)
		}
		return rows.Err()
	})
	return out
}

// resolveSession maps a plaintext session token to the caller's identity.
// pgx.ErrNoRows means "no live session of an active user" — an
// authentication verdict; any other error is infrastructure.
func resolveSession(ctx context.Context, pool *pgxpool.Pool, token string) (auth.UserInfo, error) {
	var (
		u       auth.UserInfo
		status  string
		expires time.Time
	)
	err := pool.QueryRow(ctx, `
		SELECT tenant_id, user_id, email, role, status, expires_at
		FROM auth_session_identity($1)
	`, sha256HexSession(token)).Scan(&u.TenantID, &u.ID, &u.Email, &u.Role, &status, &expires)
	if err != nil {
		return auth.UserInfo{}, err
	}
	u.Groups = loadUserGroups(ctx, pool, u.TenantID, u.ID)
	return u, nil
}

// sessionTokenFromRequest extracts the plaintext session token: cookie
// first (the web path — unchanged precedence), then a non-vdms_ Bearer
// (cookie-less clients: mobile, add-ins). Returns "" when neither is
// present. vdms_ bearers are API keys and are deliberately not returned —
// SessionOrAPIKey dispatches those to APIKeyAuth before SessionAuth runs,
// and a vdms_ key would never hash-match a session row anyway; skipping it
// here keeps the 401 reason accurate.
func sessionTokenFromRequest(r *http.Request, cookieName string) string {
	if c, err := r.Cookie(cookieName); err == nil && c.Value != "" {
		return c.Value
	}
	authz := r.Header.Get("Authorization")
	if tok, ok := strings.CutPrefix(authz, "Bearer "); ok {
		tok = strings.TrimSpace(tok)
		if tok != "" && !strings.HasPrefix(tok, "vdms_") {
			return tok
		}
	}
	return ""
}

// SessionAuth returns an http middleware that validates the session token
// (cookie or Bearer) and attaches a UserInfo to the request context.
// Unauthenticated requests are rejected with 401.
func SessionAuth(cfg SessionAuthConfig) func(http.Handler) http.Handler {
	cookie := cfg.CookieName
	if cookie == "" {
		cookie = "dms_session"
	}
	pool := cfg.Pool
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := sessionTokenFromRequest(r, cookie)
			if token == "" {
				writeUnauthorized(w, r, "authentication required")
				return
			}
			user, err := resolveSession(r.Context(), pool, token)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					// No such session — a genuine authentication verdict.
					writeUnauthorized(w, r, "authentication required")
					return
				}
				// Any other error is infrastructure (pool exhausted, DB
				// restarting, timeout) — NOT proof the session is invalid.
				// Answering 401 here made every transient blip destroy the
				// caller's session client-side (intermittent auto-logout).
				writeUnavailable(w, r, "session validation unavailable")
				return
			}

			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), user)))
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
			token := sessionTokenFromRequest(r, cookie)
			if token == "" {
				next.ServeHTTP(w, r)
				return
			}
			user, err := resolveSession(r.Context(), pool, token)
			if err != nil {
				// Bad cookie → don't block. The downstream
				// middleware (e.g. TenantHTTP) will 401 if it
				// can't resolve the tenant from anywhere else.
				next.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), user)))
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
