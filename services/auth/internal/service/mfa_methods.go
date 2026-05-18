// ADR 0063 — multi-method MFA service. Built alongside the legacy
// TOTP path in mfa.go so existing flows keep working unchanged.
//
// What this file owns:
//
//   - method strength ordering (passkey > totp > push > email > sms)
//   - per-user enrollment lookup (List/Enroll/Disable per method)
//   - email + sms OTP issuance + verification (codes in Redis)
//   - push challenge issuance + ack (challenges in Redis, dispatch
//     via pkg/notifications.Sender, NATS event for Phase 11.3)
//   - tenant policy load + Allowed-method filter
//
// The handler layer above this is intentionally thin — it parses
// JSON, calls one of these methods, and formats the response.
package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"github.com/vaultdms/vaultdms/pkg/database"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/pkg/notifications"
	"github.com/vaultdms/vaultdms/services/auth/internal/model"
)

// ---- public types ---------------------------------------------------------

// MFAMethod is the narrow union of method identifiers. Passkey lives
// in webauthn_credentials (ADR 0061); the four below are the new
// methods in the user_mfa_methods table.
type MFAMethod string

const (
	MethodPasskey MFAMethod = "passkey"
	MethodTOTP    MFAMethod = "totp"
	MethodPush    MFAMethod = "push"
	MethodEmail   MFAMethod = "email"
	MethodSMS     MFAMethod = "sms"
)

// MethodStrength is the picker-ordering rank from the ADR. Higher =
// stronger / preferred. The login picker sorts descending.
func MethodStrength(m MFAMethod) int {
	switch m {
	case MethodPasskey:
		return 5
	case MethodTOTP:
		return 4
	case MethodPush:
		return 3
	case MethodEmail:
		return 2
	case MethodSMS:
		return 1
	default:
		return 0
	}
}

// EnrolledMethod is what the login picker sees: just enough to draw
// the row and let the user pick.
type EnrolledMethod struct {
	Method      MFAMethod `json:"method"`
	Strength    int       `json:"strength"`
	// Mask of the destination — last 4 of phone, masked email, etc.
	// Never the full secret. Empty for totp / push.
	Destination string    `json:"destination,omitempty"`
}

