package service

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"

	"github.com/aieera/sedoc/pkg/crypto"
	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/auth/internal/model"
	"github.com/aieera/sedoc/services/auth/internal/repository"
)

// MFASetupResult is returned once; client renders the QR code and stores
// recovery codes somewhere safe. After this call MFA is NOT yet enabled —
// user must call ConfirmMFA with a working TOTP code to flip the flag.
type MFASetupResult struct {
	Secret        string   // base32
	QRCodeURI     string   // otpauth://...
	RecoveryCodes []string // plaintext, shown once
}

// recoveryCodeCount is how many recovery codes setup and regeneration issue.
const recoveryCodeCount = 8

// ErrTOTPCodeUsed: the TOTP code was correct but its time-step was already
// used (replay protection, acceptTOTP).
var ErrTOTPCodeUsed = &vdmserr.Error{
	Kind:    vdmserr.KindValidation,
	Code:    "TOTP_CODE_USED",
	Field:   "totp_code",
	Message: "this code was already used; wait for the next code",
}

// errMFAAlreadyOn is SetupMFA's refusal while TOTP is enabled.
var errMFAAlreadyOn = vdmserr.Conflict("two-step verification is already on; turn it off first")

// SetupMFA generates a fresh TOTP secret + 8 recovery codes, stores them
// encrypted (secret: AES-GCM; recovery: bcrypt hashes), and returns the
// plaintext for one-time display. MFA remains disabled until ConfirmMFA.
//
// DEF-01: refused (409) while MFA is enabled. Re-running setup on an
// enabled factor replaced the secret the user's authenticator app holds
// while mfa_enabled stayed true — an instant lockout. Rotating recovery
// codes is RegenerateRecoveryCodes; changing the authenticator means
// turning MFA off first. Re-running setup on a pending (unconfirmed)
// enrolment is still allowed.
func (s *Service) SetupMFA(ctx context.Context, tenantID, userID uuid.UUID, userEmail string) (*MFASetupResult, error) {
	// Refuse before generating anything (bcrypt × 8 is not free).
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		user, err := s.users.GetByID(ctx, tx, tenantID, userID)
		if err != nil {
			return err
		}
		if user.MFAEnabled {
			return errMFAAlreadyOn
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      "SeDoc",
		AccountName: userEmail,
	})
	if err != nil {
		return nil, fmt.Errorf("totp generate: %w", err)
	}
	secret := key.Secret()

	enc, err := s.encryptMFASecret(secret)
	if err != nil {
		return nil, err
	}

	codes, hashes, err := generateRecoveryCodes(recoveryCodeCount)
	if err != nil {
		return nil, err
	}

	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		setErr := s.users.SetMFASecret(ctx, tx, tenantID, userID, enc, hashes)
		if errors.Is(setErr, repository.ErrMFAEnabled) {
			// Confirmed (another tab) since the check above.
			return errMFAAlreadyOn
		}
		return setErr
	})
	if err != nil {
		return nil, err
	}

	return &MFASetupResult{
		Secret:        secret,
		QRCodeURI:     key.URL(),
		RecoveryCodes: codes,
	}, nil
}

// RegenerateRecoveryCodes replaces the user's recovery codes and returns the
// new plaintext codes for one-time display (DEF-01). The caller proves
// possession of the CURRENT authenticator with a TOTP code (single-use, the
// same replay guard as sign-in). A wrong code is a 400 INVALID_ARGUMENT on
// totp_code; a correct code from an already-used step is a 400
// TOTP_CODE_USED (ErrTOTPCodeUsed) on totp_code. Only the recovery-code hashes change: the
// TOTP secret and mfa_enabled are untouched, so the authenticator app keeps
// working and every previous recovery code stops working.
func (s *Service) RegenerateRecoveryCodes(ctx context.Context, tenantID, userID uuid.UUID, totpCode string) ([]string, error) {
	if strings.TrimSpace(totpCode) == "" {
		return nil, vdmserr.Validation("totp_code", "totp_code required")
	}
	var codes []string
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		user, err := s.users.GetByID(ctx, tx, tenantID, userID)
		if err != nil {
			return err
		}
		if !user.MFAEnabled || user.MFASecretEnc == "" {
			return vdmserr.Conflict("two-step verification is not on")
		}
		// The seal helpers bound their own KMS call (kmsCallTimeout), as at
		// every other call site.
		secret, err := s.decryptMFASecret(user.MFASecretEnc) //nolint:contextcheck // see above
		if err != nil {
			return err
		}
		res, err := s.checkTOTP(ctx, tenantID, userID, secret, totpCode)
		if err != nil {
			return err
		}
		switch res {
		case totpAccepted:
		case totpReplayed:
			// Right code, used step — typically the step just used to
			// sign in. Its own error so the page can say "wait for the
			// next code" (review M-6).
			return ErrTOTPCodeUsed
		case totpInvalid:
			return vdmserr.Validation("totp_code", "invalid code")
		}
		plain, hashes, err := generateRecoveryCodes(recoveryCodeCount)
		if err != nil {
			return err
		}
		if err := s.users.SetRecoveryHashes(ctx, tx, tenantID, userID, hashes); err != nil {
			return err
		}
		if err := s.emitMFAChanged(ctx, tx, tenantID, userID, "recovery_codes_regenerated", "totp"); err != nil {
			return err
		}
		codes = plain
		return nil
	})
	if err != nil {
		return nil, err
	}
	return codes, nil
}

