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
	"github.com/vaultdms/vaultdms/services/auth/internal/model"
)

// LoginInput is the validated request shape from the handler.
type LoginInput struct {
	Email      string
	Password   string
	TenantSlug string
	IPAddress  string
	UserAgent  string
}

// LoginResult returned on successful login. Exactly one of MFARequired or
// Session will be populated:
//   - MFARequired=true → client prompts for TOTP, submits via MFA/verify with MFASessionToken
//   - Session != nil   → client is fully logged in
type LoginResult struct {
	MFARequired      bool
	MFASessionToken  string // plaintext, short-lived (5 min)
	Session          *CreatedSession
}

// Login validates the credentials, handles rate limiting, branches on MFA.
// All failure paths return ErrInvalidCredentials to avoid leaking user
// existence. Lockout is the only observable difference.
func (s *Service) Login(ctx context.Context, in LoginInput) (*LoginResult, error) {
	email, err := validateEmail(in.Email)
	if err != nil {
		return nil, asInvalidCredentials(err)
	}

	org, err := s.users.FindOrganizationBySlug(ctx, s.pool, in.TenantSlug)
	if err != nil || org == nil {
		return nil, ErrInvalidCredentials
	}

	// Rate limit: check BEFORE bcrypt to avoid giving attackers a free
	// timing signal on user existence.
	locked, err := s.checkLoginLocked(ctx, org.ID, email)
	if err != nil {
		return nil, err
	}
	if locked {
		return nil, ErrAccountLocked
	}

	var user *model.User
	err = database.WithTenantTx(ctx, s.pool, org.ID, func(tx pgx.Tx) error {
		u, err := s.users.GetByEmail(ctx, tx, org.ID, email)
		if err != nil {
			return err // caller maps to invalid creds; won't leak NotFound
		}
		user = u
		return nil
	})
	if err != nil {
		// Increment attempt counter to rate-limit email enumeration too.
		s.incrementLoginAttempt(ctx, org.ID, email)
		_ = s.auditLoginFailedNoTx(ctx, org.ID, email, "unknown_user")
		return nil, ErrInvalidCredentials
	}

	if user.Status != model.StatusActive {
		s.incrementLoginAttempt(ctx, org.ID, email)
		_ = s.auditLoginFailedNoTx(ctx, org.ID, email, "status_not_active")
		return nil, ErrInvalidCredentials
	}

	if !bcryptCompare(user.PasswordHash, in.Password) {
		s.incrementLoginAttempt(ctx, org.ID, email)
		_ = s.auditLoginFailedNoTx(ctx, org.ID, email, "wrong_password")
		return nil, ErrInvalidCredentials
	}

	// Clear attempt counter on success.
	_ = s.rdb.Del(ctx, loginAttemptsKey(org.ID, email)).Err()

	if user.MFAEnabled {
		mfaToken, err := s.issueMFASession(ctx, user.TenantID, user.ID)
		if err != nil {
			return nil, err
		}
		return &LoginResult{MFARequired: true, MFASessionToken: mfaToken}, nil
	}

	// Password-only path: create the session + audit + update last_login.
	created, err := s.finishLogin(ctx, user, "password", in.IPAddress, in.UserAgent)
	if err != nil {
		return nil, err
	}
	return &LoginResult{Session: created}, nil
}

// finishLogin completes a successful authentication by creating a session,
// writing the audit event, and updating last_login_at — all in one TX.
func (s *Service) finishLogin(ctx context.Context, user *model.User, method, ip, ua string) (*CreatedSession, error) {
	var created *CreatedSession
	err := database.WithTenantTx(ctx, s.pool, user.TenantID, func(tx pgx.Tx) error {
		sess, err := s.createSessionInTx(ctx, tx, user, ip, ua)
		if err != nil {
			return err
		}
		created = sess

		if err := s.users.UpdateLastLogin(ctx, tx, user.TenantID, user.ID, s.clock()); err != nil {
			return err
		}
		return s.emitAuth(ctx, tx, user.TenantID, user.ID, "dms.auth.login_success.v1", map[string]any{
			"user_id":    user.ID.String(),
			"tenant_id":  user.TenantID.String(),
			"method":     method,
			"ip":         ip,
			"user_agent": ua,
		})
	})
	return created, err
}

// ---- Rate limiting --------------------------------------------------------

func loginAttemptsKey(tenantID uuid.UUID, email string) string {
	return fmt.Sprintf("login_attempts:%s:%s", tenantID.String(), email)
}

func (s *Service) checkLoginLocked(ctx context.Context, tenantID uuid.UUID, email string) (bool, error) {
	n, err := s.rdb.Get(ctx, loginAttemptsKey(tenantID, email)).Int()
	if err == redis.Nil {
		return false, nil
	}
	if err != nil {
		// Fail open on Redis outage for rate limiting — alternative is a
		// full auth outage, which is worse. Log and proceed.
		s.log.Warn().Err(err).Msg("redis get login_attempts; proceeding without rate limit")
		return false, nil
	}
	return n >= LoginAttemptsMax, nil
}

func (s *Service) incrementLoginAttempt(ctx context.Context, tenantID uuid.UUID, email string) {
	key := loginAttemptsKey(tenantID, email)
	n, err := s.rdb.Incr(ctx, key).Result()
	if err != nil {
		return
	}
	if n == 1 {
		_ = s.rdb.Expire(ctx, key, LoginAttemptsWindow).Err()
	}
}

// auditLoginFailedNoTx audits a failed login WITHOUT a caller-supplied TX.
// We still route through the outbox for event delivery but take our own TX.
func (s *Service) auditLoginFailedNoTx(ctx context.Context, tenantID uuid.UUID, email, reason string) error {
	payload, _ := json.Marshal(map[string]any{
		"tenant_id": tenantID.String(),
		"email":     email,
		"reason":    reason,
		"at":        s.clock().Format(time.RFC3339),
	})
	evt := database.NewOutboxEvent(tenantID, "dms.auth.login_failed.v1", "auth", uuid.New(), payload)

	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return s.outbox.Insert(ctx, tx, evt)
	})
}

// ---- Errors returned over the wire match the opaque sentinels. -----------

var _ = errors.New // keep errors import usage explicit