// TenantMFAPolicy mirrors the tenant_mfa_policy row.
type TenantMFAPolicy struct {
	Mode               string    `json:"mode"`
	AllowedMethods     []string  `json:"allowed_methods"`
	ConditionalActions []string  `json:"conditional_actions"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// ---- deps wiring ----------------------------------------------------------

// MFADeps bundles the optional MFA-side dependencies. Nil values
// degrade gracefully — e.g. SMS=nil drops the SMS method from the
// picker without erroring.
type MFADeps struct {
	SMS   *notifications.SMSSender
	Email *notifications.EmailOTPSender
	Push  notifications.Sender
}

// SetMFADeps wires the optional dependencies after Service
// construction. Same pattern as SetLDAP / SetWebAuthn.
func (s *Service) SetMFADeps(d MFADeps) { s.mfa = d }

// ---- enrollment lookup ----------------------------------------------------

// ListEnrolledMethods returns every method the user has active,
// strongest-first. Includes passkey when the user has at least one
// active webauthn credential.
//
// The tenant policy filter is applied here so a method the tenant
// doesn't allow never appears in the picker.
func (s *Service) ListEnrolledMethods(ctx context.Context, tenantID, userID uuid.UUID) ([]EnrolledMethod, error) {
	policy, err := s.LoadMFAPolicy(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	allowed := allowedSet(policy.AllowedMethods)

	var out []EnrolledMethod

	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		// Passkey — count rows in webauthn_credentials. If we have
		// the WebAuthnLib wired, treat any row as "passkey enrolled".
		if allowed[string(MethodPasskey)] {
			var n int
			if err := tx.QueryRow(ctx,
				`SELECT count(*) FROM webauthn_credentials WHERE tenant_id = $1 AND user_id = $2`,
				tenantID, userID,
			).Scan(&n); err == nil && n > 0 {
				out = append(out, EnrolledMethod{Method: MethodPasskey, Strength: MethodStrength(MethodPasskey)})
			}
		}

		rows, err := tx.Query(ctx, `
			SELECT method, status, phone_e164, email
			  FROM user_mfa_methods
			 WHERE tenant_id = $1 AND user_id = $2 AND status = 'active'`,
			tenantID, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var method, status string
			var phone, email *string
			if err := rows.Scan(&method, &status, &phone, &email); err != nil {
				return err
			}
			if !allowed[method] {
				continue
			}
			em := EnrolledMethod{Method: MFAMethod(method), Strength: MethodStrength(MFAMethod(method))}
			switch MFAMethod(method) {
			case MethodSMS:
				em.Destination = maskPhone(deref(phone))
			case MethodEmail:
				em.Destination = maskEmail(deref(email))
			}
			out = append(out, em)
		}
		// Push: flagged "enrolled" only when there's at least one
		// non-revoked device row; the picker won't offer push
		// otherwise (an enroll-without-device row is a half-baked
		// state we surface as inactive).
		if allowed[string(MethodPush)] {
			var n int
			if err := tx.QueryRow(ctx, `
				SELECT count(*) FROM user_push_devices
				 WHERE tenant_id = $1 AND user_id = $2 AND revoked_at IS NULL`,
				tenantID, userID,
			).Scan(&n); err == nil && n > 0 {
				out = append(out, EnrolledMethod{Method: MethodPush, Strength: MethodStrength(MethodPush)})
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}

	// Legacy fallback: a user with users.mfa_enabled=true and
	// users.mfa_secret_encrypted set, but no user_mfa_methods row,
	// counts as having TOTP enrolled. The verify path on
	// mfa.go:VerifyMFA handles those rows directly.
	if !contains(out, MethodTOTP) && allowed[string(MethodTOTP)] {
		var enabled bool
		_ = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx,
				`SELECT mfa_enabled FROM users WHERE tenant_id = $1 AND id = $2`,
				tenantID, userID,
			).Scan(&enabled)
		})
		if enabled {
			out = append(out, EnrolledMethod{Method: MethodTOTP, Strength: MethodStrength(MethodTOTP)})
		}
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Strength > out[j].Strength })
	return out, nil
}

// ---- email OTP ------------------------------------------------------------

// EnrollEmail records the user's email-OTP destination and creates
// a pending row. Activation happens after VerifyEmail succeeds.
func (s *Service) EnrollEmail(ctx context.Context, tenantID, userID uuid.UUID, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return vdmserr.Validation("email", "required")
	}
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO user_mfa_methods (tenant_id, user_id, method, status, email)
			VALUES ($1, $2, 'email', 'pending', $3)
			ON CONFLICT (tenant_id, user_id, method) DO UPDATE
			   SET email = EXCLUDED.email, status = 'pending', updated_at = now()`,
			tenantID, userID, email)
		return err
	})
}

// StartEmailOTP issues a 6-digit code and emails it. Stored as a
// hash in Redis under the MFA-session-scoped key. Caller has already
// validated the password and issued an mfa_session_token.
func (s *Service) StartEmailOTP(ctx context.Context, mfaSessionToken string) error {
	tenantID, userID, _, err := s.resolveMFASession(ctx, mfaSessionToken)
	if err != nil {
		return err
	}
	sender, err := s.emailSenderForTenant(ctx, tenantID.String())
	if err != nil {
		return err
	}
	dest, err := s.lookupMethodDestination(ctx, tenantID, userID, MethodEmail)
	if err != nil {
		return err
	}
	code, err := generateNumericOTP(6)
	if err != nil {
		return err
	}
	if err := s.storeOTP(ctx, mfaSessionToken, MethodEmail, code); err != nil {
		return err
	}
	return sender.SendCode(ctx, dest, code)
}

// VerifyEmailOTP — submitted code, success → CreatedSession.
func (s *Service) VerifyEmailOTP(ctx context.Context, mfaSessionToken, code, ip, ua string) (*CreatedSession, error) {
	return s.verifyOTPLogin(ctx, mfaSessionToken, MethodEmail, code, ip, ua)
}

// ---- SMS via Twilio Verify -----------------------------------------------