// ConfirmMFA validates a TOTP code against the pending secret and — if
// correct — flips mfa_enabled = true. Emits dms.auth.mfa_enabled.v1.
func (s *Service) ConfirmMFA(ctx context.Context, tenantID, userID uuid.UUID, code string) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		user, err := s.users.GetByID(ctx, tx, tenantID, userID)
		if err != nil {
			return err
		}
		if user.MFASecretEnc == "" {
			return vdmserr.Conflict("mfa not set up")
		}
		secret, err := s.decryptMFASecret(user.MFASecretEnc)
		if err != nil {
			return err
		}
		ok, err := s.acceptTOTP(ctx, tenantID, userID, secret, code)
		if err != nil {
			return err
		}
		if !ok {
			return vdmserr.Validation("totp_code", "invalid code")
		}
		if err := s.users.SetMFAEnabled(ctx, tx, tenantID, userID, true); err != nil {
			return err
		}
		if err := s.emitMFAChanged(ctx, tx, tenantID, userID, "enrolled", "totp"); err != nil {
			return err
		}
		return s.emitAuth(ctx, tx, tenantID, userID, "dms.auth.mfa_enabled.v1", map[string]any{
			"user_id":   userID.String(),
			"tenant_id": tenantID.String(),
		})
	})
}

// DisableMFA accepts either a TOTP code or a recovery code. On success it
// clears all MFA fields and emits dms.auth.mfa_disabled.v1.
func (s *Service) DisableMFA(ctx context.Context, tenantID, userID uuid.UUID, totpCode, recoveryCode string) error {
	if totpCode == "" && recoveryCode == "" {
		return vdmserr.Validation("totp_code", "totp_code or recovery_code required")
	}
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		user, err := s.users.GetByID(ctx, tx, tenantID, userID)
		if err != nil {
			return err
		}
		if !user.MFAEnabled {
			return vdmserr.Conflict("mfa not enabled")
		}
		ok := false
		if totpCode != "" {
			secret, err := s.decryptMFASecret(user.MFASecretEnc)
			if err != nil {
				return err
			}
			if ok, err = s.acceptTOTP(ctx, tenantID, userID, secret, totpCode); err != nil {
				return err
			}
		}
		if !ok && recoveryCode != "" {
			ok = consumeRecoveryCode(user.MFARecoveryHashes, recoveryCode) != ""
		}
		if !ok {
			return vdmserr.Validation("code", "invalid code")
		}
		if err := s.users.ClearMFA(ctx, tx, tenantID, userID); err != nil {
			return err
		}
		if err := s.emitMFAChanged(ctx, tx, tenantID, userID, "disabled", "totp"); err != nil {
			return err
		}
		return s.emitAuth(ctx, tx, tenantID, userID, "dms.auth.mfa_disabled.v1", map[string]any{
			"user_id":   userID.String(),
			"tenant_id": tenantID.String(),
		})
	})
}

