// ADR 0061 — HMAC session wrapper invariants.
//
// The wrapper is the load-bearing piece of the begin/finish dance —
// no server-side session store means the HMAC IS the trust gate.
// These tests pin the security properties so a refactor that
// silently widens the trust boundary (e.g., dropping the flow
// check) gets caught.
package service

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

func TestWebAuthn_RoundTripsEnvelope(t *testing.T) {
	t.Setenv("SEDOC_WEBAUTHN_HMAC_SECRET", "test-secret-of-sufficient-length-32b")

	orig := SessionEnvelope{
		UserID:   "u-1",
		Flow:     "register",
		Session:  json.RawMessage(`{"challenge":"abc"}`),
		IssuedAt: time.Now().Unix(),
	}
	tok, err := WrapSession(orig)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	got, err := UnwrapSession(tok, "register")
	if err != nil {
		t.Fatalf("unwrap: %v", err)
	}
	if got.UserID != orig.UserID || got.Flow != orig.Flow {
		t.Errorf("envelope mismatch: %+v vs %+v", got, orig)
	}
}

func TestWebAuthn_FlowCheckRejectsCrossFlowReuse(t *testing.T) {
	// A token issued for a "register" flow MUST NOT decode under
	// "login" — otherwise an attacker who steals a registration
	// session token could complete a login it wasn't authorized
	// for.
	t.Setenv("SEDOC_WEBAUTHN_HMAC_SECRET", "test-secret")
	tok, _ := WrapSession(SessionEnvelope{
		UserID:   "u-1",
		Flow:     "register",
		Session:  json.RawMessage(`{}`),
		IssuedAt: time.Now().Unix(),
	})
	if _, err := UnwrapSession(tok, "login"); err != ErrInvalidSession {
		t.Errorf("cross-flow unwrap should fail; got %v", err)
	}
}

func TestWebAuthn_TamperedHMACRejected(t *testing.T) {
	t.Setenv("SEDOC_WEBAUTHN_HMAC_SECRET", "test-secret")
	tok, _ := WrapSession(SessionEnvelope{
		UserID:   "u-1",
		Flow:     "register",
		Session:  json.RawMessage(`{}`),
		IssuedAt: time.Now().Unix(),
	})
	// Flip one bit inside the 32-byte HMAC tag: a well-formed token whose
	// tag no longer matches the body.
	raw, err := base64.URLEncoding.DecodeString(tok)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	raw[len(raw)-1] ^= 0x01
	if _, err := UnwrapSession(base64.URLEncoding.EncodeToString(raw), "register"); err != ErrInvalidSession {
		t.Errorf("tampered tag should fail; got %v", err)
	}

	// Alter the second-to-last base64 character. Depending on the body
	// length that character may only carry trailing bits a lenient decoder
	// ignores (the token then decodes to the SAME bytes), which is exactly
	// why UnwrapSession decodes strictly.
	bad := []byte(tok)
	if bad[len(bad)-2] == '_' {
		bad[len(bad)-2] = 'A'
	} else {
		bad[len(bad)-2] = '_'
	}
	if _, err := UnwrapSession(string(bad), "register"); err != ErrInvalidSession {
		t.Errorf("token with altered trailing character should fail; got %v", err)
	}
}

func TestWebAuthn_DifferentSecretRejects(t *testing.T) {
	// A token wrapped with secret-A must NOT verify under secret-B.
	t.Setenv("SEDOC_WEBAUTHN_HMAC_SECRET", "secret-A")
	tok, _ := WrapSession(SessionEnvelope{
		UserID: "u-1", Flow: "register", IssuedAt: time.Now().Unix(),
	})
	t.Setenv("SEDOC_WEBAUTHN_HMAC_SECRET", "secret-B")
	if _, err := UnwrapSession(tok, "register"); err != ErrInvalidSession {
		t.Errorf("token from different secret should fail; got %v", err)
	}
}

func TestWebAuthn_ExpiredTokenRejected(t *testing.T) {
	t.Setenv("SEDOC_WEBAUTHN_HMAC_SECRET", "test-secret")
	// Wrap with an issued_at far in the past — older than
	// SessionTokenTTL.
	tok, _ := WrapSession(SessionEnvelope{
		UserID:   "u-1",
		Flow:     "register",
		IssuedAt: time.Now().Add(-2 * SessionTokenTTL).Unix(),
	})
	if _, err := UnwrapSession(tok, "register"); err != ErrInvalidSession {
		t.Errorf("expired token should fail; got %v", err)
	}
}

func TestWebAuthn_LoadConfig_ReturnsNilWhenRPIDMissing(t *testing.T) {
	t.Setenv("SEDOC_WEBAUTHN_RPID", "")
	if cfg, err := LoadWebAuthnConfigFromEnv(); cfg != nil || err != nil {
		t.Errorf("expected nil config when RPID missing; got %+v, %v", cfg, err)
	}
}

func TestWebAuthn_LoadConfig_DefaultOriginsToHTTPSRPID(t *testing.T) {
	t.Setenv("SEDOC_WEBAUTHN_RPID", "vaultdms.example.com")
	t.Setenv("SEDOC_WEBAUTHN_ORIGINS", "")
	t.Setenv("SEDOC_WEBAUTHN_HMAC_SECRET", "a-dedicated-webauthn-secret-of-32b")
	cfg, err := LoadWebAuthnConfigFromEnv()
	if err != nil || cfg == nil {
		t.Fatalf("expected config, got %v", err)
	}
	if len(cfg.RPOrigins) != 1 || cfg.RPOrigins[0] != "https://vaultdms.example.com" {
		t.Errorf("default origin = %v; want https://vaultdms.example.com", cfg.RPOrigins)
	}
}
