package service

// Wave 15.3 — force-change-password.
//
// Flow:
//   1. Login detects users.must_change_password=true. Instead of
//      creating a session, it issues a short-lived one-time change
//      token and returns require_password_change=true to the client.
//   2. Client POSTs the token + new password to /auth/change-password.
//   3. Server validates the new password (length/char policy + not
//      in last 5 hashes), writes it via a tenant TX, records the
//      history row, clears the must_change flag, emits
//      dms.auth.password_changed.v1 via the outbox, and hands back a
//      normal session.
//
// SSO users (users.sso_federated=true) never enter this path — they
// have no local password hash. The expiry sweeper also skips them.
//
// The one-time token is kept in Redis under:
//   auth:pwchange:<sha256(token)>  →  { tenant_id, user_id, reason }
// with a 10-minute TTL. It is single-use: accepted exactly once,
// deleted on redeem or on first failed attempt that reveals a token
// is otherwise valid.

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

const (
	// PasswordChangeTokenTTL is how long a user has between a
	// must_change_password login and completing the change. Short on
	// purpose — this is a second-factor-equivalent hand-off.
	PasswordChangeTokenTTL = 10 * time.Minute

	// PasswordHistoryKeep is the number of prior hashes retained per
	// user to reject reuse. Mirrors the brief's "last 5".
	PasswordHistoryKeep = 5

	// DefaultPasswordExpiryDays is used when the tenant setting is
	// missing (shouldn't happen post-migration) but keeps the cron
	// safe to call on under-configured tenants.
	DefaultPasswordExpiryDays = 90
)

// ChangePasswordReason tags the emitted event so downstream can
// distinguish admin-forced from scheduled-expiry from self-initiated.
type ChangePasswordReason string

const (
	ReasonAdminReset     ChangePasswordReason = "admin_reset"
	ReasonExpiry         ChangePasswordReason = "expiry"
	ReasonFirstLogin     ChangePasswordReason = "first_login"
	ReasonSelfInitiated  ChangePasswordReason = "self"
	ReasonHIBPCompromise ChangePasswordReason = "hibp_compromise"
)

// pwChangeState is the Redis payload for a one-time change token.
type pwChangeState struct {
	TenantID uuid.UUID            `json:"tenant_id"`
	UserID   uuid.UUID            `json:"user_id"`
	Reason   ChangePasswordReason `json:"reason"`
}

// ErrPasswordChangeRequired is the sentinel returned by Login when
// the user must change their password before receiving a session.
// The Login wrapper never surfaces this as an error to the wire; the
// handler checks LoginResult.RequirePasswordChange instead. Kept for
// internal branching.
var ErrPasswordChangeRequired = errors.New("password change required")

// ErrPasswordReused is returned by ChangePassword when the candidate
// matches any of the last N stored hashes.
var ErrPasswordReused = vdmserr.Validation("password", "must not match your last 5 passwords")

// ErrPasswordChangeTokenInvalid is returned when the one-time change
// token is missing, expired, or already consumed.
var ErrPasswordChangeTokenInvalid = vdmserr.Unauthorized("password change token invalid or expired")

// ---- Token helpers --------------------------------------------------------

func pwChangeKey(tokenSHA string) string {
	return "auth:pwchange:" + tokenSHA
}

// issuePasswordChangeToken mints a single-use plaintext token,
// hashes it for the Redis key, and stores the user + reason payload.
// Returns the plaintext token (handed to the client once).
func (s *Service) issuePasswordChangeToken(ctx context.Context, tenantID, userID uuid.UUID, reason ChangePasswordReason) (string, error) {
	token, err := randomToken(32)
	if err != nil {
		return "", err
	}
	state, _ := json.Marshal(pwChangeState{TenantID: tenantID, UserID: userID, Reason: reason})
	if err := s.rdb.Set(ctx, pwChangeKey(sha256Hex(token)), state, PasswordChangeTokenTTL).Err(); err != nil {
		return "", fmt.Errorf("redis set pwchange: %w", err)
	}
	return token, nil
}