// emitMFAChanged writes dms.user.mfa_changed.v1 (contract event, parallel to
// dms.user.mfa_reset.v1) inside the caller's txn. Counts the user's remaining
// factors — TOTP (users.mfa_enabled) + passkeys + other methods — so audit/SIEM
// can see whether the user still has MFA. action: enrolled|removed|disabled|recovery_codes_regenerated.
func (s *Service) emitMFAChanged(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID, action, method string) error {
	var totpOn bool
	var passkeys, methods int
	// Best-effort counts (a schema variance must not fail the factor change).
	_ = tx.QueryRow(ctx, `SELECT COALESCE(mfa_enabled,false) FROM users WHERE tenant_id=$1 AND id=$2`, tenantID, userID).Scan(&totpOn)
	_ = tx.QueryRow(ctx, `SELECT count(*) FROM webauthn_credentials WHERE tenant_id=$1 AND user_id=$2`, tenantID, userID).Scan(&passkeys)
	_ = tx.QueryRow(ctx, `SELECT count(*) FROM user_mfa_methods WHERE tenant_id=$1 AND user_id=$2 AND status='active'`, tenantID, userID).Scan(&methods)
	count := passkeys + methods
	if totpOn {
		count++
	}
	payload, _ := json.Marshal(map[string]any{
		"user_id":                userID.String(),
		"tenant_id":              tenantID.String(),
		"action":                 action, // enrolled | removed | disabled | recovery_codes_regenerated
		"method":                 method, // totp | passkey | email | sms | push
		"remaining_factor_count": count,
	})
	evt := database.NewOutboxEvent(tenantID, "dms.user.mfa_changed.v1", "user", userID, payload)
	return s.outbox.Insert(ctx, tx, evt)
}

// ---- MFA challenge flow (after password succeeds for an MFA-enabled user)

// issueMFASession mints a short-lived token that the client returns on
// MFA/verify along with the TOTP code. Stored in Redis keyed by SHA-256(token).
func (s *Service) issueMFASession(ctx context.Context, tenantID, userID uuid.UUID) (string, error) {
	plain, err := randomToken(32)
	if err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]string{
		"user_id":   userID.String(),
		"tenant_id": tenantID.String(),
	})
	hash := sha256Hex(plain)
	pipe := s.rdb.Pipeline()
	pipe.Set(ctx, mfaSessionKey(tenantID, hash), body, MFASessionTTL)
	pipe.Set(ctx, mfaTenantKey(hash), tenantID.String(), MFASessionTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return "", fmt.Errorf("redis set mfa_session: %w", err)
	}
	return plain, nil
}

func mfaSessionKey(tenantID uuid.UUID, hash string) string {
	return "mfa_session:" + tenantID.String() + ":" + hash
}
func mfaTenantKey(hash string) string { return "mfa_tenant:" + hash }
func mfaAttemptsKey(tenantID uuid.UUID, hash string) string {
	return "mfa_attempts:" + tenantID.String() + ":" + hash
}

// resolveMFATenant returns the tenant ID associated with an MFA session
// token hash, or the zero UUID if no session exists.
func (s *Service) resolveMFATenant(ctx context.Context, hash string) uuid.UUID {
	tenantStr, err := s.rdb.Get(ctx, mfaTenantKey(hash)).Result()
	if err != nil {
		return uuid.Nil
	}
	tid, err := uuid.Parse(tenantStr)
	if err != nil {
		return uuid.Nil
	}
	return tid
}

// deleteMFASession removes the session + attempts + tenant lookup entries.
func (s *Service) deleteMFASession(ctx context.Context, hash string) {
	tid := s.resolveMFATenant(ctx, hash)
	pipe := s.rdb.Pipeline()
	pipe.Del(ctx, mfaTenantKey(hash))
	if tid != uuid.Nil {
		pipe.Del(ctx, mfaSessionKey(tid, hash))
		pipe.Del(ctx, mfaAttemptsKey(tid, hash))
	}
	_, _ = pipe.Exec(ctx)
}

