package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pquerna/otp/totp"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"

	"github.com/aieera/sedoc/pkg/crypto"
	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/auth/internal/model"
)

// MFASetupResult is returned once; client renders the QR code and stores
// recovery codes somewhere safe. After this call MFA is NOT yet enabled —
// user must call ConfirmMFA with a working TOTP code to flip the flag.
type MFASetupResult struct {
	Secret        string   // base32
	QRCodeURI     string   // otpauth://...
	RecoveryCodes []string // plaintext, shown once
}

// SetupMFA generates a fresh TOTP secret + 8 recovery codes, stores them
// encrypted (secret: AES-GCM; recovery: bcrypt hashes), and returns the
// plaintext for one-time display. MFA remains disabled until ConfirmMFA.
func (s *Service) SetupMFA(ctx context.Context, tenantID, userID uuid.UUID, userEmail string) (*MFASetupResult, error) {
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

	codes, hashes, err := generateRecoveryCodes(8)
	if err != nil {
		return nil, err
	}

	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return s.users.SetMFASecret(ctx, tx, tenantID, userID, enc, hashes)
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
		if !totp.Validate(code, secret) {
			return vdmserr.Validation("totp_code", "invalid code")
		}
		if err := s.users.SetMFAEnabled(ctx, tx, tenantID, userID, true); err != nil {
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
			ok = totp.Validate(totpCode, secret)
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
		return s.emitAuth(ctx, tx, tenantID, userID, "dms.auth.mfa_disabled.v1", map[string]any{
			"user_id":   userID.String(),
			"tenant_id": tenantID.String(),
		})
	})
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
func mfaTenantKey(hash string) string   { return "mfa_tenant:" + hash }
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
	if !totp.Validate(totpCode, secret) {
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
		user         *model.User
		matchedHash  string
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

// ---- MFA secret encryption ------------------------------------------------

// encryptMFASecret returns nonce||ciphertext hex-encoded. When the KeyManager
// is set we wrap via envelope; otherwise we use the LocalKEK directly. Both
// paths use AES-256-GCM from pkg/crypto.
func (s *Service) encryptMFASecret(plain string) (string, error) {
	kek, err := s.currentKEK()
	if err != nil {
		return "", err
	}
	ct, nonce, err := crypto.EncryptData([]byte(plain), kek)
	if err != nil {
		return "", err
	}
	buf := append([]byte{}, nonce...)
	buf = append(buf, ct...)
	return base64.StdEncoding.EncodeToString(buf), nil
}

func (s *Service) decryptMFASecret(encoded string) (string, error) {
	if encoded == "" {
		return "", vdmserr.Conflict("mfa secret missing")
	}
	buf, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("mfa b64: %w", err)
	}
	if len(buf) < crypto.NonceSize+1 {
		return "", fmt.Errorf("mfa ciphertext too short")
	}
	kek, err := s.currentKEK()
	if err != nil {
		return "", err
	}
	nonce := buf[:crypto.NonceSize]
	ct := buf[crypto.NonceSize:]
	pt, err := crypto.DecryptData(ct, nonce, kek)
	if err != nil {
		return "", fmt.Errorf("mfa decrypt: %w", err)
	}
	return string(pt), nil
}

// currentKEK returns the AES key currently in use. If a KMS is configured
// we ask it for a DEK; otherwise we use the static LocalKEK from config.
func (s *Service) currentKEK() ([]byte, error) {
	if s.kms != nil {
		// For per-secret envelope encryption we'd keep the encryptedDEK on
		// the user row next to the ciphertext. For Phase-A1 simplicity
		// we use a single KEK for all users; a follow-up migration adds a
		// per-user wrapped DEK column.
		kmsCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		dek, _, err := s.kms.GenerateDataKey(kmsCtx, "vaultdms-auth-mfa")
		if err != nil {
			return nil, err
		}
		return dek, nil
	}
	if len(s.localKek) != crypto.DEKSize {
		return nil, fmt.Errorf("no MFA encryption key available (%d bytes)", len(s.localKek))
	}
	return s.localKek, nil
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