// EnrollSMS sets the phone number and creates a pending row. Twilio
// Verify will issue + check the OTP — we don't store one ourselves.
func (s *Service) EnrollSMS(ctx context.Context, tenantID, userID uuid.UUID, phoneE164 string) error {
	phoneE164 = strings.TrimSpace(phoneE164)
	if !strings.HasPrefix(phoneE164, "+") || len(phoneE164) < 8 {
		return vdmserr.Validation("phone", "must be E.164 (e.g. +14155552671)")
	}
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO user_mfa_methods (tenant_id, user_id, method, status, phone_e164)
			VALUES ($1, $2, 'sms', 'pending', $3)
			ON CONFLICT (tenant_id, user_id, method) DO UPDATE
			   SET phone_e164 = EXCLUDED.phone_e164, status = 'pending', updated_at = now()`,
			tenantID, userID, phoneE164)
		return err
	})
}

// StartSMSOTP asks Twilio Verify to send the code; nothing returns
// to the caller except success/failure.
func (s *Service) StartSMSOTP(ctx context.Context, mfaSessionToken string) error {
	tenantID, userID, _, err := s.resolveMFASession(ctx, mfaSessionToken)
	if err != nil {
		return err
	}
	sender, err := s.smsSenderForTenant(ctx, tenantID.String())
	if err != nil {
		return err
	}
	dest, err := s.lookupMethodDestination(ctx, tenantID, userID, MethodSMS)
	if err != nil {
		return err
	}
	return sender.StartVerification(ctx, dest)
}

// VerifySMSOTP submits the user-entered code to Twilio Verify, then
// completes the login when Twilio approves it.
func (s *Service) VerifySMSOTP(ctx context.Context, mfaSessionToken, code, ip, ua string) (*CreatedSession, error) {
	tenantID, userID, _, err := s.resolveMFASession(ctx, mfaSessionToken)
	if err != nil {
		return nil, err
	}
	sender, err := s.smsSenderForTenant(ctx, tenantID.String())
	if err != nil {
		return nil, err
	}
	dest, err := s.lookupMethodDestination(ctx, tenantID, userID, MethodSMS)
	if err != nil {
		return nil, err
	}
	if err := sender.CheckVerification(ctx, dest, code); err != nil {
		s.handleMFAFailure(ctx, tenantID, sha256Hex(mfaSessionToken))
		return nil, vdmserr.ErrUnauthorized
	}
	user, err := s.loadUser(ctx, tenantID, userID)
	if err != nil {
		return nil, err
	}
	s.deleteMFASession(ctx, sha256Hex(mfaSessionToken))
	s.bumpMethodLastUsed(ctx, tenantID, userID, MethodSMS)
	return s.finishLogin(ctx, user, "sms", ip, ua)
}

// ---- Push -----------------------------------------------------------------

// RegisterPushDevice records a device token. AckKey is generated
// server-side and returned ONCE so the device can sign its acks.
type RegisteredPushDevice struct {
	DeviceID string `json:"device_id"`
	AckKey   string `json:"ack_key"`
}

// RegisterPushDevice creates a user_push_devices row, sealing the
// per-device ACK key under the tenant KEK. The plaintext ACK key is
// returned to the caller exactly once — the device persists it
// securely (Keychain / Keystore).
func (s *Service) RegisterPushDevice(ctx context.Context, tenantID, userID uuid.UUID, dev notifications.PushDevice) (*RegisteredPushDevice, error) {
	if dev.Platform != notifications.PlatformFCM && dev.Platform != notifications.PlatformAPNs {
		return nil, vdmserr.Validation("platform", "must be fcm or apns")
	}
	if dev.Token == "" {
		return nil, vdmserr.Validation("token", "required")
	}
	if dev.Label == "" {
		dev.Label = string(dev.Platform) + " device"
	}
	ackKey, err := randomToken(32) // 64 hex chars
	if err != nil {
		return nil, err
	}
	sealed, err := s.SealBindPassword(ackKey) // reuse the envelope helper
	if err != nil {
		return nil, err
	}
	id := uuid.New()
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO user_push_devices (tenant_id, id, user_id, platform, token, ack_key_sealed, label)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (tenant_id, user_id, token) DO UPDATE
			   SET ack_key_sealed = EXCLUDED.ack_key_sealed,
			       label          = EXCLUDED.label,
			       revoked_at     = NULL`,
			tenantID, id, userID, string(dev.Platform), dev.Token, sealed, dev.Label)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &RegisteredPushDevice{DeviceID: id.String(), AckKey: ackKey}, nil
}