// consumePasswordChangeToken looks up and deletes the token atomically
// so it's truly single-use. Returns the stored state.
func (s *Service) consumePasswordChangeToken(ctx context.Context, token string) (*pwChangeState, error) {
	if token == "" {
		return nil, ErrPasswordChangeTokenInvalid
	}
	key := pwChangeKey(sha256Hex(token))
	val, err := s.rdb.GetDel(ctx, key).Result()
	if err == redis.Nil {
		return nil, ErrPasswordChangeTokenInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("redis getdel pwchange: %w", err)
	}
	var st pwChangeState
	if err := json.Unmarshal([]byte(val), &st); err != nil {
		return nil, ErrPasswordChangeTokenInvalid
	}
	return &st, nil
}

// ---- ChangePassword flow --------------------------------------------------

// ChangePasswordInput is what the /auth/change-password endpoint
// hands to the service.
type ChangePasswordInput struct {
	OneTimeChangeToken string
	NewPassword        string
	IPAddress          string
	UserAgent          string
}

// ChangePasswordResult is returned on success: a normal session.
type ChangePasswordResult struct {
	Session *CreatedSession
}

// ChangePassword redeems a one-time token, validates the new
// password against policy + history, writes it, records history,
// clears the must_change flag, emits an outbox event, and issues a
// session.
func (s *Service) ChangePassword(ctx context.Context, in ChangePasswordInput) (*ChangePasswordResult, error) {
	if err := validatePassword(in.NewPassword); err != nil {
		return nil, err
	}

	state, err := s.consumePasswordChangeToken(ctx, in.OneTimeChangeToken)
	if err != nil {
		return nil, err
	}

	var result *ChangePasswordResult
	err = database.WithTenantTx(ctx, s.pool, state.TenantID, func(tx pgx.Tx) error {
		user, err := s.users.GetByID(ctx, tx, state.TenantID, state.UserID)
		if err != nil {
			return err
		}
		if user.Status != model.StatusActive {
			return ErrInvalidCredentials
		}
		if user.SSOFederated {
			// SSO users don't have a local password. Should never
			// hit this path but reject defensively.
			return vdmserr.Validation("password", "SSO-federated users cannot change a local password")
		}

		// Reject reuse against the current hash + last N history rows.
		if user.PasswordHash != "" && bcryptCompare(user.PasswordHash, in.NewPassword) {
			return ErrPasswordReused
		}
		prior, err := s.users.RecentPasswordHashes(ctx, tx, state.TenantID, state.UserID, PasswordHistoryKeep)
		if err != nil {
			return err
		}
		for _, h := range prior {
			if bcryptCompare(h, in.NewPassword) {
				return ErrPasswordReused
			}
		}

		newHash, err := bcryptHash(in.NewPassword)
		if err != nil {
			return err
		}

		now := s.clock()
		expiresAt, err := s.computeExpiresAt(ctx, tx, state.TenantID, now)
		if err != nil {
			return err
		}

		if err := s.users.SetPasswordHash(ctx, tx, state.TenantID, state.UserID, newHash); err != nil {
			return err
		}
		if err := s.users.SetPasswordLifecycle(ctx, tx, state.TenantID, state.UserID, now, expiresAt); err != nil {
			return err
		}
		if err := s.users.RecordPasswordHistory(ctx, tx, state.TenantID, state.UserID, newHash, PasswordHistoryKeep); err != nil {
			return err
		}

		// Re-read so the session creator sees the fresh flags.
		user, err = s.users.GetByID(ctx, tx, state.TenantID, state.UserID)
		if err != nil {
			return err
		}

		// Emit outbox event. Payload deliberately excludes the hash.
		if err := s.emitAuth(ctx, tx, state.TenantID, state.UserID, "dms.auth.password_changed.v1", map[string]any{
			"user_id":   state.UserID.String(),
			"tenant_id": state.TenantID.String(),
			"reason":    string(state.Reason),
			"at":        now.Format(time.RFC3339),
		}); err != nil {
			return err
		}

		// Issue session. finishLogin writes last_login + login_success.
		sess, err := s.createSessionInTx(ctx, tx, user, in.IPAddress, in.UserAgent)
		if err != nil {
			return err
		}
		if err := s.users.UpdateLastLogin(ctx, tx, user.TenantID, user.ID, now); err != nil {
			return err
		}
		if err := s.emitAuth(ctx, tx, user.TenantID, user.ID, "dms.auth.login_success.v1", map[string]any{
			"user_id":    user.ID.String(),
			"tenant_id":  user.TenantID.String(),
			"method":     "password_change",
			"ip":         in.IPAddress,
			"user_agent": in.UserAgent,
		}); err != nil {
			return err
		}
		result = &ChangePasswordResult{Session: sess}
		PasswordChangedTotal.WithLabelValues(string(state.Reason)).Inc()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// computeExpiresAt resolves the tenant's password_expiry_days and
// returns the new absolute expiry timestamp, or nil if expiry is
// disabled (0).
func (s *Service) computeExpiresAt(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, now time.Time) (*time.Time, error) {
	days, err := s.users.GetTenantPasswordExpiryDays(ctx, tx, tenantID)
	if err != nil {
		// If the org row can't be read (shouldn't happen inside a
		// tenant tx) fall back to the default rather than orphaning
		// the change.
		days = DefaultPasswordExpiryDays
	}
	if days <= 0 {
		return nil, nil
	}
	exp := now.Add(time.Duration(days) * 24 * time.Hour)
	return &exp, nil
}

// ---- Admin force-reset ----------------------------------------------------

// ForcePasswordReset is the admin-triggered variant: sets
// must_change_password=true on the target user, emits an audit
// event, and returns. The user's next Login will receive a change
// token. This method does NOT reveal whether the target user exists;
// callers gate with the admin RequireRole middleware before calling.
func (s *Service) ForcePasswordReset(ctx context.Context, tenantID, actorID, targetUserID uuid.UUID) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		target, err := s.users.GetByID(ctx, tx, tenantID, targetUserID)
		if err != nil {
			return err
		}
		if target.SSOFederated {
			return vdmserr.Validation("user", "SSO-federated users cannot have their local password reset")
		}
		if err := s.users.SetMustChangePassword(ctx, tx, tenantID, targetUserID, true); err != nil {
			return err
		}
		// Invalidate all live sessions so the user is forced back
		// through login.
		if _, err := s.sessions.RevokeAllForUser(ctx, tx, tenantID, targetUserID, nil); err != nil {
			return err
		}
		return s.emitAuth(ctx, tx, tenantID, targetUserID, "dms.auth.password_reset_requested.v1", map[string]any{
			"user_id":   targetUserID.String(),
			"tenant_id": tenantID.String(),
			"actor_id":  actorID.String(),
			"reason":    string(ReasonAdminReset),
			"at":        s.clock().Format(time.RFC3339),
		})
	})
}

// ---- Expiry sweeper -------------------------------------------------------

// SweepExpiredPasswordsForTenant flags local-auth users whose
// password_expires_at has passed, emits dms.auth.password_expired.v1
// per user, and returns the count flagged. Intended to be called
// from a daily Temporal schedule; safe to invoke repeatedly.
func (s *Service) SweepExpiredPasswordsForTenant(ctx context.Context, tenantID uuid.UUID) (int, error) {
	var flagged int
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		ids, err := s.users.FlagExpiredPasswords(ctx, tx, tenantID, s.clock(), 500)
		if err != nil {
			return err
		}
		flagged = len(ids)
		for _, uid := range ids {
			if err := s.emitAuth(ctx, tx, tenantID, uid, "dms.auth.password_expired.v1", map[string]any{
				"user_id":   uid.String(),
				"tenant_id": tenantID.String(),
				"at":        s.clock().Format(time.RFC3339),
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if flagged > 0 {
		PasswordExpiriesPending.WithLabelValues(tenantID.String()).Add(float64(flagged))
	}
	return flagged, nil
}
