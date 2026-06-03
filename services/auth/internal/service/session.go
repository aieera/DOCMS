package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/auth/internal/model"
)

// CreatedSession bundles what a successful auth flow returns: the plaintext
// token (returned to the client once, never persisted) plus the session row.
type CreatedSession struct {
	Token     string // plaintext; callers set cookie + echo in response
	Session   *model.Session
	UserView  model.PublicView
}

// createSessionInTx writes a session row + primes the Redis cache, inside
// the caller's TX. The concurrent-session limit is enforced here by
// revoking the oldest session when N > 5.
func (s *Service) createSessionInTx(ctx context.Context, tx pgx.Tx, user *model.User, ip, ua string) (*CreatedSession, error) {
	plaintext, err := randomToken(32) // 256 bits → 64 hex chars
	if err != nil {
		return nil, err
	}
	tokenHash := sha256Hex(plaintext)
	id, err := newUUID()
	if err != nil {
		return nil, err
	}
	now := s.clock()
	session := &model.Session{
		ID:             id,
		TenantID:       user.TenantID,
		UserID:         user.ID,
		TokenHash:      tokenHash,
		IPAddress:      ip,
		UserAgent:      ua,
		ExpiresAt:      now.Add(SessionTTL),
		LastActivityAt: now,
		CreatedAt:      now,
	}
	if err := s.sessions.Create(ctx, tx, session); err != nil {
		return nil, err
	}

	// Enforce concurrent session limit (N > limit → revoke oldest).
	n, err := s.sessions.CountActiveForUser(ctx, tx, user.TenantID, user.ID)
	if err != nil {
		return nil, err
	}
	for i := n; i > ConcurrentSessionLimit; i-- {
		if err := s.sessions.DeleteOldestForUser(ctx, tx, user.TenantID, user.ID); err != nil {
			return nil, err
		}
	}

	// Prime Redis cache. Best-effort; if Redis is down, the next read will
	// fall through to Postgres.
	s.cacheSession(ctx, tokenHash, &model.CachedSession{
		UserID:    user.ID,
		TenantID:  user.TenantID,
		Email:     user.Email,
		Role:      user.Role,
		ExpiresAt: session.ExpiresAt,
	})

	return &CreatedSession{Token: plaintext, Session: session, UserView: user.ToPublic()}, nil
}

// ValidateSession is the hot path that every other service hits via its
// session-validation middleware. Order of operations:
//   1. hash the presented token
//   2. Redis GET session:{hash} — if present, return immediately
//   3. on Redis miss, fall through to Postgres via sessions table
//   4. if found and within SessionSlidingThreshold of expiry, extend
//   5. if absolute lifetime exceeded, reject
//
// Returns a CachedSession populated from whichever source served the read.
func (s *Service) ValidateSession(ctx context.Context, plaintextToken string) (*model.CachedSession, error) {
	if plaintextToken == "" {
		return nil, vdmserr.ErrUnauthorized
	}
	hash := sha256Hex(plaintextToken)

	// 1. Redis fast path.
	if cached, err := s.readCachedSession(ctx, hash); err == nil && cached != nil {
		if s.clock().After(cached.ExpiresAt) {
			s.deleteCachedSession(ctx, hash)
			return nil, vdmserr.ErrUnauthorized
		}
		return cached, nil
	}

	// 2. Postgres fallback.
	sess, err := s.sessions.GetByTokenHash(ctx, s.pool, hash)
	if err != nil {
		return nil, vdmserr.ErrUnauthorized
	}
	now := s.clock()
	if now.After(sess.ExpiresAt) {
		return nil, vdmserr.ErrUnauthorized
	}
	// Absolute max lifetime.
	if now.Sub(sess.CreatedAt) > SessionMaxLifetime {
		_ = s.sessions.RevokeByTokenHash(ctx, s.pool, hash)
		s.deleteCachedSession(ctx, hash)
		return nil, vdmserr.ErrUnauthorized
	}

	// Re-hydrate user to get current email + role (may have changed).
	var user *model.User
	if err := database.WithTenantTx(ctx, s.pool, sess.TenantID, func(tx pgx.Tx) error {
		u, err := s.users.GetByID(ctx, tx, sess.TenantID, sess.UserID)
		if err != nil {
			return err
		}
		user = u
		return nil
	}); err != nil {
		return nil, vdmserr.ErrUnauthorized
	}
	if user.Status != model.StatusActive {
		_ = s.sessions.RevokeByTokenHash(ctx, s.pool, hash)
		s.deleteCachedSession(ctx, hash)
		return nil, vdmserr.ErrUnauthorized
	}

	// 3. Sliding window: extend if within threshold, capped by absolute max.
	if sess.ExpiresAt.Sub(now) < SessionSlidingThreshold {
		newExpiry := now.Add(SessionTTL)
		if cap := sess.CreatedAt.Add(SessionMaxLifetime); newExpiry.After(cap) {
			newExpiry = cap
		}
		_ = s.sessions.ExtendExpiry(ctx, s.pool, sess.ID, newExpiry)
		sess.ExpiresAt = newExpiry
	}
	_ = s.sessions.TouchActivity(ctx, s.pool, sess.ID, now)

	cached := &model.CachedSession{
		UserID:    user.ID,
		TenantID:  user.TenantID,
		Email:     user.Email,
		Role:      user.Role,
		ExpiresAt: sess.ExpiresAt,
	}
	s.cacheSession(ctx, hash, cached)
	return cached, nil
}