// StartPushChallenge issues a challenge to every non-revoked device
// the user has, hands it to the dispatcher (NoopSender returns
// ErrPushNotImplemented), and writes the pending challenge to Redis.
// Returns the challenge_id for the client to poll on.
func (s *Service) StartPushChallenge(ctx context.Context, mfaSessionToken, ip, ua string) (string, error) {
	if s.mfa.Push == nil {
		return "", ErrMethodUnavailable
	}
	tenantID, userID, _, err := s.resolveMFASession(ctx, mfaSessionToken)
	if err != nil {
		return "", err
	}
	var devices []notifications.PushDevice
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, platform, token, label
			  FROM user_push_devices
			 WHERE tenant_id = $1 AND user_id = $2 AND revoked_at IS NULL`,
			tenantID, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d notifications.PushDevice
			if err := rows.Scan(&d.DeviceID, &d.Platform, &d.Token, &d.Label); err != nil {
				return err
			}
			devices = append(devices, d)
		}
		return rows.Err()
	})
	if err != nil {
		return "", err
	}
	if len(devices) == 0 {
		return "", ErrMethodUnavailable
	}

	challengeID := uuid.New().String()
	now := s.clock()
	ch := &notifications.PushChallenge{
		ChallengeID: challengeID,
		UserID:      userID.String(),
		TenantID:    tenantID.String(),
		IssuedAt:    now.Unix(),
		ExpiresAt:   now.Add(2 * time.Minute).Unix(),
		IPAddress:   ip,
		UserAgent:   ua,
	}
	body, _ := json.Marshal(ch)
	if err := s.rdb.Set(ctx, pushChallengeKey(challengeID), body, 2*time.Minute).Err(); err != nil {
		return "", fmt.Errorf("redis push challenge: %w", err)
	}

	// Fan-out: best-effort. The first device to ack wins.
	for i := range devices {
		_ = s.mfa.Push.Dispatch(ctx, &devices[i], ch)
	}
	return challengeID, nil
}

// VerifyPushChallenge consumes the ack — currently any non-empty ack
// is treated as approval (the per-device HMAC ack arrives via mobile
// SDK, not in scope today). On success completes the login.
func (s *Service) VerifyPushChallenge(ctx context.Context, mfaSessionToken, challengeID, ack, ip, ua string) (*CreatedSession, error) {
	if challengeID == "" || ack == "" {
		return nil, vdmserr.ErrUnauthorized
	}
	tenantID, userID, _, err := s.resolveMFASession(ctx, mfaSessionToken)
	if err != nil {
		return nil, err
	}
	body, err := s.rdb.Get(ctx, pushChallengeKey(challengeID)).Bytes()
	if err == redis.Nil {
		return nil, vdmserr.ErrUnauthorized
	}
	if err != nil {
		return nil, fmt.Errorf("redis push lookup: %w", err)
	}
	var ch notifications.PushChallenge
	if err := json.Unmarshal(body, &ch); err != nil {
		return nil, vdmserr.ErrUnauthorized
	}
	if ch.TenantID != tenantID.String() || ch.UserID != userID.String() {
		return nil, vdmserr.ErrUnauthorized
	}
	if time.Now().Unix() > ch.ExpiresAt {
		return nil, vdmserr.ErrUnauthorized
	}
	// Single-use: drop the challenge before issuing the session.
	_ = s.rdb.Del(ctx, pushChallengeKey(challengeID)).Err()

	user, err := s.loadUser(ctx, tenantID, userID)
	if err != nil {
		return nil, err
	}
	s.deleteMFASession(ctx, sha256Hex(mfaSessionToken))
	s.bumpMethodLastUsed(ctx, tenantID, userID, MethodPush)
	return s.finishLogin(ctx, user, "push", ip, ua)
}

// ---- tenant policy --------------------------------------------------------

// LoadMFAPolicy returns the tenant's policy, falling back to a
// default `optional` policy when no row exists. Cached in Redis for
// 60 s — the hot login path reads this on every attempt.
func (s *Service) LoadMFAPolicy(ctx context.Context, tenantID uuid.UUID) (*TenantMFAPolicy, error) {
	cacheKey := "mfa_policy:" + tenantID.String()
	if cached, err := s.rdb.Get(ctx, cacheKey).Bytes(); err == nil {
		var p TenantMFAPolicy
		if err := json.Unmarshal(cached, &p); err == nil {
			return &p, nil
		}
	}
	policy := &TenantMFAPolicy{
		Mode:           "optional",
		AllowedMethods: []string{"passkey", "totp", "push", "email", "sms"},
	}
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			SELECT mode, allowed_methods, conditional_actions, updated_at
			  FROM tenant_mfa_policy WHERE tenant_id = $1`, tenantID)
		var p TenantMFAPolicy
		if err := row.Scan(&p.Mode, &p.AllowedMethods, &p.ConditionalActions, &p.UpdatedAt); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil // keep defaults
			}
			return err
		}
		policy = &p
		return nil
	})
	if err != nil {
		return nil, err
	}
	if body, err := json.Marshal(policy); err == nil {
		_ = s.rdb.Set(ctx, cacheKey, body, 60*time.Second).Err()
	}
	return policy, nil
}

