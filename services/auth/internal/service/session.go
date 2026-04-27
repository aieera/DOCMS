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

	"github.com/vaultdms/vaultdms/pkg/database"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/auth/internal/model"
)

// CreatedSession bundles what a successful auth flow returns: the plaintext
// token (returned to the client once, never persisted) plus the session row.
type CreatedSession struct {
	Token     string // plaintext; callers set cookie + echo in response
	Session   *model.Session
	UserView  model.PublicView
}

// createSessionInTx writes a session row + primes the Redis cache, inside
// the caller's TX. Enforces the tenant's concurrent-session limit: when
// N > limit after insert, the oldest session is revoked and a
// session.evicted audit event is appended to the same outbox tx.
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
	// Per-tenant config; cheap DB read, but we're already in a tenant tx
	// so this could be inlined in a single query later if profiling
	// shows it matters.
	cfg := s.LoadSessionConfig(ctx, user.TenantID)
	session := &model.Session{
		ID:             id,
		TenantID:       user.TenantID,
		UserID:         user.ID,
		TokenHash:      tokenHash,
		IPAddress:      ip,
		UserAgent:      ua,
		ExpiresAt:      now.Add(cfg.TTL),
		LastActivityAt: now,
		CreatedAt:      now,
	}
	if err := s.sessions.Create(ctx, tx, session); err != nil {
		return nil, err
	}

	// Enforce concurrent session limit (N > limit → revoke oldest +
	// emit session.evicted). The audit event fires once per eviction so
	// a user who's been logging in aggressively has a clear trail.
	n, err := s.sessions.CountActiveForUser(ctx, tx, user.TenantID, user.ID)
	if err != nil {
		return nil, err
	}
	for i := n; i > cfg.ConcurrentLimit; i-- {
		if err := s.sessions.DeleteOldestForUser(ctx, tx, user.TenantID, user.ID); err != nil {
			return nil, err
		}
		if err := s.emitAuth(ctx, tx, user.TenantID, user.ID,
			"dms.auth.session.evicted.v1", map[string]any{
				"user_id":         user.ID.String(),
				"reason":          "concurrent_limit",
				"concurrent_cap":  cfg.ConcurrentLimit,
				"evicted_at":      now.UTC().Format(time.RFC3339),
			}); err != nil {
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

// ValidationRequest carries the request context bits ValidateSession
// needs beyond the raw token. Empty IP / UA are tolerated (legacy
// sessions and internal service-to-service callers may not populate
// them) — BindingMatches treats unknown sides as "match" so the check
// never spuriously revokes.
type ValidationRequest struct {
	IP        string
	UserAgent string
}

// ValidationResult bundles the cached session plus a binding outcome
// flag. Callers render the flag as X-Session-Warning in `warn` mode or
// bubble up the 401 in `enforce` mode (ValidateSessionWithBinding
// returns the error already for `enforce`).
type ValidationResult struct {
	Session        *model.CachedSession
	BindingWarning bool
}

// ValidateSession preserves the old signature for any caller that does
// not yet pass request context. It delegates to ValidateSessionWithBinding
// with an empty ValidationRequest, so binding degrades to the unknown
// case (no mismatch, no warning).
func (s *Service) ValidateSession(ctx context.Context, plaintextToken string) (*model.CachedSession, error) {
	res, err := s.ValidateSessionWithBinding(ctx, plaintextToken, ValidationRequest{})
	if err != nil {
		return nil, err
	}
	return res.Session, nil
}

// ValidateSessionWithBinding is the hot path that every other service hits via
// its session-validation middleware. Order of operations:
//   1. hash the presented token
//   2. Redis GET session:{hash} — if present and unexpired, proceed to binding check
//   3. on Redis miss, fall through to Postgres via sessions table
//   4. if found, enforce absolute max lifetime + binding strictness
//   5. sliding window: extend ExpiresAt if within threshold; coalesced activity touch (60s)
//
// BindingStrictness is read from the tenant config; on mismatch:
//   - none:    no-op
//   - warn:    sets BindingWarning=true for caller to surface in a header
//   - enforce: revokes session + emits session.binding_mismatch audit, returns 401
func (s *Service) ValidateSessionWithBinding(ctx context.Context, plaintextToken string, req ValidationRequest) (*ValidationResult, error) {
	if plaintextToken == "" {
		return nil, vdmserr.ErrUnauthorized
	}
	hash := sha256Hex(plaintextToken)

	// 1. Redis fast path. Binding values live in the DB row so we always
	//    need at least one DB hit to enforce binding — but the Redis
	//    entry lets us skip the user re-hydration when the cached copy
	//    is fresh enough.
	if cached, err := s.readCachedSession(ctx, hash); err == nil && cached != nil {
		if s.clock().After(cached.ExpiresAt) {
			s.deleteCachedSession(ctx, hash)
			return nil, vdmserr.ErrUnauthorized
		}
		// Fetch the DB row ONLY to enforce binding + activity coalescing.
		// When the tenant's strictness is `none` AND no UA/IP was
		// provided, we can skip this altogether — the cached entry is
		// enough.
		cfg := s.LoadSessionConfig(ctx, cached.TenantID)
		if cfg.BindingStrictness == BindingNone && req.IP == "" && req.UserAgent == "" {
			return &ValidationResult{Session: cached}, nil
		}
		sess, err := s.sessions.GetByTokenHash(ctx, s.pool, hash)
		if err == nil && sess != nil {
			if warning, err := s.applyBinding(ctx, sess, cfg, req, hash); err != nil {
				return nil, err
			} else if warning {
				return &ValidationResult{Session: cached, BindingWarning: true}, nil
			}
			s.coalesceActivity(ctx, sess)
		}
		return &ValidationResult{Session: cached}, nil
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
	cfg := s.LoadSessionConfig(ctx, sess.TenantID)
	// Absolute max lifetime.
	if now.Sub(sess.CreatedAt) > cfg.AbsoluteMax {
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

	// Binding check before we extend the session — if we're about to
	// revoke it on mismatch in enforce mode, don't bother extending.
	warning, err := s.applyBinding(ctx, sess, cfg, req, hash)
	if err != nil {
		return nil, err
	}

	// 3. Sliding window: extend if within threshold, capped by absolute max.
	if sess.ExpiresAt.Sub(now) < cfg.SlidingThreshold {
		newExpiry := now.Add(cfg.TTL)
		if cap := sess.CreatedAt.Add(cfg.AbsoluteMax); newExpiry.After(cap) {
			newExpiry = cap
		}
		_ = s.sessions.ExtendExpiry(ctx, s.pool, sess.ID, newExpiry)
		sess.ExpiresAt = newExpiry
		_ = database.WithTenantTx(ctx, s.pool, sess.TenantID, func(tx pgx.Tx) error {
			return s.emitAuth(ctx, tx, sess.TenantID, sess.UserID,
				"dms.auth.session.refreshed.v1", map[string]any{
					"session_id": sess.ID.String(),
					"new_expiry": newExpiry.UTC().Format(time.RFC3339),
				})
		})
	}
	s.coalesceActivity(ctx, sess)

	cached := &model.CachedSession{
		UserID:    user.ID,
		TenantID:  user.TenantID,
		Email:     user.Email,
		Role:      user.Role,
		ExpiresAt: sess.ExpiresAt,
	}
	s.cacheSession(ctx, hash, cached)
	return &ValidationResult{Session: cached, BindingWarning: warning}, nil
}

// applyBinding enforces the tenant's session_binding_strictness when the
// request's IP CIDR + UA fingerprint drift from the session's. Returns
// (warning, err):
//   - warning=true  → caller surfaces X-Session-Warning (warn mode).
//   - err=ErrUnauthorized → session revoked + binding_mismatch audited (enforce).
//   - (false, nil)  → match OR strictness=none.
func (s *Service) applyBinding(ctx context.Context, sess *model.Session, cfg SessionConfig, req ValidationRequest, hash string) (bool, error) {
	if req.IP == "" && req.UserAgent == "" {
		return false, nil // no request context; nothing to check
	}
	if BindingMatches(sess.IPAddress, sess.UserAgent, req.IP, req.UserAgent) {
		return false, nil
	}
	switch cfg.BindingStrictness {
	case BindingNone:
		return false, nil
	case BindingWarn:
		_ = database.WithTenantTx(ctx, s.pool, sess.TenantID, func(tx pgx.Tx) error {
			return s.emitAuth(ctx, tx, sess.TenantID, sess.UserID,
				"dms.auth.session.binding_mismatch.v1", map[string]any{
					"session_id":     sess.ID.String(),
					"strictness":     string(cfg.BindingStrictness),
					"stored_ip_cidr": IPBindingCIDR(sess.IPAddress),
					"req_ip_cidr":    IPBindingCIDR(req.IP),
					"action":         "warn",
				})
		})
		return true, nil
	case BindingEnforce:
		_ = s.sessions.RevokeByTokenHash(ctx, s.pool, hash)
		s.deleteCachedSession(ctx, hash)
		_ = database.WithTenantTx(ctx, s.pool, sess.TenantID, func(tx pgx.Tx) error {
			return s.emitAuth(ctx, tx, sess.TenantID, sess.UserID,
				"dms.auth.session.binding_mismatch.v1", map[string]any{
					"session_id":     sess.ID.String(),
					"strictness":     string(cfg.BindingStrictness),
					"stored_ip_cidr": IPBindingCIDR(sess.IPAddress),
					"req_ip_cidr":    IPBindingCIDR(req.IP),
					"action":         "revoke",
				})
		})
		return false, vdmserr.ErrUnauthorized
	}
	return false, nil
}

// coalesceActivity writes last_activity_at at most once per 60s per
// session. Without coalescing, every authenticated request hits the DB
// with an UPDATE against a row whose PK is the session_id — at N req/s
// for one logged-in user that's a hot-write bottleneck. 60s is the
// smallest window that keeps "last seen" timestamps actionable for
// admins while cutting 99%+ of the writes.
func (s *Service) coalesceActivity(ctx context.Context, sess *model.Session) {
	const coalesceWindow = 60 * time.Second
	now := s.clock()
	if now.Sub(sess.LastActivityAt) < coalesceWindow {
		return
	}
	_ = s.sessions.TouchActivity(ctx, s.pool, sess.ID, now)
	sess.LastActivityAt = now
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
// Emits a single session.revoked audit carrying the count so the trail
// is usable (one row per revoked session would be noisy).
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
		if err != nil {
			return err
		}
		n = cnt
		if cnt > 0 {
			return s.emitAuth(ctx, tx, tenantID, userID,
				"dms.auth.session.revoked.v1", map[string]any{
					"user_id": userID.String(),
					"scope":   "all_other",
					"count":   cnt,
				})
		}
		return nil
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

// RevokeSession deletes a specific session, asserting ownership. Emits
// a session.revoked audit with scope=single and the target id.
func (s *Service) RevokeSession(ctx context.Context, tenantID, userID, id uuid.UUID) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := s.sessions.RevokeByID(ctx, tx, tenantID, userID, id); err != nil {
			return err
		}
		return s.emitAuth(ctx, tx, tenantID, userID,
			"dms.auth.session.revoked.v1", map[string]any{
				"user_id":    userID.String(),
				"session_id": id.String(),
				"scope":      "single",
			})
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
