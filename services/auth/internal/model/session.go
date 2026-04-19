package model

import (
	"time"

	"github.com/google/uuid"
)

// Session is a database-backed auth session. The plaintext token is never
// stored — only its SHA-256 hash. The token lives in: (a) the server response
// to login, (b) the dms_session cookie, (c) the Authorization: Bearer header.
// It never survives in the DB.
type Session struct {
	ID             uuid.UUID
	TenantID       uuid.UUID
	UserID         uuid.UUID
	TokenHash      string // SHA-256 hex
	IPAddress      string
	UserAgent      string
	ExpiresAt      time.Time
	LastActivityAt time.Time
	CreatedAt      time.Time
	RevokedAt      *time.Time
}

// SessionSummary is the view returned to users managing their own sessions.
type SessionSummary struct {
	ID             uuid.UUID `json:"id"`
	IPAddress      string    `json:"ip_address"`
	UserAgent      string    `json:"user_agent"`
	CreatedAt      time.Time `json:"created_at"`
	LastActivityAt time.Time `json:"last_activity_at"`
	ExpiresAt      time.Time `json:"expires_at"`
	IsCurrent      bool      `json:"is_current"`
}

// CachedSession is the lightweight projection stored in Redis under
// session:{token_hash}. It carries just enough to populate auth context on
// hot-path validation without a DB round-trip.
type CachedSession struct {
	UserID    uuid.UUID   `json:"user_id"`
	TenantID  uuid.UUID   `json:"tenant_id"`
	Email     string      `json:"email"`
	Role      Role        `json:"role"`
	Groups    []uuid.UUID `json:"groups"`
	ExpiresAt time.Time   `json:"expires_at"`
}

// APIKey is an alternative authentication primitive with scopes.
type APIKey struct {
	TenantID    uuid.UUID
	ID          uuid.UUID
	UserID      uuid.UUID
	Name        string
	KeyHash     string // SHA-256 hex
	KeyPrefix   string // first 12 chars of plaintext — safe to display
	Scopes      []string
	LastUsedAt  *time.Time
	ExpiresAt   *time.Time
	CreatedAt   time.Time
	RevokedAt   *time.Time
}

// APIKeyIssued is what we return on POST /api-keys. The Key field holds the
// full plaintext and is populated ONLY on creation.
type APIKeyIssued struct {
	ID        uuid.UUID  `json:"key_id"`
	Key       string     `json:"api_key"`
	KeyPrefix string     `json:"key_prefix"`
	Name      string     `json:"name"`
	Scopes    []string   `json:"scopes"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	Warning   string     `json:"warning"`
}

// Valid scopes. Handlers check endpoint scope against this allowlist.
const (
	ScopeDocumentsRead  = "documents:read"
	ScopeDocumentsWrite = "documents:write"
	ScopeSearchRead     = "search:read"
	ScopeUpload         = "upload"
	ScopeWebhooksManage = "webhooks:manage"
)

// ValidScopes returns the allowed scope set.
func ValidScopes() map[string]struct{} {
	return map[string]struct{}{
		ScopeDocumentsRead:  {},
		ScopeDocumentsWrite: {},
		ScopeSearchRead:     {},
		ScopeUpload:         {},
		ScopeWebhooksManage: {},
	}
}
