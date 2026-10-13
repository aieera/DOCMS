package scim

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/database"
)

// TenantResolver looks up the tenant and checks the SCIM bearer token. It
// reads sso_configs rows of any provider_type that carry a
// `scim_token_hash` field in their config JSONB — this keeps SCIM tied to
// whichever SSO relationship the admin already set up.
type TenantResolver struct {
	pool *pgxpool.Pool
}

// NewTenantResolver constructs a resolver.
func NewTenantResolver(pool *pgxpool.Pool) *TenantResolver { return &TenantResolver{pool: pool} }

// tenantCtxKey carries the resolved tenant down the handler chain.
type tenantCtxKey struct{}

// TenantFromContext returns the resolved tenant id. Handlers invoke this
// after Authenticate has run.
func TenantFromContext(ctx context.Context) (uuid.UUID, bool) {
	v, ok := ctx.Value(tenantCtxKey{}).(uuid.UUID)
	return v, ok
}

// Authenticate returns a middleware that rejects requests lacking a valid
// Bearer token. On success it attaches the tenant id to the request ctx.
//
// The token is never logged. Comparison uses constant-time SHA-256 match.
func (r *TenantResolver) Authenticate(tenantSlug string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			raw := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
			if raw == "" || raw == req.Header.Get("Authorization") {
				writeAuthError(w)
				return
			}
			presented := sha256Hex(raw)
			tenantID, storedHash, err := r.lookup(req.Context(), tenantSlug)
			if err != nil || storedHash == "" {
				writeAuthError(w)
				return
			}
			if subtle.ConstantTimeCompare([]byte(presented), []byte(storedHash)) != 1 {
				writeAuthError(w)
				return
			}
			ctx := context.WithValue(req.Context(), tenantCtxKey{}, tenantID)
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	}
}

// lookup fetches (tenant_id, scim_token_hash) by slug. Returns empty hash
// if the tenant exists but no SCIM token is configured — treated as 401.
//
// organizations carries no RLS, so the slug resolves on the pool; the
// FORCE-RLS sso_configs read then runs in that tenant's transaction (on
// the raw pool the production NOBYPASSRLS role saw no rows and every
// SCIM request 401'd — M-27).
func (r *TenantResolver) lookup(ctx context.Context, slug string) (uuid.UUID, string, error) {
	var (
		tenantID uuid.UUID
		raw      []byte
	)
	if err := r.pool.QueryRow(ctx, `
		SELECT id FROM organizations WHERE slug = $1 AND deleted_at IS NULL
	`, strings.ToLower(strings.TrimSpace(slug))).Scan(&tenantID); err != nil {
		return uuid.Nil, "", err
	}
	err := database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT config FROM sso_configs
			WHERE tenant_id = $1 AND is_active AND config ? 'scim_token_hash'
			ORDER BY updated_at DESC
			LIMIT 1
		`, tenantID).Scan(&raw)
	})
	if err != nil {
		return uuid.Nil, "", err
	}
	var cfg struct {
		SCIMTokenHash string `json:"scim_token_hash"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return tenantID, "", err
	}
	return tenantID, cfg.SCIMTokenHash, nil
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func writeAuthError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/scim+json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(NewError(http.StatusUnauthorized, "", "missing or invalid bearer token"))
}
