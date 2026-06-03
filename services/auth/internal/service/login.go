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
	"github.com/aieera/sedoc/services/auth/internal/ldap"
	"github.com/aieera/sedoc/services/auth/internal/model"
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

	// ADR 0062 — try LDAP first when active. The directory is the
	// source of truth; local password is the fallback when the
	// tenant has fallback_to_local on.
	if s.HasActiveLDAP(ctx, org.ID) {
		ldapUsername := emailLocalPart(email)
		ldapRes, err := s.AuthenticateLDAP(ctx, org.ID, ldapUsername, in.Password)
		switch {
		case err == nil:
			_ = s.rdb.Del(ctx, loginAttemptsKey(org.ID, email)).Err()
			created, err := s.FinishLDAPLogin(ctx, ldapRes.User, in.IPAddress, in.UserAgent)
			if err != nil {
				return nil, err
			}
			return &LoginResult{Session: created}, nil
		case errors.Is(err, ldap.ErrInvalidCredentials):
			if !s.FallbackToLocal(ctx, org.ID) {
				s.incrementLoginAttempt(ctx, org.ID, email)
				_ = s.auditLoginFailedNoTx(ctx, org.ID, email, "ldap_invalid")
				return nil, ErrInvalidCredentials
			}
			// fall through to local bcrypt check below
		case errors.Is(err, ErrLDAPDirectoryDown):
			// Directory unreachable — surface as 503 by returning
			// the wrapped error. The handler maps unknown errors to
			// 500; we want explicit 503. Use a typed error so the
			// handler can map it cleanly.
			s.log.Error().Err(err).Msg("ldap directory unreachable on login")
			return nil, vdmserr.Internal("identity provider unreachable")
		default:
			s.log.Error().Err(err).Msg("ldap auth unexpected error")
			return nil, ErrInvalidCredentials
		}
	}

	if !bcryptCompare(user.PasswordHash, in.Password) {
		s.incrementLoginAttempt(ctx, org.ID, email)
		_ = s.auditLoginFailedNoTx(ctx, org.ID, email, "wrong_password")
		return nil, ErrInvalidCredentials
	}

	// Clear attempt counter on success.
	_ = s.rdb.Del(ctx, loginAttemptsKey(org.ID, email)).Err()

	// ADR 0063 — pull tenant MFA policy. When mode=required, every
	// login must clear an MFA challenge OR return ErrMFAEnrollmentRequired
	// when the user has no methods to challenge with (so the admin can
	// enroll them out-of-band). LoadMFAPolicy is best-effort cached;
	// failure here falls open to the legacy mfa_enabled-only behavior.
	mustChallenge := user.MFAEnabled
	if policy, err := s.LoadMFAPolicy(ctx, user.TenantID); err == nil && policy.Mode == "required" {
		methods, _ := s.ListEnrolledMethods(ctx, user.TenantID, user.ID)
		if len(methods) == 0 && !user.MFAEnabled {
			// Do NOT increment the login-attempt counter here. The
			// credential check just succeeded — the failure is purely
			// "tenant policy requires MFA, this user has none
			// enrolled," which is an admin/onboarding state, not a
			// credential-guessing attempt. Audit the event so the
			// admin can see it, but don't rate-limit-lock the user.
			_ = s.auditLoginFailedNoTx(ctx, org.ID, email, "mfa_enrollment_required")
			return nil, ErrMFAEnrollmentRequired
		}
		mustChallenge = true
	}

	if mustChallenge {
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

// ErrMFAEnrollmentRequired — tenant policy is `required` but this
// user has zero methods enrolled. Login refuses; an admin must
// pre-provision a method out-of-band (or temporarily flip the policy
// to `optional`) before the user can sign in.
//
// MUST use Conflict (not Forbidden) so the custom errors.Is in
// pkg/errors — which compares on (Kind, Code) — doesn't match this
// against ErrAccountLocked (also a Forbidden). The collision caused
// every "needs to enrol MFA" response to be re-stamped to 429
// "account temporarily locked" by the handler's special case.
var ErrMFAEnrollmentRequired = vdmserr.Conflict("multi-factor authentication is required by your administrator; ask them to enroll a method on your account")

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