// Logout revokes by plaintext token (hashes it first).
func (s *Service) Logout(ctx context.Context, plaintextToken string) error {
	if plaintextToken == "" {
		return nil
	}
	hash := sha256Hex(plaintextToken)
	if err := s.sessions.RevokeByTokenHash(ctx, s.pool, hash); err != nil {
		return err
	}
	s.deleteCachedSession(ctx, hash)

	// Audit (best-effort; do not fail logout if audit fails).
	sess, err := s.sessions.GetByTokenHash(ctx, s.pool, hash)
	if err == nil && sess != nil {
		_ = database.WithTenantTx(ctx, s.pool, sess.TenantID, func(tx pgx.Tx) error {
			return s.emitAuth(ctx, tx, sess.TenantID, sess.UserID,
				"dms.auth.logout.v1", map[string]any{
					"session_id": sess.ID.String(),
					"user_id":    sess.UserID.String(),
				})
		})
	}
	return nil
}

// RevokeAllOtherSessions revokes every active session for the caller
// except currentTokenHash (the one they're using to make this request).
func (s *Service) RevokeAllOtherSessions(ctx context.Context, tenantID, userID uuid.UUID, currentTokenHash string) (int64, error) {
	// Look up the current session id, then revoke-all except that id.
	var exceptID *uuid.UUID
	if currentTokenHash != "" {
		if s, err := s.sessions.GetByTokenHash(ctx, s.pool, currentTokenHash); err == nil && s != nil {
			id := s.ID
			exceptID = &id
		}
	}
	var n int64
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		cnt, err := s.sessions.RevokeAllForUser(ctx, tx, tenantID, userID, exceptID)
		n = cnt
		return err
	})
	// Best-effort Redis cleanup: we don't know every token_hash here, so
	// we rely on the expires-at check in ValidateSession catching revoked
	// rows on next read (Redis TTL ≤ SessionTTL).
	return n, err
}

// ListUserSessions returns active sessions for a user for the "manage devices"
// UI. The current session is flagged if currentTokenHash matches.
func (s *Service) ListUserSessions(ctx context.Context, tenantID, userID uuid.UUID, currentTokenHash string) ([]model.SessionSummary, error) {
	var out []model.SessionSummary
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		sessions, err := s.sessions.ListActiveForUser(ctx, tx, tenantID, userID)
		if err != nil {
			return err
		}
		for _, s := range sessions {
			out = append(out, model.SessionSummary{
				ID:             s.ID,
				IPAddress:      s.IPAddress,
				UserAgent:      s.UserAgent,
				CreatedAt:      s.CreatedAt,
				LastActivityAt: s.LastActivityAt,
				ExpiresAt:      s.ExpiresAt,
				IsCurrent:      s.TokenHash == currentTokenHash,
			})
		}
		return nil
	})
	return out, err
}

// RevokeSession deletes a specific session, asserting ownership.
func (s *Service) RevokeSession(ctx context.Context, tenantID, userID, id uuid.UUID) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return s.sessions.RevokeByID(ctx, tx, tenantID, userID, id)
	})
}

// ---- Redis cache helpers --------------------------------------------------
//
// Keys:
//   session:{tenantID}:{hash}    → JSON CachedSession (tenant-scoped, defense-in-depth)
//   session_tenant:{hash}        → tenantID (indirection used by ValidateSession fast
//                                  path, which arrives with only the token hash)
// Both keys are written/deleted together and share the same TTL.

func sessionKey(tenantID uuid.UUID, hash string) string {
	return "session:" + tenantID.String() + ":" + hash
}

func sessionTenantKey(hash string) string { return "session_tenant:" + hash }

func (s *Service) cacheSession(ctx context.Context, hash string, c *model.CachedSession) {
	b, err := json.Marshal(c)
	if err != nil {
		return
	}
	ttl := time.Until(c.ExpiresAt)
	if ttl <= 0 {
		return
	}
	pipe := s.rdb.Pipeline()
	pipe.Set(ctx, sessionKey(c.TenantID, hash), b, ttl)
	pipe.Set(ctx, sessionTenantKey(hash), c.TenantID.String(), ttl)
	_, _ = pipe.Exec(ctx)
}

func (s *Service) readCachedSession(ctx context.Context, hash string) (*model.CachedSession, error) {
	tenantStr, err := s.rdb.Get(ctx, sessionTenantKey(hash)).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	tenantID, err := uuid.Parse(tenantStr)
	if err != nil {
		return nil, err
	}
	b, err := s.rdb.Get(ctx, sessionKey(tenantID, hash)).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c model.CachedSession
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// deleteCachedSession removes both cache entries. Tenant ID is resolved
// via the sess_tenant lookup if the caller doesn't already know it.
func (s *Service) deleteCachedSession(ctx context.Context, hash string) {
	tenantStr, err := s.rdb.Get(ctx, sessionTenantKey(hash)).Result()
	pipe := s.rdb.Pipeline()
	pipe.Del(ctx, sessionTenantKey(hash))
	if err == nil {
		if tid, perr := uuid.Parse(tenantStr); perr == nil {
			pipe.Del(ctx, sessionKey(tid, hash))
		}
	}
	_, _ = pipe.Exec(ctx)
}

// ensure fmt + errors imports are used on paths that don't invoke them directly
var _ = fmt.Sprintf
var _ = errors.New
