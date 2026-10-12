package service

import (
	"errors"
	"testing"
)

// L-4 — the begin/finish session envelope is HMAC'd with a dedicated
// secret. It used to fall back to SEDOC_GATEWAY_SECRET, which every
// service and the gateway share, so anyone holding the gateway secret
// could forge a WebAuthn session envelope. No fallback any more.
func TestWebAuthn_RequiresDedicatedSecret(t *testing.T) {
	t.Setenv("SEDOC_WEBAUTHN_HMAC_SECRET", "")
	t.Setenv("SEDOC_GATEWAY_SECRET", "shared-gateway-secret-shared-gateway")

	if _, err := WrapSession(SessionEnvelope{UserID: "u", Flow: "login"}); err == nil {
		t.Fatal("WrapSession must not fall back to the gateway secret")
	}

	// Passkeys enabled without the dedicated secret: startup refuses.
	t.Setenv("SEDOC_WEBAUTHN_RPID", "dms.example.com")
	if cfg, err := LoadWebAuthnConfigFromEnv(); !errors.Is(err, ErrWebAuthnSecretMissing) || cfg != nil {
		t.Fatalf("enabled without secret: got cfg=%v err=%v, want ErrWebAuthnSecretMissing", cfg, err)
	}
	t.Setenv("SEDOC_WEBAUTHN_HMAC_SECRET", "too-short")
	if _, err := LoadWebAuthnConfigFromEnv(); !errors.Is(err, ErrWebAuthnSecretMissing) {
		t.Fatalf("short secret: got %v, want ErrWebAuthnSecretMissing", err)
	}
	t.Setenv("SEDOC_WEBAUTHN_HMAC_SECRET", "a-dedicated-webauthn-secret-of-32b")
	if cfg, err := LoadWebAuthnConfigFromEnv(); err != nil || cfg == nil {
		t.Fatalf("dedicated secret: got cfg=%v err=%v", cfg, err)
	}
}