// VerifyMFA completes the second factor. Returns a CreatedSession on success.
// Increments an attempt counter; after MFAAttemptsMax failures, the MFA
// session is destroyed so the user must re-authenticate from scratch.
func (s *Service) VerifyMFA(ctx context.Context, mfaSessionToken, totpCode, ip, ua string) (*CreatedSession, error) {
	if mfaSessionToken == "" || totpCode == "" {
		return nil, vdmserr.ErrUnauthorized
	}
	hash := sha256Hex(mfaSessionToken)

	tenantID := s.resolveMFATenant(ctx, hash)
	if tenantID == uuid.Nil {
		return nil, vdmserr.ErrUnauthorized
	}
	body, err := s.rdb.Get(ctx, mfaSessionKey(tenantID, hash)).Bytes()
	if err == redis.Nil {
		return nil, vdmserr.ErrUnauthorized
	}
	if err != nil {
		return nil, fmt.Errorf("redis get mfa_session: %w", err)
	}
	var ref struct {
		UserID   string `json:"user_id"`
		TenantID string `json:"tenant_id"`
	}
	if err := json.Unmarshal(body, &ref); err != nil {
		return nil, vdmserr.ErrUnauthorized
	}
	userID, err2 := uuid.Parse(ref.UserID)
	if err2 != nil {
		return nil, vdmserr.ErrUnauthorized
	}

	var user *model.User
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		u, err := s.users.GetByID(ctx, tx, tenantID, userID)
		if err != nil {
			return err
		}
		user = u
		return nil
	})
	if err != nil {
		return nil, vdmserr.ErrUnauthorized
	}

	secret, err := s.decryptMFASecret(user.MFASecretEnc)
	if err != nil {
		return nil, err
	}
	ok, err := s.acceptTOTP(ctx, tenantID, userID, secret, totpCode)
	if err != nil {
		return nil, err
	}
	if !ok {
		s.handleMFAFailure(ctx, tenantID, hash)
		return nil, vdmserr.ErrUnauthorized
	}

	// One-time: consume the MFA session before session creation so a replay
	// cannot yield two real sessions.
	s.deleteMFASession(ctx, hash)

	return s.finishLogin(ctx, user, "totp", ip, ua)
}

// VerifyRecoveryCode is the lost-device fallback. The recovery code is
// consumed (one-time use). On success, creates a session.
func (s *Service) VerifyRecoveryCode(ctx context.Context, mfaSessionToken, recoveryCode, ip, ua string) (*CreatedSession, error) {
	if mfaSessionToken == "" || recoveryCode == "" {
		return nil, vdmserr.ErrUnauthorized
	}
	hash := sha256Hex(mfaSessionToken)

	tenantID := s.resolveMFATenant(ctx, hash)
	if tenantID == uuid.Nil {
		return nil, vdmserr.ErrUnauthorized
	}
	body, err := s.rdb.Get(ctx, mfaSessionKey(tenantID, hash)).Bytes()
	if err != nil {
		return nil, vdmserr.ErrUnauthorized
	}
	var ref struct{ UserID, TenantID string }
	_ = json.Unmarshal(body, &ref)
	userID, _ := uuid.Parse(ref.UserID)
	if userID == uuid.Nil {
		return nil, vdmserr.ErrUnauthorized
	}

	var (
		user        *model.User
		matchedHash string
	)
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		u, err := s.users.GetByID(ctx, tx, tenantID, userID)
		if err != nil {
			return err
		}
		user = u
		matchedHash = consumeRecoveryCode(u.MFARecoveryHashes, recoveryCode)
		if matchedHash == "" {
			return vdmserr.ErrUnauthorized
		}
		return s.users.ConsumeRecoveryHash(ctx, tx, tenantID, userID, matchedHash)
	})
	if err != nil {
		// Count the failed attempt (increments the per-session counter and
		// tears down the MFA session at the cap) — the TOTP path does this, the
		// recovery-code path previously did not, so recovery-code guessing was
		// unbounded within a session.
		s.handleMFAFailure(ctx, tenantID, hash)
		return nil, vdmserr.ErrUnauthorized
	}

	s.deleteMFASession(ctx, hash)
	created, err := s.finishLogin(ctx, user, "recovery_code", ip, ua)
	if err != nil {
		return nil, err
	}

	// Warn if the user is running low on codes.
	if len(user.MFARecoveryHashes)-1 <= 2 {
		s.log.Warn().Str("user_id", user.ID.String()).Int("remaining", len(user.MFARecoveryHashes)-1).
			Msg("user has low recovery code count; encourage regenerate")
	}
	return created, nil
}

func (s *Service) handleMFAFailure(ctx context.Context, tenantID uuid.UUID, mfaTokenHash string) {
	key := mfaAttemptsKey(tenantID, mfaTokenHash)
	n, err := s.rdb.Incr(ctx, key).Result()
	if err != nil {
		return
	}
	if n == 1 {
		_ = s.rdb.Expire(ctx, key, MFASessionTTL).Err()
	}
	if n >= int64(MFAAttemptsMax) {
		s.deleteMFASession(ctx, mfaTokenHash)
	}
}

// ---- TOTP single use (L-2) -------------------------------------------------

// totpReplayWindow covers the whole ±1-step validation window (3 × 30 s):
// the last accepted step is remembered at least that long.
const totpReplayWindow = 90 * time.Second

