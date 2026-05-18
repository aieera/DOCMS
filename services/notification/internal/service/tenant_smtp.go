// Per-tenant SMTP resolver for the notification delivery path. Mirrors
// the auth service's mfa_tenant_config.go pattern: DB row wins for
// outbound transactional mail; the boot-time s.smtp is the env-mode
// fallback when no row exists.
package service

import (
	"context"

	"github.com/vaultdms/vaultdms/pkg/esign"
	"github.com/vaultdms/vaultdms/services/notification/internal/repository"
)

// SetTenantSMTPDeps wires the per-tenant SMTP lookup. notifRepo is
// the shared repo with GetSMTPConfig; sealingKey unseals the
// password column. Called from main.go after Service construction.
func (s *Service) SetTenantSMTPDeps(notifRepo TenantSMTPReader, sealingKey []byte) {
	s.tenantSMTP = notifRepo
	s.smtpSealKey = sealingKey
}

// TenantSMTPReader is the read interface the delivery loop needs.
// Implemented by the notification *Repository.
type TenantSMTPReader interface {
	GetSMTPConfig(ctx context.Context, tenantID string) (*repository.SMTPConfig, error)
}

// smtpSenderForTenant returns the SMTPSender to use for tenantID.
// DB row wins; falls back to s.smtp (boot-time env). When neither is
// usable, returns a sender whose Enabled() reports false so the
// delivery loop logs "would send" and moves on.
func (s *Service) smtpSenderForTenant(ctx context.Context, tenantID string) SMTPSender {
	if s.tenantSMTP != nil && tenantID != "" {
		row, err := s.tenantSMTP.GetSMTPConfig(ctx, tenantID)
		if err == nil && row != nil {
			password, err := esign.UnsealString(row.PasswordSealed, s.smtpSealKey)
			if err == nil {
				return NewSMTPSender(SMTPConfig{
					Host:     row.Host,
					Port:     row.Port,
					Username: row.Username,
					Password: password,
					From:     row.FromAddr,
					StartTLS: row.StartTLS,
				})
			}
		}
	}
	return s.smtp
}