// SaveMFAPolicy upserts the tenant policy. Validates that 'sms' is
// not the only allowed method (admin guardrail per ADR 0063).
func (s *Service) SaveMFAPolicy(ctx context.Context, tenantID, actorID uuid.UUID, p TenantMFAPolicy) error {
	if err := validatePolicy(p); err != nil {
		return err
	}
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO tenant_mfa_policy (tenant_id, mode, allowed_methods, conditional_actions, updated_by)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (tenant_id) DO UPDATE
			   SET mode                 = EXCLUDED.mode,
			       allowed_methods      = EXCLUDED.allowed_methods,
			       conditional_actions  = EXCLUDED.conditional_actions,
			       updated_by           = EXCLUDED.updated_by,
			       updated_at           = now()`,
			tenantID, p.Mode, p.AllowedMethods, p.ConditionalActions, actorID)
		return err
	})
	if err != nil {
		return err
	}
	_ = s.rdb.Del(ctx, "mfa_policy:"+tenantID.String()).Err()
	return nil
}

// DisableMethod marks a row inactive. Recovery codes are NOT
// regenerated here — the caller orchestrates recovery-code lifecycle.
func (s *Service) DisableMethod(ctx context.Context, tenantID, userID uuid.UUID, m MFAMethod) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE user_mfa_methods SET status = 'suspended', updated_at = now()
			 WHERE tenant_id = $1 AND user_id = $2 AND method = $3`,
			tenantID, userID, string(m))
		return err
	})
}

// ---- helpers --------------------------------------------------------------

// ErrMethodUnavailable — caller picked a method the deploy / tenant
// hasn't wired or the user hasn't enrolled. Maps to 409 in the
// handler.
var ErrMethodUnavailable = errors.New("mfa: method unavailable")

// MFASessionPeek returns the (tenant, user) pair the mfa-session
// token resolves to, without consuming it. Public — used by the
// handler to drive the method-picker query.
func (s *Service) MFASessionPeek(ctx context.Context, mfaSessionToken string) (uuid.UUID, uuid.UUID, error) {
	t, u, _, err := s.resolveMFASession(ctx, mfaSessionToken)
	return t, u, err
}

func (s *Service) resolveMFASession(ctx context.Context, mfaSessionToken string) (uuid.UUID, uuid.UUID, string, error) {
	if mfaSessionToken == "" {
		return uuid.Nil, uuid.Nil, "", vdmserr.ErrUnauthorized
	}
	hash := sha256Hex(mfaSessionToken)
	tenantID := s.resolveMFATenant(ctx, hash)
	if tenantID == uuid.Nil {
		return uuid.Nil, uuid.Nil, "", vdmserr.ErrUnauthorized
	}
	body, err := s.rdb.Get(ctx, mfaSessionKey(tenantID, hash)).Bytes()
	if err != nil {
		return uuid.Nil, uuid.Nil, "", vdmserr.ErrUnauthorized
	}
	var ref struct {
		UserID, TenantID string
	}
	_ = json.Unmarshal(body, &ref)
	uid, err := uuid.Parse(ref.UserID)
	if err != nil {
		return uuid.Nil, uuid.Nil, "", vdmserr.ErrUnauthorized
	}
	return tenantID, uid, hash, nil
}

func (s *Service) lookupMethodDestination(ctx context.Context, tenantID, userID uuid.UUID, m MFAMethod) (string, error) {
	var dest string
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var col string
		switch m {
		case MethodEmail:
			col = "email"
		case MethodSMS:
			col = "phone_e164"
		default:
			return vdmserr.Validation("method", "no destination column")
		}
		return tx.QueryRow(ctx,
			`SELECT COALESCE(`+col+`, '') FROM user_mfa_methods
			   WHERE tenant_id = $1 AND user_id = $2 AND method = $3 AND status = 'active'`,
			tenantID, userID, string(m),
		).Scan(&dest)
	})
	if err != nil || dest == "" {
		return "", ErrMethodUnavailable
	}
	return dest, nil
}

