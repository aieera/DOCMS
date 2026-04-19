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
	"github.com/jackc/pgx/v5/pgxpool"
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
func (r *TenantResolver) lookup(ctx context.Context, slug string) (uuid.UUID, string, error) {
	var (
		tenantID uuid.UUID
		raw      []byte
	)
	err := r.pool.QueryRow(ctx, `
		SELECT o.id, c.config
		FROM organizations o
		JOIN sso_configs c ON c.tenant_id = o.id AND c.is_active
		WHERE o.slug = $1 AND o.deleted_at IS NULL
		  AND c.config ? 'scim_token_hash'
		ORDER BY c.updated_at DESC
		LIMIT 1
	`, strings.ToLower(strings.TrimSpace(slug))).Scan(&tenantID, &raw)
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
