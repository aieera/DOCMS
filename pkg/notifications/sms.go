// Package notifications is the auth-side outbound surface for MFA
// challenges (SMS OTP, email OTP, push). It deliberately does NOT
// share state with services/notification — that service is for
// user-facing notifications (mentions, comments, share-links). MFA
// is on the login hot-path and cannot afford a cross-service hop.
//
// The SMS and email OTP adapters run in dev-stub mode ONLY when the
// caller opts in explicitly (DevStub, wired from SEDOC_MFA_DEV_STUB=1) and
// no real credentials are configured: the code is logged instead of sent
// so dev / Playwright runs work without Twilio or SMTP. Without
// credentials and without the flag they refuse (ErrSMSDisabled /
// ErrEmailDisabled) — missing configuration must never degrade into a
// known-code MFA bypass (CB-4).
package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrSMSDisabled is returned when SMSSender is asked to send or check but
// no credentials are configured AND DevStub is false. The login flow
// degrades to "method unavailable" rather than crashing.
var ErrSMSDisabled = errors.New("sms: not configured (set SEDOC_TWILIO_* env vars)")

// SMSConfig is the static config for the platform Twilio account.
// Tenants using their own Twilio subaccount override these via the
// per-tenant secret store; the resolution helper lives in the auth
// service so the adapter stays vendor-shaped, not tenant-shaped.
type SMSConfig struct {
	AccountSID string
	AuthToken  string
	// VerifyServiceSID is the Twilio Verify Service Sid (VAxxxxxxxx).
	// We use Verify rather than direct SMS so Twilio owns the OTP
	// generation, retry / locale localisation, and SS7 fraud
	// mitigation. ADR 0063 §"Twilio Verify".
	VerifyServiceSID string
	// HTTPTimeout caps the outbound POST. Twilio's median latency
	// is ~300 ms; we cap at 8 s to stay clear of any layer-7 idle
	// killer.
	HTTPTimeout time.Duration
	// DevStub opts into log-the-code mode when no real credentials are
	// configured (dev / Playwright). Real credentials always win.
	DevStub bool
	// Logger receives the OTP code in DevStub mode. Non-nil only when
	// the caller cares to capture it (tests).
	StubLog func(phone, code string)
}

// SMSSender sends OTP starts and verifies user-entered codes.
// The adapter holds no state — every call is a POST.
type SMSSender struct {
	cfg    SMSConfig
	client *http.Client
}

// NewSMSSender constructs an adapter. Without credentials it operates in
// stub mode only when cfg.DevStub is set; otherwise it is disabled.
func NewSMSSender(cfg SMSConfig) *SMSSender {
	if cfg.HTTPTimeout == 0 {
		cfg.HTTPTimeout = 8 * time.Second
	}
	return &SMSSender{
		cfg:    cfg,
		client: &http.Client{Timeout: cfg.HTTPTimeout},
	}
}

// IsConfigured reports whether real Twilio credentials are wired.
func (s *SMSSender) IsConfigured() bool {
	return s.cfg.AccountSID != "" && s.cfg.AuthToken != "" && s.cfg.VerifyServiceSID != ""
}

// stubbed reports log-the-code mode: explicitly requested, no credentials.
func (s *SMSSender) stubbed() bool { return s.cfg.DevStub && !s.IsConfigured() }

// Available reports whether the sender can serve the SMS factor at all.
func (s *SMSSender) Available() bool { return s.IsConfigured() || s.cfg.DevStub }

// StartVerification asks Twilio Verify to send a code to phoneE164.
// Twilio owns the code generation; we never see plaintext. To verify
// a user-entered code, the service layer calls CheckVerification.
func (s *SMSSender) StartVerification(ctx context.Context, phoneE164 string) error {
	if !s.Available() {
		return ErrSMSDisabled
	}
	if s.stubbed() {
		// Stub mode: log a fake code so the UI flow can be exercised
		// without real Twilio. CheckVerification accepts the same
		// fixed value below.
		if s.cfg.StubLog != nil {
			s.cfg.StubLog(phoneE164, "000000")
		}
		return nil
	}
	endpoint := fmt.Sprintf("https://verify.twilio.com/v2/Services/%s/Verifications", s.cfg.VerifyServiceSID)
	form := url.Values{}
	form.Set("To", phoneE164)
	form.Set("Channel", "sms")
	return s.post(ctx, endpoint, form)
}

// CheckVerification submits a user-entered code to Twilio Verify.
// Returns nil on approval; ErrCodeRejected when Twilio reports any
// status other than "approved".
func (s *SMSSender) CheckVerification(ctx context.Context, phoneE164, code string) error {
	if !s.Available() {
		return ErrSMSDisabled
	}
	if s.stubbed() {
		// Stub: accept the fixed dev code only.
		if code == "000000" {
			return nil
		}
		return ErrCodeRejected
	}
	endpoint := fmt.Sprintf("https://verify.twilio.com/v2/Services/%s/VerificationCheck", s.cfg.VerifyServiceSID)
	form := url.Values{}
	form.Set("To", phoneE164)
	form.Set("Code", code)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.SetBasicAuth(s.cfg.AccountSID, s.cfg.AuthToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("twilio check: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("twilio check %d: %s", resp.StatusCode, string(body))
	}
	var out struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("twilio decode: %w", err)
	}
	if out.Status != "approved" {
		return ErrCodeRejected
	}
	return nil
}

// ErrCodeRejected — Twilio said the code didn't match.
var ErrCodeRejected = errors.New("sms: code rejected")

func (s *SMSSender) post(ctx context.Context, endpoint string, form url.Values) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewBufferString(form.Encode()))
	if err != nil {
		return err
	}
	req.SetBasicAuth(s.cfg.AccountSID, s.cfg.AuthToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("twilio post: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("twilio %d: %s", resp.StatusCode, string(body))
	}
	return nil
}
