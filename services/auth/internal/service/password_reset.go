// Password-reset flow (audit-2026-05 Track 2).
//
// Threat model:
//   - Enumeration oracle: forgot-password ALWAYS reports success and runs to a
//     fixed time floor, so "email exists" leaks neither via body nor timing.
//   - Token theft: the opaque token is 256-bit, single-use, 30-min TTL; only
//     its SHA-256 is stored. The token is "<tenant_id>.<random>" so reset stays
//     tenant-scoped (no cross-tenant lookup).
//   - Stolen-cookie survival: a successful reset revokes EVERY session for the
//     user, so an attacker's existing cookie dies with the reset.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/auth/internal/model"
)

const (
	resetTokenTTL     = 30 * time.Minute
	resetTimingFloor  = 200 * time.Millisecond // forgot-password constant-time floor
	resetMaxPerWindow = 3
	resetWindow       = 15 * time.Minute
)

// ErrResetTokenGone signals a used/expired reset token. The handler maps it to
// 410 Gone (distinct from a malformed/unknown token, which is a generic 401).
var ErrResetTokenGone = errors.New("reset token used or expired")

// RequestPasswordReset never reveals whether the account exists. It always
// returns (the handler always 200s); internal failures are logged, not
// surfaced. When (tenant, email) resolves to an active user it mints + stores a
// token and emits dms.auth.password_reset_requested.v1 for the notification
// service to email.
func (s *Service) RequestPasswordReset(ctx context.Context, email, tenantSlug, ip string) {
	started := time.Now()
	defer func() {
		if d := time.Since(started); d < resetTimingFloor {
			time.Sleep(resetTimingFloor - d)
		}
	}()

	email = strings.ToLower(strings.TrimSpace(email))
	org, err := s.users.FindOrganizationBySlug(ctx, s.pool, tenantSlug)
	if err != nil || org == nil || org.DeletedAt != nil {
		return
	}
	if !s.allowResetRequest(ctx, org.ID, email) {
		return // per-email throttle; still 200 + floor so it's not an oracle
	}

	if err := database.WithTenantTx(ctx, s.pool, org.ID, func(tx pgx.Tx) error {
		user, uerr := s.users.GetByEmail(ctx, tx, org.ID, email)
		if uerr != nil || user == nil || user.Status != model.StatusActive {
			return nil // no active account — silently no-op
		}
		rnd, rerr := randomToken(32)
		if rerr != nil {
			return rerr
		}
		token := org.ID.String() + "." + rnd
		expiresAt := s.clock().Add(resetTokenTTL)
		if ierr := s.users.InsertPasswordResetToken(ctx, tx, org.ID, user.ID, sha256Hex(token), expiresAt, ip); ierr != nil {
			return ierr
		}
		payload, _ := json.Marshal(map[string]any{
			"user_id":     user.ID.String(),
			"email":       user.Email,
			"reset_token": token, // plaintext for the email link; never persisted
			"expires_at":  expiresAt.Format(time.RFC3339),
			"locale":      user.Locale,
		})
		evt := database.NewOutboxEvent(org.ID, "dms.auth.password_reset_requested.v1", "user", user.ID, payload)
		return s.outbox.Insert(ctx, tx, evt)
	}); err != nil {
		s.log.Error().Err(err).Msg("password reset request failed (suppressed to avoid enumeration)")
	}
}

// allowResetRequest throttles to resetMaxPerWindow per (tenant,email).
func (s *Service) allowResetRequest(ctx context.Context, tenantID uuid.UUID, email string) bool {
	key := "pwreset:" + tenantID.String() + ":" + email
	n, err := s.rdb.Incr(ctx, key).Result()
	if err != nil {
		return true // fail-open on a redis blip; per-IP limit at the router still applies
	}
	if n == 1 {
		_ = s.rdb.Expire(ctx, key, resetWindow).Err()
	}
	return n <= resetMaxPerWindow
}

// ResetPassword consumes a token, sets the new password, revokes all sessions,
// and emits dms.auth.password_changed.v1. Replayed/expired tokens → ErrResetTokenGone.
func (s *Service) ResetPassword(ctx context.Context, token, newPassword string) error {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 || parts[1] == "" {
		return vdmserr.ErrUnauthorized
	}
	tenantID, err := uuid.Parse(parts[0])
	if err != nil {
		return vdmserr.ErrUnauthorized
	}
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	hash, err := bcryptHash(newPassword)
	if err != nil {
		return err
	}
	tokenHash := sha256Hex(token)

	var revokedHashes []string
	if err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rt, ferr := s.users.FindPasswordResetToken(ctx, tx, tenantID, tokenHash)
		if ferr != nil {
			return vdmserr.ErrUnauthorized // unknown token — generic
		}
		if rt.UsedAt != nil || s.clock().After(rt.ExpiresAt) {
			return ErrResetTokenGone
		}
		if serr := s.users.SetPasswordHash(ctx, tx, tenantID, rt.UserID, hash); serr != nil {
			return serr
		}
		if merr := s.users.MarkPasswordResetTokenUsed(ctx, tx, tenantID, tokenHash); merr != nil {
			return merr
		}
		// Capture the live sessions before revoking so we can purge their Redis
		// cache entries after commit — RevokeAllForUser only flips revoked_at,
		// but ValidateSession reads a cache, so without this the stolen cookie
		// would survive until the cache TTL.
		active, lerr := s.sessions.ListActiveForUser(ctx, tx, tenantID, rt.UserID)
		if lerr != nil {
			return lerr
		}
		for i := range active {
			revokedHashes = append(revokedHashes, active[i].TokenHash)
		}
		// Kill every session — a stolen cookie must not survive the reset.
		if _, rerr := s.sessions.RevokeAllForUser(ctx, tx, tenantID, rt.UserID, nil); rerr != nil {
			return rerr
		}
		payload, _ := json.Marshal(map[string]any{"user_id": rt.UserID.String()})
		evt := database.NewOutboxEvent(tenantID, "dms.auth.password_changed.v1", "user", rt.UserID, payload)
		return s.outbox.Insert(ctx, tx, evt)
	}); err != nil {
		return err
	}
	// Immediate revocation: drop the cached sessions now, not at TTL.
	for _, h := range revokedHashes {
		s.deleteCachedSession(ctx, h)
	}
	return nil
}
