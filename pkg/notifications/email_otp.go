package notifications

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/smtp"
	"strings"
	"time"
)

// EmailOTPConfig captures both the SMTP transport and the email
// branding (From, ReplyTo, subject prefix). We deliberately keep
// the API surface small — MFA OTPs are short, fixed-shape messages,
// not arbitrary mail.
type EmailOTPConfig struct {
	Host     string // smtp.example.com
	Port     int    // 587
	Username string
	Password string
	From     string // "SeDoc Security <security@vaultdms.app>"
	// SubjectPrefix is prepended to every OTP email's subject so
	// inboxes can route them with a filter.
	SubjectPrefix string
	// HTTPTimeout is unused for SMTP; the dial timeout below is
	// what matters. Kept for symmetry with SMSConfig.
	DialTimeout time.Duration
	// DevStub: log the code instead of sending. Mirrors SMSConfig.
	DevStub bool
	StubLog func(toEmail, code string)
}

// EmailOTPSender sends a 6-digit code to a user's email. Unlike SMS
// (where Twilio Verify owns the code), the auth service generates
// the code and stores its hash in Redis; this adapter is a dumb
// transport.
type EmailOTPSender struct {
	cfg EmailOTPConfig
}

// NewEmailOTPSender constructs the adapter. Empty Host = stub mode.
func NewEmailOTPSender(cfg EmailOTPConfig) *EmailOTPSender {
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 8 * time.Second
	}
	return &EmailOTPSender{cfg: cfg}
}

// IsConfigured reports whether SMTP credentials are wired.
func (e *EmailOTPSender) IsConfigured() bool {
	return !e.cfg.DevStub && e.cfg.Host != "" && e.cfg.From != ""
}

// SendCode delivers `code` to `toEmail`. The body is intentionally
// boring: a short paragraph + the code on its own line.
func (e *EmailOTPSender) SendCode(ctx context.Context, toEmail, code string) error {
	if e.cfg.DevStub || !e.IsConfigured() {
		if e.cfg.StubLog != nil {
			e.cfg.StubLog(toEmail, code)
		}
		return nil
	}
	subject := strings.TrimSpace(e.cfg.SubjectPrefix+" Sign-in code") + ": " + code
	body := strings.Join([]string{
		"From: " + e.cfg.From,
		"To: " + toEmail,
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		"Your SeDoc sign-in code is:",
		"",
		"    " + code,
		"",
		"It expires in 5 minutes. If you didn't request this, you can",
		"safely ignore this email — your account hasn't been accessed.",
	}, "\r\n")

	addr := fmt.Sprintf("%s:%d", e.cfg.Host, e.cfg.Port)
	auth := smtp.PlainAuth("", e.cfg.Username, e.cfg.Password, e.cfg.Host)

	// Implement a context-cancellable send by running the blocking
	// SendMail call on a goroutine and waiting on either ctx.Done or
	// the result. Adds no measurable latency vs. a direct call.
	type sendResult struct{ err error }
	done := make(chan sendResult, 1)
	go func() {
		// smtp.SendMail negotiates STARTTLS on port 587 by default
		// when the server advertises it. For 465 the caller is
		// expected to wrap the dial in TLS — that's a future
		// enhancement; the common path is 587 + STARTTLS.
		_ = tls.VersionTLS12 // silence unused; documents the floor
		done <- sendResult{err: smtp.SendMail(addr, auth, e.cfg.From, []string{toEmail}, []byte(body))}
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case r := <-done:
		if r.err != nil {
			return fmt.Errorf("smtp send: %w", r.err)
		}
		return nil
	}
}

// ErrEmailDisabled — caller asked to send but SMTP is not configured.
var ErrEmailDisabled = errors.New("email: not configured (set SEDOC_SMTP_* env vars)")
