// Wave 12.1 — SMTP transactional email sender.
//
// Uses net/smtp from the standard library. Deliberately narrow:
// one `Send(to, subject, body)` call, plain text only, STARTTLS
// when configured, zero template engine, zero retry loop. The
// retry story lives in the outbox publisher that hands the event
// to this sender — if the send call returns an error we bubble it
// and the publisher requeues.
//
// Supported MTAs: anything speaking standard ESMTP + STARTTLS
// (AWS SES, SendGrid, Mailgun, Postmark, self-hosted Postfix,
// MailHog for dev). Host-specific "use our API instead of SMTP"
// paths (SendGrid Web API, SES v2) are not wired here; SMTP works
// against all of them.
//
// Security invariants:
//   - Plaintext auth happens ONLY over a STARTTLS channel. If
//     StartTLS is true (default) and the server refuses it, the
//     send fails rather than falling back to plaintext. If
//     StartTLS is explicitly false (dev relay like MailHog),
//     unauthenticated plaintext is fine.
//   - We never log the body — DSR tokens flow through here.
package service

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// SMTPConfig is the sender's runtime config. Host == "" means the
// sender is disabled and Send is a no-op that logs only.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	StartTLS bool
}

// SMTPSender abstracts the send path so tests can swap in a stub.
type SMTPSender interface {
	Send(to, subject, body string) error
	Enabled() bool
}

// smtpSender is the net/smtp-backed implementation.
type smtpSender struct {
	cfg SMTPConfig
}

// NewSMTPSender constructs a sender. When cfg.Host == "" the
// sender reports Enabled() == false and Send returns nil without
// doing any I/O — the service's existing "would send email" log
// line remains the only evidence of the call.
func NewSMTPSender(cfg SMTPConfig) SMTPSender {
	return &smtpSender{cfg: cfg}
}

func (s *smtpSender) Enabled() bool { return s.cfg.Host != "" }

// Send delivers one message. Returns nil on success, non-nil on
// any ESMTP, TLS, or network failure.
func (s *smtpSender) Send(to, subject, body string) error {
	if !s.Enabled() {
		return nil
	}
	if strings.TrimSpace(to) == "" {
		return fmt.Errorf("smtp: to required")
	}

	// JoinHostPort brackets IPv6 literals ("[::1]:25"); a plain
	// "%s:%d" would produce an undialable "::1:25".
	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("smtp dial: %w", err)
	}
	defer conn.Close()

	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	defer c.Close()

	if s.cfg.StartTLS {
		tlsCfg := &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	}

	// Auth only after the channel is encrypted (when StartTLS=true)
	// or when the operator explicitly accepted plaintext (StartTLS=false).
	if s.cfg.Username != "" {
		auth := smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}

	if err := c.Mail(s.cfg.From); err != nil {
		return fmt.Errorf("smtp mail: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("smtp rcpt: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	msg := buildRFC5322(s.cfg.From, to, subject, body)
	if _, err := w.Write([]byte(msg)); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp close data: %w", err)
	}
	return c.Quit()
}

// buildRFC5322 assembles a minimal RFC 5322 message. plain text
// body only; if we ever need HTML + multipart, swap for a proper
// builder (jordan-wright/email) — deliberately avoided today to
// keep the dependency surface minimal.
func buildRFC5322(from, to, subject, body string) string {
	var b strings.Builder
	b.WriteString("From: ")
	b.WriteString(from)
	b.WriteString("\r\n")
	b.WriteString("To: ")
	b.WriteString(to)
	b.WriteString("\r\n")
	b.WriteString("Subject: ")
	b.WriteString(subject)
	b.WriteString("\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	return b.String()
}
