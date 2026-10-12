// CB-4 — verified enrollment for the email and SMS factors.
//
// EnrollEmail / EnrollSMS create a `pending` user_mfa_methods row and send
// a verification code to the new destination; only a correct code flips
// the row to `active` (the login picker and the OTP senders only ever use
// active rows). Before this, nothing activated a pending row, so the
// factors could never be used — and once activated by hand, an
// unconfigured SMS sender accepted the stub code 000000.
package service

import (
	"context"
	"crypto/subtle"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
)

const (
	enrollOTPTTL      = 10 * time.Minute
	enrollMaxAttempts = 5
)

func enrollOTPKey(m MFAMethod, tenantID, userID uuid.UUID) string {
	return "mfa_enroll_otp:" + string(m) + ":" + tenantID.String() + ":" + userID.String()
}

func enrollAttemptsKey(m MFAMethod, tenantID, userID uuid.UUID) string {
	return "mfa_enroll_attempts:" + string(m) + ":" + tenantID.String() + ":" + userID.String()
}

// pendingDestination returns the destination of the user's pending
// enrollment for m (email address or E.164 phone).
func (s *Service) pendingDestination(ctx context.Context, tenantID, userID uuid.UUID, m MFAMethod) (string, error) {
	col := "email"
	if m == MethodSMS {
		col = "phone_e164"
	}
	var dest string
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT COALESCE(`+col+`, '') FROM user_mfa_methods
			  WHERE tenant_id = $1 AND user_id = $2 AND method = $3 AND status = 'pending'`,
			tenantID, userID, string(m)).Scan(&dest)
	})
	if err != nil || dest == "" {
		return "", vdmserr.Conflict("no pending " + string(m) + " enrollment; enroll first")
	}
	return dest, nil
}

// sendEmailEnrollmentCode issues a 6-digit code for the pending email
// destination (hash kept in Redis for 10 min).
func (s *Service) sendEmailEnrollmentCode(ctx context.Context, tenantID, userID uuid.UUID, dest string) error {
	sender, err := s.emailSenderForTenant(ctx, tenantID.String())
	if err != nil {
		return err
	}
	code, err := generateNumericOTP(6)
	if err != nil {
		return err
	}
	pipe := s.rdb.Pipeline()
	pipe.Set(ctx, enrollOTPKey(MethodEmail, tenantID, userID), sha256Hex(code), enrollOTPTTL)
	pipe.Del(ctx, enrollAttemptsKey(MethodEmail, tenantID, userID))
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("redis enroll otp: %w", err)
	}
	return sender.SendCode(ctx, dest, code)
}

// VerifyEmailEnrollment checks the code sent at EnrollEmail and activates
// the pending email factor. Five wrong codes burn the code.
func (s *Service) VerifyEmailEnrollment(ctx context.Context, tenantID, userID uuid.UUID, code string) error {
	if _, err := s.pendingDestination(ctx, tenantID, userID, MethodEmail); err != nil {
		return err
	}
	stored, err := s.rdb.Get(ctx, enrollOTPKey(MethodEmail, tenantID, userID)).Result()
	if err == redis.Nil {
		return vdmserr.Validation("code", "code expired; enroll again to get a new one")
	}
	if err != nil {
		return fmt.Errorf("redis enroll otp: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(sha256Hex(code)), []byte(stored)) != 1 {
		s.countEnrollFailure(ctx, MethodEmail, tenantID, userID)
		return vdmserr.Validation("code", "invalid code")
	}
	_ = s.rdb.Del(ctx, enrollOTPKey(MethodEmail, tenantID, userID), enrollAttemptsKey(MethodEmail, tenantID, userID)).Err()
	return s.activateMethod(ctx, tenantID, userID, MethodEmail)
}

// VerifySMSEnrollment checks the code Twilio Verify sent at EnrollSMS and
// activates the pending SMS factor.
func (s *Service) VerifySMSEnrollment(ctx context.Context, tenantID, userID uuid.UUID, code string) error {
	dest, err := s.pendingDestination(ctx, tenantID, userID, MethodSMS)
	if err != nil {
		return err
	}
	if n, _ := s.rdb.Get(ctx, enrollAttemptsKey(MethodSMS, tenantID, userID)).Int(); n >= enrollMaxAttempts {
		return vdmserr.Validation("code", "too many attempts; enroll again to get a new code")
	}
	sender, err := s.smsSenderForTenant(ctx, tenantID.String())
	if err != nil {
		return err
	}
	if err := sender.CheckVerification(ctx, dest, code); err != nil {
		s.countEnrollFailure(ctx, MethodSMS, tenantID, userID)
		return vdmserr.Validation("code", "invalid code")
	}
	_ = s.rdb.Del(ctx, enrollAttemptsKey(MethodSMS, tenantID, userID)).Err()
	return s.activateMethod(ctx, tenantID, userID, MethodSMS)
}

func (s *Service) countEnrollFailure(ctx context.Context, m MFAMethod, tenantID, userID uuid.UUID) {
	key := enrollAttemptsKey(m, tenantID, userID)
	n, err := s.rdb.Incr(ctx, key).Result()
	if err != nil {
		return
	}
	if n == 1 {
		_ = s.rdb.Expire(ctx, key, enrollOTPTTL).Err()
	}
	if n >= enrollMaxAttempts {
		_ = s.rdb.Del(ctx, enrollOTPKey(m, tenantID, userID)).Err()
	}
}

// activateMethod flips a pending factor to active and emits the factor
// change.
func (s *Service) activateMethod(ctx context.Context, tenantID, userID uuid.UUID, m MFAMethod) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE user_mfa_methods
			   SET status = 'active', failed_count = 0, updated_at = now()
			 WHERE tenant_id = $1 AND user_id = $2 AND method = $3 AND status = 'pending'`,
			tenantID, userID, string(m))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return vdmserr.Conflict("no pending " + string(m) + " enrollment")
		}
		return s.emitMFAChanged(ctx, tx, tenantID, userID, "enrolled", string(m))
	})
}

// methodSenderAvailable reports whether the deployment / tenant can
// deliver codes for m. Email/SMS factors are hidden from the picker and
// refused at enrollment otherwise.
func (s *Service) methodSenderAvailable(ctx context.Context, tenantID uuid.UUID, m MFAMethod) bool {
	switch m {
	case MethodSMS:
		sender, err := s.smsSenderForTenant(ctx, tenantID.String())
		return err == nil && sender.Available()
	case MethodEmail:
		sender, err := s.emailSenderForTenant(ctx, tenantID.String())
		return err == nil && sender.Available()
	}
	return true
}
