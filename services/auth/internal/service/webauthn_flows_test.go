// ADR 0070 — flow-level invariants that don't need a real DB.
//
// The full begin/finish round-trip needs UserRepository + a live
// Postgres + a virtual authenticator; that's covered by Playwright
// (web/e2e/43-passkeys.spec.ts). What we pin here:
//   - NewWebAuthnLib returns nil for nil config (graceful disable)
//   - NewWebAuthnLib succeeds for a valid config
//   - Service methods return ErrWebAuthnNotImplemented when
//     WebAuthnLib is nil (deploy without RPID env var)
//   - splitUserTenant decodes the composite envelope ID correctly
package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestNewWebAuthnLib_NilConfigReturnsNil(t *testing.T) {
	wa, err := NewWebAuthnLib(nil)
	if err != nil {
		t.Fatalf("err=%v want nil", err)
	}
	if wa != nil {
		t.Errorf("wa=%v want nil", wa)
	}
}

func TestNewWebAuthnLib_ValidConfig(t *testing.T) {
	cfg := &WebAuthnConfig{
		RPID:          "localhost",
		RPDisplayName: "VaultDMS Test",
		RPOrigins:     []string{"http://localhost:3000"},
	}
	wa, err := NewWebAuthnLib(cfg)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if wa == nil {
		t.Fatal("wa is nil despite valid config")
	}
}

func TestPasskeyMethods_NotImplementedWhenLibNil(t *testing.T) {
	// A Service constructed without WebAuthnLib must return the
	// sentinel error from every flow method, NOT a panic. Deploy
	// without VAULTDMS_WEBAUTHN_RPID stays serviceable.
	s := &Service{} // zero-value; WebAuthnLib nil
	ctx := context.Background()

	if _, _, err := s.PasskeyRegistrationStart(ctx, uuid.New(), uuid.New()); !errors.Is(err, ErrWebAuthnNotImplemented) {
		t.Errorf("PasskeyRegistrationStart err=%v want ErrWebAuthnNotImplemented", err)
	}
	if _, err := s.PasskeyRegistrationFinish(ctx, uuid.New(), "tok", "name", []byte("{}")); !errors.Is(err, ErrWebAuthnNotImplemented) {
		t.Errorf("PasskeyRegistrationFinish err=%v want ErrWebAuthnNotImplemented", err)
	}
	if _, _, err := s.PasskeyLoginStart(ctx, "tenant", "alice@example.com"); !errors.Is(err, ErrWebAuthnNotImplemented) {
		t.Errorf("PasskeyLoginStart err=%v want ErrWebAuthnNotImplemented", err)
	}
	if _, err := s.PasskeyLoginFinish(ctx, "tok", []byte("{}"), "ip", "ua"); !errors.Is(err, ErrWebAuthnNotImplemented) {
		t.Errorf("PasskeyLoginFinish err=%v want ErrWebAuthnNotImplemented", err)
	}
}

func TestSplitUserTenant_RoundTrip(t *testing.T) {
	u, ten := uuid.New(), uuid.New()
	combined := u.String() + "|" + ten.String()
	gotU, gotT, err := splitUserTenant(combined)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if gotU != u {
		t.Errorf("user=%v want %v", gotU, u)
	}
	if gotT != ten {
		t.Errorf("tenant=%v want %v", gotT, ten)
	}
}

func TestSplitUserTenant_RejectsMalformed(t *testing.T) {
	for _, bad := range []string{
		"",
		"no-pipe",
		"only-one|",
		"|only-other",
		"not-a-uuid|" + uuid.New().String(),
		uuid.New().String() + "|not-a-uuid",
	} {
		if _, _, err := splitUserTenant(bad); err == nil {
			t.Errorf("splitUserTenant(%q) should error", bad)
		}
	}
}
