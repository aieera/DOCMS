// Per-tenant Twilio + SMTP credential resolvers for MFA.
//
// MFADeps holds the env-mode senders built at boot. When a tenant has
// saved their own credentials via /admin/integrations (Notifications
// tab), we build a SHORT-LIVED sender per-call using those creds and
// use it instead. The env-mode sender is the fallback when no DB row
// exists, matching the eSign per-tenant pattern.
package service

import (
	"context"
	"time"

	"github.com/vaultdms/vaultdms/pkg/esign"
	"github.com/vaultdms/vaultdms/pkg/notifications"
	"github.com/vaultdms/vaultdms/services/auth/internal/repository"
)

// SetNotifRepo wires the auth repository so the service can read the
// per-tenant Twilio + SMTP config tables (and write to tenant_twilio_configs).
// Called from main.go after Service construction. Nil disables the
// per-tenant path; env-mode senders remain the only fallback.
func (s *Service) SetNotifRepo(r *repository.Repository) { s.notifRepo = r }

// SetNotifSealKey installs the SMTP-password seal key. MUST match the
// notification service's deriveSMTPSealKey so a row written here can
// be unsealed there (and vice versa for Twilio auth_token, which is
// auth-only so the rest of the auth service still uses s.localKek).
func (s *Service) SetNotifSealKey(k []byte) { s.notifSealKey = k }

// smsSenderForTenant returns the SMSSender that should be used to send
// an OTP for tenantID. DB row wins; falls back to s.mfa.SMS (env).
// Returns ErrMethodUnavailable when neither source has credentials.
func (s *Service) smsSenderForTenant(ctx context.Context, tenantID string) (*notifications.SMSSender, error) {
	if s.notifRepo != nil {
		row, err := s.notifRepo.GetTwilioConfig(ctx, tenantID)
		if err == nil && row != nil {
			token, err := esign.UnsealString(row.AuthTokenSealed, s.localKek)
			if err != nil {
				return nil, err
			}
			return notifications.NewSMSSender(notifications.SMSConfig{
				AccountSID:       row.AccountSID,
				AuthToken:        token,
				VerifyServiceSID: row.VerifyServiceSID,
				HTTPTimeout:      8 * time.Second,
			}), nil
		}
	}
	if s.mfa.SMS == nil {
		return nil, ErrMethodUnavailable
	}
	return s.mfa.SMS, nil
}

// emailSenderForTenant returns the EmailOTPSender for tenantID.
// DB tenant_smtp_configs row wins; falls back to s.mfa.Email (env).
func (s *Service) emailSenderForTenant(ctx context.Context, tenantID string) (*notifications.EmailOTPSender, error) {
	if s.notifRepo != nil {
		row, err := s.notifRepo.GetSMTPConfig(ctx, tenantID)
		if err == nil && row != nil {
			password, err := esign.UnsealString(row.PasswordSealed, s.notifSealKey)
			if err != nil {
				return nil, err
			}
			return notifications.NewEmailOTPSender(notifications.EmailOTPConfig{
				Host:          row.Host,
				Port:          row.Port,
				Username:      row.Username,
				Password:      password,
				From:          row.FromAddr,
				SubjectPrefix: "[VaultDMS]",
			}), nil
		}
	}
	if s.mfa.Email == nil {
		return nil, ErrMethodUnavailable
	}
	return s.mfa.Email, nil
}