// acceptTOTPStep records step as the user's last accepted TOTP step unless
// that step (or a later one) was already used. Returns 1 when accepted.
var acceptTOTPStep = redis.NewScript(`
local cur = redis.call('GET', KEYS[1])
if cur and tonumber(cur) >= tonumber(ARGV[1]) then
  return 0
end
redis.call('SET', KEYS[1], ARGV[1], 'EX', tonumber(ARGV[2]))
return 1
`)

func totpStepKey(tenantID, userID uuid.UUID) string {
	return "mfa_totp_step:" + tenantID.String() + ":" + userID.String()
}

// totpMatchStep returns the 30 s time-step code matches within the same
// ±1-step skew totp.Validate allows, or -1.
func totpMatchStep(secret, code string, now time.Time) int64 {
	opts := totp.ValidateOpts{Period: 30, Skew: 0, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1}
	for _, off := range []int64{-1, 0, 1} {
		at := now.Add(time.Duration(off) * 30 * time.Second)
		want, err := totp.GenerateCodeCustom(secret, at, opts)
		if err == nil && subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return at.Unix() / 30
		}
	}
	return -1
}

// totpCheck is checkTOTP's verdict.
type totpCheck int

const (
	totpInvalid  totpCheck = iota // matches no step in the skew window
	totpReplayed                  // matches a step already used (or older)
	totpAccepted                  // matches an unused step, now burned
)

// acceptTOTP validates code and burns its time-step for the user, so a
// code (or any code from an earlier step) cannot be replayed within the
// skew window — RFC 6238 §5.2. Fails closed on a Redis error.
func (s *Service) acceptTOTP(ctx context.Context, tenantID, userID uuid.UUID, secret, code string) (bool, error) {
	res, err := s.checkTOTP(ctx, tenantID, userID, secret, code)
	return res == totpAccepted, err
}

// checkTOTP is acceptTOTP with the refusal split: a code that matches no
// step (totpInvalid) versus a correct code whose step was already used
// (totpReplayed). Used where the caller is already signed in and the
// difference changes what they should do (the recovery-codes page: "wait
// for the next code"); sign-in keeps its one generic refusal via
// acceptTOTP. Telling a replay apart reveals nothing usable: the step is
// burned either way.
func (s *Service) checkTOTP(ctx context.Context, tenantID, userID uuid.UUID, secret, code string) (totpCheck, error) {
	step := totpMatchStep(secret, strings.TrimSpace(code), s.clock())
	if step < 0 {
		return totpInvalid, nil
	}
	n, err := acceptTOTPStep.Run(ctx, s.rdb, []string{totpStepKey(tenantID, userID)},
		step, int(totpReplayWindow.Seconds())).Int()
	if err != nil {
		return totpInvalid, fmt.Errorf("totp replay guard: %w", err)
	}
	if n != 1 {
		return totpReplayed, nil
	}
	return totpAccepted, nil
}

// ---- MFA secret encryption ------------------------------------------------
//
// Two at-rest formats (shared by MFA secrets, LDAP bind passwords and push
// ack keys):
//
//	legacy  base64(nonce || ciphertext)            — AES-256-GCM under the
//	        static local KEK (SEDOC_LOCAL_KEK).
//	v2      "v2:" base64(wrappedDEK) ":" base64(nonce || ciphertext)
//	        — envelope: a fresh DEK per secret from the KeyManager, stored
//	        wrapped next to the ciphertext so it can be unwrapped later.
//
// With a KeyManager configured new secrets are sealed as v2; legacy rows
// keep decrypting with the local KEK. (L-1: the old currentKEK() asked
// the KMS for a NEW data key on every call and discarded the wrapped
// form, so nothing it sealed could ever be opened again.)

const (
	sealV2Prefix = "v2:"
	// mfaKEKID names the KEK the auth service's at-rest secrets are
	// wrapped under.
	mfaKEKID       = "vaultdms-auth-mfa"
	kmsCallTimeout = 10 * time.Second
)

