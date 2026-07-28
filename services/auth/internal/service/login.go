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

	"github.com/aieera/sedoc/pkg/auth"
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
	MFARequired     bool
	MFASessionToken string // plaintext, short-lived (5 min)
	Session         *CreatedSession
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

	// ADR 0063 — tenant MFA policy.
	//   mode=required    → every login must clear an MFA challenge, or return
	//                      ErrMFAEnrollmentRequired when the user has no method.
	//   mode=conditional → challenge users who HAVE MFA enrolled, but don't
	//                      force enrollment on those who don't (previously a
	//                      silent no-op — conditional was never enforced).
	//   anything else    → fall back to the user's own mfa_enabled flag.
	// FAIL CLOSED on a policy-load error: the block previously did `err == nil &&
	// ...`, so a policy-store error silently downgraded to a password-only
	// session, bypassing a required-MFA policy. A rare policy-store outage
	// blocking login is preferable to silently skipping the control.
	mustChallenge := user.MFAEnabled
	policy, perr := s.LoadMFAPolicy(ctx, user.TenantID)
	if perr != nil {
		return nil, fmt.Errorf("mfa policy unavailable; login temporarily blocked: %w", perr)
	}
	switch policy.Mode {
	case "required":
		methods, _ := s.ListEnrolledMethods(ctx, user.TenantID, user.ID)
		if len(methods) == 0 && !user.MFAEnabled {
			// Do NOT increment the login-attempt counter here. The credential
			// check just succeeded — the failure is purely "tenant policy
			// requires MFA, this user has none enrolled," an admin/onboarding
			// state, not a credential-guessing attempt.
			_ = s.auditLoginFailedNoTx(ctx, org.ID, email, "mfa_enrollment_required")
			return nil, ErrMFAEnrollmentRequired
		}
		mustChallenge = true
	case "conditional":
		methods, _ := s.ListEnrolledMethods(ctx, user.TenantID, user.ID)
		if len(methods) > 0 {
			mustChallenge = true
		}
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
// user has zero methods enrolled. Login refuses (403); the user must
// enroll a factor (or an admin pre-provisions one) before signing in.
//
// Forbidden (→ 403) with a DISTINCT code. The custom errors.Is in
// pkg/errors compares on (Kind, Code), so a plain vdmserr.Forbidden
// (Code "FORBIDDEN") would collide with ErrAccountLocked and get
// re-stamped to 429 by the handler's special case. The unique code
// "MFA_ENROLLMENT_REQUIRED" yields the correct 403 without matching
// ErrAccountLocked.
var ErrMFAEnrollmentRequired = &vdmserr.Error{
	Kind:    vdmserr.KindForbidden,
	Code:    "MFA_ENROLLMENT_REQUIRED",
	Message: "multi-factor authentication is required by your administrator; enroll a method to sign in",
}

// finishLogin completes a successful authentication by creating a session,
// writing the audit event, and updating last_login_at — all in one TX.
func (s *Service) finishLogin(ctx context.Context, user *model.User, method, ip, ua string) (*CreatedSession, error) {
	// Attach the just-authenticated user (and client IP) to ctx so the
	// outbox stamps actor_id/actor_name on the login_success audit row.
	// At login time the request ctx has no UserInfo yet — without this
	// the outbox falls back to actor_type="system", which is why login
	// events were mis-attributed to "system" instead of the real user.
	ctx = auth.WithUser(ctx, auth.UserInfo{
		ID:       user.ID,
		TenantID: user.TenantID,
		Email:    user.Email,
		Role:     string(user.Role),
	})
	if ip != "" {
		ctx = auth.WithClientIP(ctx, ip)
	}
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
