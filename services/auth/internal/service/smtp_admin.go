// Admin-side write path for tenant_smtp_configs. The read path lives
// in mfa_tenant_config.go (emailSenderForTenant). SMTP is shared
// between auth (email OTP) and notification (transactional); auth
// owns the write because the admin UI lives under /api/v1/admin/notifications.
package service

import (
	"context"
	"errors"
	"net/smtp"
	"strconv"
	"time"

	"github.com/aieera/sedoc/pkg/esign"
	"github.com/aieera/sedoc/services/auth/internal/repository"
)

type SMTPConfigPublic struct {
	Host        string    `json:"host"`
	Port        int       `json:"port"`
	Username    string    `json:"username"`
	HasPassword bool      `json:"has_password"`
	FromAddr    string    `json:"from_addr"`
	StartTLS    bool      `json:"starttls"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (s *Service) GetSMTPConfigForAdmin(ctx context.Context, tenantID string) (*SMTPConfigPublic, error) {
	if s.notifRepo == nil {
		return nil, errors.New("notification repo not wired")
	}
	row, err := s.notifRepo.GetSMTPConfig(ctx, tenantID)
	if err != nil || row == nil {
		return nil, err
	}
	return &SMTPConfigPublic{
		Host:        row.Host,
		Port:        row.Port,
		Username:    row.Username,
		HasPassword: row.PasswordSealed != "",
		FromAddr:    row.FromAddr,
		StartTLS:    row.StartTLS,
		UpdatedAt:   row.UpdatedAt,
	}, nil
}

func (s *Service) SaveSMTPConfig(ctx context.Context, tenantID, userID, host string, port int, username, password, fromAddr string, startTLS bool) error {
	if s.notifRepo == nil {
		return errors.New("notification repo not wired")
	}
	if host == "" || fromAddr == "" {
		return errors.New("host and from_addr are required")
	}
	if port <= 0 {
		port = 587
	}
	var sealed string
	if password == "" {
		existing, err := s.notifRepo.GetSMTPConfig(ctx, tenantID)
		if err != nil {
			return err
		}
		if existing != nil {
			sealed = existing.PasswordSealed
		}
	} else {
		var err error
		sealed, err = esign.SealString([]byte(password), s.notifSealKey)
		if err != nil {
			return err
		}
	}
	return s.notifRepo.UpsertSMTPConfig(ctx, &repository.SMTPConfig{
		TenantID:       tenantID,
		Host:           host,
		Port:           port,
		Username:       username,
		PasswordSealed: sealed,
		FromAddr:       fromAddr,
		StartTLS:       startTLS,
	}, userID)
}

func (s *Service) DeleteSMTPConfig(ctx context.Context, tenantID string) error {
	if s.notifRepo == nil {
		return errors.New("notification repo not wired")
	}
	return s.notifRepo.DeleteSMTPConfig(ctx, tenantID)
}

// TestSMTPSend opens an SMTP connection against the saved tenant
// config and tries to send one email to `to`. Returns the raw SMTP
// error verbatim so the admin sees what went wrong.
func (s *Service) TestSMTPSend(ctx context.Context, tenantID, to string) error {
	if s.notifRepo == nil {
		return errors.New("notification repo not wired")
	}
	row, err := s.notifRepo.GetSMTPConfig(ctx, tenantID)
	if err != nil {
		return err
	}
	if row == nil {
		return errors.New("smtp not configured for this tenant; save credentials first")
	}
	password, err := esign.UnsealString(row.PasswordSealed, s.notifSealKey)
	if err != nil {
		return err
	}
	addr := row.Host + ":" + strconv.Itoa(row.Port)
	smtpAuth := smtp.PlainAuth("", row.Username, password, row.Host)
	msg := []byte("From: " + row.FromAddr + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: VaultDMS — SMTP test\r\n" +
		"\r\n" +
		"This message confirms your VaultDMS SMTP credentials work.\r\n")
	if !row.StartTLS {
		smtpAuth = nil // local relays like MailHog refuse PLAIN AUTH
	}
	return smtp.SendMail(addr, smtpAuth, row.FromAddr, []string{to}, msg)
}
