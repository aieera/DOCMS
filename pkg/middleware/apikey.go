// Package middleware: APIKeyAuth validates a Bearer API key and stamps
// tenant + user identity onto the request context. Used by the
// iPaaS-facing endpoints (Zapier / Make / n8n polling triggers, ADR
// 0090) which call from outside any session-cookie context.
//
// The auth service owns key issuance + the canonical ValidateAPIKey;
// this middleware does the same SQL lookup directly because we don't
// want every public-API call to add a cross-service gRPC hop. The
// api_keys table has globally unique key_hash so no tenant predicate
// is needed for the lookup itself.
//
// Required scope is a per-route constant (e.g. "integrations:read").
// Mismatched scope → 403. Revoked / expired / missing → 401.
package middleware

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vaultdms/vaultdms/pkg/auth"
)

// APIKeyAuthConfig bundles the middleware's deps.
type APIKeyAuthConfig struct {
	Pool *pgxpool.Pool
	// RequiredScope refuses keys whose scopes array doesn't contain
	// this value. Empty string = scope check skipped (rare).
	RequiredScope string
}

// APIKeyAuth returns http middleware that validates the API key in
// the Authorization header and populates auth.UserInfo on ctx. The
// downstream handler reads tenant / user via auth.GetTenantID /
// auth.GetUserID exactly as a session-authenticated path would.
func APIKeyAuth(cfg APIKeyAuthConfig) func(http.Handler) http.Handler {
	pool := cfg.Pool
	required := cfg.RequiredScope
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractBearer(r)
			if token == "" || !strings.HasPrefix(token, "vdms_") {
				writeUnauthorized(w, r, "api key required")
				return
			}
			hash := sha256Hex(token)
			var (
				keyID     uuid.UUID
				tenantID  uuid.UUID
				userID    uuid.UUID
				scopes    []string
				expiresAt *time.Time
				revokedAt *time.Time
			)
			err := pool.QueryRow(r.Context(), `
				SELECT id, tenant_id, user_id, scopes, expires_at, revoked_at
				FROM api_keys
				WHERE key_hash = $1`,
				hash,
			).Scan(&keyID, &tenantID, &userID, &scopes, &expiresAt, &revokedAt)
			if err != nil {
				writeUnauthorized(w, r, "invalid api key")
				return
			}
			if revokedAt != nil {
				writeUnauthorized(w, r, "api key revoked")
				return
			}
			if expiresAt != nil && time.Now().After(*expiresAt) {
				writeUnauthorized(w, r, "api key expired")
				return
			}
			if required != "" && !containsScope(scopes, required) {
				writeForbidden(w, r, "missing scope: "+required)
				return
			}
			ctx := auth.WithUser(r.Context(), auth.UserInfo{
				ID:       userID,
				TenantID: tenantID,
				Role:     "api_key",
			})
			// MCP tool dispatch + future per-scope routes need the
			// API key's scope list on ctx. Session-cookie callers
			// don't get this — they use role-based gating instead.
			ctx = auth.WithScopes(ctx, scopes)
			// Best-effort last-used touch. Fire-and-forget so a slow
			// UPDATE never blocks the actual request response.
			go func() {
				_, _ = pool.Exec(r.Context(), `UPDATE api_keys SET last_used_at = now() WHERE id = $1`, keyID)
			}()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func extractBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

func sha256Hex(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

func containsScope(scopes []string, want string) bool {
	for _, s := range scopes {
		if s == want {
			return true
		}
		// "integrations:*" grants all integrations:* scopes.
		if strings.HasSuffix(s, ":*") && strings.HasPrefix(want, strings.TrimSuffix(s, "*")) {
			return true
		}
	}
	return false
}
