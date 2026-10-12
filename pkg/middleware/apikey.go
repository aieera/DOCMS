// Package middleware: APIKeyAuth validates a Bearer API key and stamps
// tenant + user identity onto the request context. Used by the
// iPaaS-facing endpoints (Zapier / Make / n8n polling triggers, ADR
// 0090) which call from outside any session-cookie context.
//
// The auth service owns key issuance + the canonical ValidateAPIKey;
// this middleware does the same lookup directly because we don't want
// every public-API call to add a cross-service gRPC hop. api_keys is
// FORCE RLS and the tenant is what the lookup discovers, so it goes
// through auth's SECURITY DEFINER auth_lookup_api_key_by_hash
// (services/auth/migrations/000001) — a raw-pool SELECT reads zero rows
// under the production dms_app (NOBYPASSRLS) role (C-3).
//
// Required scope is a per-route constant (e.g. "integrations:read").
// Mismatched scope → 403. Revoked / expired / missing → 401.
package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
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
			// The definer function only returns non-revoked keys; the
			// revoked_at check below stays as defence in depth.
			err := pool.QueryRow(r.Context(), `
				SELECT id, tenant_id, user_id, scopes, expires_at, revoked_at
				FROM auth_lookup_api_key_by_hash($1)`,
				hash,
			).Scan(&keyID, &tenantID, &userID, &scopes, &expiresAt, &revokedAt)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					writeUnauthorized(w, r, "invalid api key")
					return
				}
				// Infrastructure (pool, DB restart, missing migration) is
				// not proof the key is bad — same split as SessionAuth.
				writeUnavailable(w, r, "api key validation unavailable")
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
			touchAPIKey(r.Context(), pool, tenantID, keyID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

const (
	// apiKeyTouchInterval bounds last_used_at writes to one per key per
	// minute — the column is an "is this key still in use" signal, not
	// an access log, and a busy integration would otherwise UPDATE the
	// same row on every request.
	apiKeyTouchInterval = time.Minute
	apiKeyTouchTimeout  = 2 * time.Second
)

var apiKeyTouches = struct {
	sync.Mutex
	last map[uuid.UUID]time.Time
}{last: map[uuid.UUID]time.Time{}}

// apiKeyTouchDue reports whether keyID's last_used_at should be written
// now, reserving the slot when it is.
func apiKeyTouchDue(keyID uuid.UUID, now time.Time) bool {
	apiKeyTouches.Lock()
	defer apiKeyTouches.Unlock()
	if last, ok := apiKeyTouches.last[keyID]; ok && now.Sub(last) < apiKeyTouchInterval {
		return false
	}
	apiKeyTouches.last[keyID] = now
	return true
}

// touchAPIKey records last_used_at off the request path (R-4). The write
// is detached from the request (context.WithoutCancel — net/http cancels
// r.Context() as soon as the response is written, which used to drop
// most touches) with its own short timeout, and runs inside the key's
// tenant transaction because api_keys is FORCE RLS. A failed touch frees
// the slot so the next request retries.
func touchAPIKey(reqCtx context.Context, pool *pgxpool.Pool, tenantID, keyID uuid.UUID) {
	if !apiKeyTouchDue(keyID, time.Now()) {
		return
	}
	ctx := context.WithoutCancel(reqCtx)
	go func() {
		ctx, cancel := context.WithTimeout(ctx, apiKeyTouchTimeout)
		defer cancel()
		err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx,
				`UPDATE api_keys SET last_used_at = now() WHERE tenant_id = $1 AND id = $2`,
				tenantID, keyID)
			return e
		})
		if err != nil {
			apiKeyTouches.Lock()
			delete(apiKeyTouches.last, keyID)
			apiKeyTouches.Unlock()
		}
	}()
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
