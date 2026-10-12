package notifications

import (
	"context"
	"errors"
	"testing"
)

// CB-4 — with no Twilio credentials the SMS adapter silently switched to
// stub mode and CheckVerification accepted the fixed code 000000: any
// deploy that forgot SEDOC_TWILIO_* turned SMS MFA into a known-code
// bypass. Stub mode is now explicit opt-in only (SEDOC_MFA_DEV_STUB=1 →
// DevStub); otherwise an unconfigured sender refuses with ErrSMSDisabled.
func TestSMSOTP_NoStubWithoutExplicitFlag(t *testing.T) {
	ctx := context.Background()
	s := NewSMSSender(SMSConfig{}) // no credentials, no flag
	if s.Available() {
		t.Fatal("an unconfigured sender without the dev flag must not be available")
	}
	if err := s.StartVerification(ctx, "+14155552671"); !errors.Is(err, ErrSMSDisabled) {
		t.Fatalf("StartVerification = %v, want ErrSMSDisabled", err)
	}
	if err := s.CheckVerification(ctx, "+14155552671", "000000"); !errors.Is(err, ErrSMSDisabled) {
		t.Fatalf("CheckVerification(000000) = %v, want ErrSMSDisabled — the stub code must not pass", err)
	}

	var logged string
	stub := NewSMSSender(SMSConfig{DevStub: true, StubLog: func(_, code string) { logged = code }})
	if !stub.Available() || stub.IsConfigured() {
		t.Fatal("explicit dev stub: available, not configured")
	}
	if err := stub.StartVerification(ctx, "+14155552671"); err != nil || logged != "000000" {
		t.Fatalf("dev stub start: err=%v logged=%q", err, logged)
	}
	if err := stub.CheckVerification(ctx, "+14155552671", "000000"); err != nil {
		t.Fatalf("dev stub accepts its fixed code: %v", err)
	}
	if err := stub.CheckVerification(ctx, "+14155552671", "123456"); !errors.Is(err, ErrCodeRejected) {
		t.Fatalf("dev stub rejects other codes: %v", err)
	}

	real := NewSMSSender(SMSConfig{AccountSID: "AC", AuthToken: "t", VerifyServiceSID: "VA", DevStub: true})
	if !real.IsConfigured() {
		t.Fatal("real credentials win over the dev flag")
	}
}

func TestEmailOTP_NoStubWithoutExplicitFlag(t *testing.T) {
	ctx := context.Background()
	e := NewEmailOTPSender(EmailOTPConfig{})
	if e.Available() {
		t.Fatal("unconfigured email sender without the dev flag must not be available")
	}
	if err := e.SendCode(ctx, "a@example.com", "123456"); !errors.Is(err, ErrEmailDisabled) {
		t.Fatalf("SendCode = %v, want ErrEmailDisabled", err)
	}

	var logged string
	stub := NewEmailOTPSender(EmailOTPConfig{DevStub: true, StubLog: func(_, code string) { logged = code }})
	if err := stub.SendCode(ctx, "a@example.com", "654321"); err != nil || logged != "654321" {
		t.Fatalf("dev stub: err=%v logged=%q", err, logged)
	}
}