// sealSecret encrypts plain in the current format (v2 with a KMS, legacy
// otherwise).
func (s *Service) sealSecret(plain []byte) (string, error) {
	if s.kms != nil {
		kmsCtx, cancel := context.WithTimeout(context.Background(), kmsCallTimeout)
		defer cancel()
		dek, wrapped, err := s.kms.GenerateDataKey(kmsCtx, mfaKEKID)
		if err != nil {
			return "", fmt.Errorf("kms generate data key: %w", err)
		}
		ct, nonce, err := crypto.EncryptData(plain, dek)
		if err != nil {
			return "", err
		}
		return sealV2Prefix + base64.StdEncoding.EncodeToString(wrapped) + ":" +
			base64.StdEncoding.EncodeToString(append(append([]byte{}, nonce...), ct...)), nil
	}
	if len(s.localKek) != crypto.DEKSize {
		return "", fmt.Errorf("no secret encryption key available (%d bytes)", len(s.localKek))
	}
	ct, nonce, err := crypto.EncryptData(plain, s.localKek)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(append(append([]byte{}, nonce...), ct...)), nil
}

// unsealSecret opens either format.
func (s *Service) unsealSecret(encoded string) ([]byte, error) {
	if rest, ok := strings.CutPrefix(encoded, sealV2Prefix); ok {
		wrappedB64, blobB64, found := strings.Cut(rest, ":")
		if !found {
			return nil, fmt.Errorf("sealed secret: malformed v2 envelope")
		}
		if s.kms == nil {
			return nil, fmt.Errorf("sealed secret: v2 envelope needs a KMS")
		}
		wrapped, err := base64.StdEncoding.DecodeString(wrappedB64)
		if err != nil {
			return nil, fmt.Errorf("sealed secret: wrapped dek b64: %w", err)
		}
		kmsCtx, cancel := context.WithTimeout(context.Background(), kmsCallTimeout)
		defer cancel()
		dek, err := s.kms.DecryptDataKey(kmsCtx, mfaKEKID, wrapped)
		if err != nil {
			return nil, fmt.Errorf("kms decrypt data key: %w", err)
		}
		return openNonceCiphertext(blobB64, dek)
	}
	if len(s.localKek) != crypto.DEKSize {
		return nil, fmt.Errorf("no secret encryption key available (%d bytes)", len(s.localKek))
	}
	return openNonceCiphertext(encoded, s.localKek)
}

func openNonceCiphertext(b64 string, key []byte) ([]byte, error) {
	buf, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("sealed secret b64: %w", err)
	}
	if len(buf) < crypto.NonceSize+1 {
		return nil, fmt.Errorf("sealed secret too short")
	}
	pt, err := crypto.DecryptData(buf[crypto.NonceSize:], buf[:crypto.NonceSize], key)
	if err != nil {
		return nil, fmt.Errorf("sealed secret decrypt: %w", err)
	}
	return pt, nil
}

// encryptMFASecret seals a TOTP secret for users.mfa_secret_encrypted.
func (s *Service) encryptMFASecret(plain string) (string, error) {
	return s.sealSecret([]byte(plain))
}

func (s *Service) decryptMFASecret(encoded string) (string, error) {
	if encoded == "" {
		return "", vdmserr.Conflict("mfa secret missing")
	}
	pt, err := s.unsealSecret(encoded)
	if err != nil {
		return "", fmt.Errorf("mfa: %w", err)
	}
	return string(pt), nil
}

// ---- Recovery codes -------------------------------------------------------

// generateRecoveryCodes returns `n` uppercase alphanumeric codes (8 chars
// each) and their bcrypt-10 hashes.
func generateRecoveryCodes(n int) (plain []string, hashes []string, err error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // drop confusable chars
	const length = 8
	plain = make([]string, n)
	hashes = make([]string, n)
	for i := 0; i < n; i++ {
		buf := make([]byte, length)
		if _, err = rand.Read(buf); err != nil {
			return nil, nil, err
		}
		var sb strings.Builder
		for j := 0; j < length; j++ {
			sb.WriteByte(alphabet[int(buf[j])%len(alphabet)])
		}
		plain[i] = sb.String()
		h, err := bcrypt.GenerateFromPassword([]byte(plain[i]), 12)
		if err != nil {
			return nil, nil, err
		}
		hashes[i] = string(h)
	}
	return plain, hashes, nil
}

// consumeRecoveryCode attempts to match `code` against the bcrypt hashes in
// the user's recovery_hashes array. Returns the matched hash (for deletion)
// or empty string if no match.
func consumeRecoveryCode(hashes []string, code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	for _, h := range hashes {
		if bcrypt.CompareHashAndPassword([]byte(h), []byte(code)) == nil {
			return h
		}
	}
	return ""
}

var _ = time.Second // kept for future use (attempt-window tunables)