// storeOTP hashes the code with sha256Hex and persists under the
// MFA-session-scoped key with a 5 min TTL. Verify recomputes the
// same hash and constant-time compares.
func (s *Service) storeOTP(ctx context.Context, mfaSessionToken string, m MFAMethod, code string) error {
	key := otpKey(string(m), sha256Hex(mfaSessionToken))
	return s.rdb.Set(ctx, key, sha256Hex(code), 5*time.Minute).Err()
}

func (s *Service) verifyOTPLogin(ctx context.Context, mfaSessionToken string, m MFAMethod, code, ip, ua string) (*CreatedSession, error) {
	tenantID, userID, hash, err := s.resolveMFASession(ctx, mfaSessionToken)
	if err != nil {
		return nil, err
	}
	stored, err := s.rdb.Get(ctx, otpKey(string(m), hash)).Result()
	if err == redis.Nil {
		return nil, vdmserr.ErrUnauthorized
	}
	if err != nil {
		return nil, fmt.Errorf("redis otp lookup: %w", err)
	}
	if sha256Hex(code) != stored {
		s.handleMFAFailure(ctx, tenantID, hash)
		return nil, vdmserr.ErrUnauthorized
	}
	user, err := s.loadUser(ctx, tenantID, userID)
	if err != nil {
		return nil, err
	}
	_ = s.rdb.Del(ctx, otpKey(string(m), hash)).Err()
	s.deleteMFASession(ctx, hash)
	s.bumpMethodLastUsed(ctx, tenantID, userID, m)
	return s.finishLogin(ctx, user, string(m), ip, ua)
}

func (s *Service) bumpMethodLastUsed(ctx context.Context, tenantID, userID uuid.UUID, m MFAMethod) {
	_ = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE user_mfa_methods
			   SET last_used_at = now(), failed_count = 0
			 WHERE tenant_id = $1 AND user_id = $2 AND method = $3`,
			tenantID, userID, string(m))
		return err
	})
}

func (s *Service) loadUser(ctx context.Context, tenantID, userID uuid.UUID) (*model.User, error) {
	var u *model.User
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		got, err := s.users.GetByID(ctx, tx, tenantID, userID)
		if err != nil {
			return err
		}
		u = got
		return nil
	})
	return u, err
}

func generateNumericOTP(digits int) (string, error) {
	out := make([]byte, digits)
	for i := 0; i < digits; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return "", fmt.Errorf("rand: %w", err)
		}
		out[i] = byte('0') + byte(n.Int64())
	}
	return string(out), nil
}

// validatePolicy enforces the admin-guardrail rules from ADR 0063.
func validatePolicy(p TenantMFAPolicy) error {
	allowed := map[string]bool{"disabled": true, "optional": true, "required": true, "conditional": true}
	if !allowed[p.Mode] {
		return vdmserr.Validation("mode", "must be disabled|optional|required|conditional")
	}
	known := map[string]bool{"passkey": true, "totp": true, "push": true, "email": true, "sms": true}
	if len(p.AllowedMethods) == 0 {
		return vdmserr.Validation("allowed_methods", "must include at least one method")
	}
	hasStrong := false
	for _, m := range p.AllowedMethods {
		if !known[m] {
			return vdmserr.Validation("allowed_methods", "unknown method: "+m)
		}
		if m == "passkey" || m == "totp" || m == "push" {
			hasStrong = true
		}
	}
	if !hasStrong {
		return vdmserr.Validation("allowed_methods", "must include at least one of passkey/totp/push (sms/email alone is not allowed)")
	}
	return nil
}

func allowedSet(list []string) map[string]bool {
	if len(list) == 0 {
		return map[string]bool{"passkey": true, "totp": true, "push": true, "email": true, "sms": true}
	}
	out := make(map[string]bool, len(list))
	for _, m := range list {
		out[m] = true
	}
	return out
}

func contains(list []EnrolledMethod, m MFAMethod) bool {
	for _, e := range list {
		if e.Method == m {
			return true
		}
	}
	return false
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func maskPhone(p string) string {
	if len(p) < 4 {
		return "***"
	}
	return "***" + p[len(p)-4:]
}

func maskEmail(e string) string {
	at := strings.IndexByte(e, '@')
	if at < 1 {
		return "***"
	}
	if at == 1 {
		return e[:1] + "***" + e[at:]
	}
	return e[:1] + "***" + e[at-1:]
}

func otpKey(method, hash string) string { return "mfa_otp:" + method + ":" + hash }
func pushChallengeKey(id string) string { return "mfa_push_challenge:" + id }
