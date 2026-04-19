package service

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/vaultdms/vaultdms/pkg/database"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/auth/internal/model"
)

// IssueAPIKeyInput is the validated shape for POST /api-keys.
type IssueAPIKeyInput struct {
	TenantID      uuid.UUID
	UserID        uuid.UUID
	Name          string
	Scopes        []string
	ExpiresInDays int
}

// IssueAPIKey creates an API key. Returns the plaintext once; it is never
// persisted. Role enforcement (admin-only) happens in the handler.
func (s *Service) IssueAPIKey(ctx context.Context, in IssueAPIKeyInput) (*model.APIKeyIssued, error) {
	if in.Name == "" {
		return nil, vdmserr.Validation("name", "required")
	}
	if len(in.Scopes) == 0 {
		return nil, vdmserr.Validation("scopes", "at least one scope required")
	}
	valid := model.ValidScopes()
	for _, sc := range in.Scopes {
		if _, ok := valid[sc]; !ok {
			return nil, vdmserr.Validation("scopes", "invalid scope: "+sc)
		}
	}

	// Enforce per-user cap.
	var count int
	err := database.WithTenantTx(ctx, s.pool, in.TenantID, func(tx pgx.Tx) error {
		n, err := s.apiKeys.CountActiveByUser(ctx, tx, in.TenantID, in.UserID)
		count = n
		return err
	})
	if err != nil {
		return nil, err
	}
	if count >= APIKeyMaxPerUser {
		return nil, vdmserr.Conflict("API key limit reached")
	}

	plaintext, err := generateAPIKey()
	if err != nil {
		return nil, err
	}
	keyHash := sha256Hex(plaintext)
	prefix := plaintext[:APIKeyPrefixLen]

	id, err := newUUID()
	if err != nil {
		return nil, err
	}

	var expiresAt *time.Time
	if in.ExpiresInDays > 0 {
		t := s.clock().Add(time.Duration(in.ExpiresInDays) * 24 * time.Hour)
		expiresAt = &t
	}

	key := &model.APIKey{
		TenantID:  in.TenantID,
		ID:        id,
		UserID:    in.UserID,
		Name:      in.Name,
		KeyHash:   keyHash,
		KeyPrefix: prefix,
		Scopes:    in.Scopes,
		ExpiresAt: expiresAt,
		CreatedAt: s.clock(),
	}

	err = database.WithTenantTx(ctx, s.pool, in.TenantID, func(tx pgx.Tx) error {
		if err := s.apiKeys.Create(ctx, tx, key); err != nil {
			return err
		}
		return s.emitAuth(ctx, tx, in.TenantID, id, "dms.auth.api_key_issued.v1", map[string]any{
			"key_id":    id.String(),
			"user_id":   in.UserID.String(),
			"tenant_id": in.TenantID.String(),
			"name":      in.Name,
			"scopes":    in.Scopes,
		})
	})
	if err != nil {
		return nil, err
	}

	return &model.APIKeyIssued{
		ID:        id,
		Key:       plaintext,
		KeyPrefix: prefix,
		Name:      in.Name,
		Scopes:    in.Scopes,
		ExpiresAt: expiresAt,
		CreatedAt: key.CreatedAt,
		Warning:   "This is the only time the full API key will be shown. Store it securely.",
	}, nil
}

// ListAPIKeys returns the caller's keys (never the hash or the plaintext).
func (s *Service) ListAPIKeys(ctx context.Context, tenantID, userID uuid.UUID) ([]model.APIKey, error) {
	var out []model.APIKey
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		keys, err := s.apiKeys.ListByUser(ctx, tx, tenantID, userID)
		if err != nil {
			return err
		}
		out = keys
		return nil
	})
	return out, err
}

// RevokeAPIKey marks a key revoked. Idempotent.
func (s *Service) RevokeAPIKey(ctx context.Context, tenantID, userID, id uuid.UUID) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := s.apiKeys.Revoke(ctx, tx, tenantID, userID, id); err != nil {
			return err
		}
		return s.emitAuth(ctx, tx, tenantID, id, "dms.auth.api_key_revoked.v1", map[string]any{
			"key_id":  id.String(),
			"user_id": userID.String(),
		})
	})
}

// ValidateAPIKey hydrates an APIKey from its plaintext, checking revocation,
// expiry, and optionally a required scope. Returns (*APIKey, nil) on valid.
// The caller uses .UserID / .TenantID to populate auth context.
// Also best-effort updates last_used_at (debounced in Redis to once/minute).
func (s *Service) ValidateAPIKey(ctx context.Context, plaintext, requiredScope string) (*model.APIKey, error) {
	if plaintext == "" {
		return nil, vdmserr.ErrUnauthorized
	}
	hash := sha256Hex(plaintext)
	k, err := s.apiKeys.GetByHash(ctx, s.pool, hash)
	if err != nil {
		return nil, vdmserr.ErrUnauthorized
	}
	if k.RevokedAt != nil {
		return nil, vdmserr.ErrUnauthorized
	}
	if k.ExpiresAt != nil && s.clock().After(*k.ExpiresAt) {
		return nil, vdmserr.ErrUnauthorized
	}
	if requiredScope != "" && !hasScope(k.Scopes, requiredScope) {
		return nil, vdmserr.ErrForbidden
	}

	// Debounce last_used_at updates: at most once per minute per key. The
	// background goroutine gets its own short-lived context so a stalled
	// Redis or Postgres call cannot keep the goroutine alive forever.
	go func(id uuid.UUID) {
		bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.debouncedTouchAPIKey(bgCtx, id)
	}(k.ID)

	return k, nil
}

func (s *Service) debouncedTouchAPIKey(ctx context.Context, id uuid.UUID) {
	key := "apikey_touched:" + id.String()
	ok, err := s.rdb.SetNX(ctx, key, "1", time.Minute).Result()
	if err != nil || !ok {
		return
	}
	_ = s.apiKeys.TouchLastUsed(ctx, s.pool, id, s.clock())
}

// generateAPIKey produces "vdms_" + 48 alphanumeric chars from crypto/rand.
func generateAPIKey() (string, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	const n = 48
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		out[i] = alphabet[int(buf[i])%len(alphabet)]
	}
	return "vdms_" + string(out), nil
}

func hasScope(granted []string, want string) bool {
	for _, g := range granted {
		if g == want {
			return true
		}
	}
	return false
}
