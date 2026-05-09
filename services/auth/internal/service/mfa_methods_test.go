// ADR 0063 — flow-level invariants. Things that need a real
// Postgres / Redis / Twilio go through Playwright + integration
// tests; what we pin here:
//   - Strength ordering matches the ADR table
//   - Policy validation rejects sms-only AND unknown methods
//   - Email/phone masking never leaks the full destination
package service

import (
	"strings"
	"testing"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
)

func TestMethodStrength_ADROrdering(t *testing.T) {
	cases := []struct {
		m       MFAMethod
		stronger []MFAMethod
	}{
		{MethodPasskey, nil},
		{MethodTOTP, []MFAMethod{MethodPasskey}},
		{MethodPush, []MFAMethod{MethodPasskey, MethodTOTP}},
		{MethodEmail, []MFAMethod{MethodPasskey, MethodTOTP, MethodPush}},
		{MethodSMS, []MFAMethod{MethodPasskey, MethodTOTP, MethodPush, MethodEmail}},
	}
	for _, c := range cases {
		s := MethodStrength(c.m)
		for _, x := range c.stronger {
			if MethodStrength(x) <= s {
				t.Errorf("%s (str=%d) must outrank %s (str=%d)", x, MethodStrength(x), c.m, s)
			}
		}
	}
}

func TestValidatePolicy_RejectsSMSOnly(t *testing.T) {
	err := validatePolicy(TenantMFAPolicy{Mode: "required", AllowedMethods: []string{"sms"}})
	if err == nil {
		t.Fatal("sms-only must be rejected (admin-guardrail per ADR 0063)")
	}
}

func TestValidatePolicy_AcceptsTOTPPlusSMS(t *testing.T) {
	if err := validatePolicy(TenantMFAPolicy{Mode: "required", AllowedMethods: []string{"totp", "sms"}}); err != nil {
		t.Errorf("totp+sms must pass: %v", err)
	}
}

func TestValidatePolicy_RejectsUnknownMethod(t *testing.T) {
	err := validatePolicy(TenantMFAPolicy{Mode: "optional", AllowedMethods: []string{"totp", "smoke-signal"}})
	if err == nil {
		t.Fatal("unknown method must be rejected")
	}
}

func TestValidatePolicy_RejectsBadMode(t *testing.T) {
	if err := validatePolicy(TenantMFAPolicy{Mode: "MAYBE", AllowedMethods: []string{"totp"}}); err == nil {
		t.Fatal("unknown mode must be rejected")
	}
}

func TestMaskPhone_HidesAllButLast4(t *testing.T) {
	got := maskPhone("+14155552671")
	if !strings.HasSuffix(got, "2671") || strings.Contains(got, "+1415") {
		t.Errorf("maskPhone leaked digits: %q", got)
	}
}

func TestMaskEmail_KeepsFirstCharAndDomain(t *testing.T) {
	got := maskEmail("alice@example.com")
	if got == "alice@example.com" {
		t.Errorf("maskEmail returned plaintext: %q", got)
	}
	if !strings.Contains(got, "@example.com") {
		t.Errorf("maskEmail dropped domain: %q", got)
	}
	if !strings.HasPrefix(got, "a") {
		t.Errorf("maskEmail dropped first char: %q", got)
	}
}

func TestGenerateNumericOTP_ShapeAndEntropy(t *testing.T) {
	a, err := generateNumericOTP(6)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 6 {
		t.Errorf("len=%d want 6", len(a))
	}
	for _, r := range a {
		if r < '0' || r > '9' {
			t.Errorf("non-digit in OTP: %q", a)
		}
	}
	// Two consecutive calls must (almost certainly) differ — 1-in-1M
	// false positive is acceptable.
	b, _ := generateNumericOTP(6)
	if a == b {
		t.Errorf("identical OTPs across two calls — entropy looks broken (%q == %q)", a, b)
	}
}

func TestErrMFAEnrollmentRequired_IsForbidden(t *testing.T) {
	// Pin the kind so the HTTP layer maps it to 403, not 500.
	if vdmserr.KindOf(ErrMFAEnrollmentRequired) != vdmserr.KindForbidden {
		t.Errorf("ErrMFAEnrollmentRequired must be Forbidden kind for 403 mapping, got %v",
			vdmserr.KindOf(ErrMFAEnrollmentRequired))
	}
}

func TestAllowedSet_EmptyDefaultsToAll(t *testing.T) {
	s := allowedSet(nil)
	for _, m := range []string{"passkey", "totp", "push", "email", "sms"} {
		if !s[m] {
			t.Errorf("default policy must allow %q", m)
		}
	}
}
