// ADR 0070 — HMAC session wrapper invariants.
//
// The wrapper is the load-bearing piece of the begin/finish dance —
// no server-side session store means the HMAC IS the trust gate.
// These tests pin the security properties so a refactor that
// silently widens the trust boundary (e.g., dropping the flow
// check) gets caught.
package service

import (
	"encoding/json"
	"testing"
	"time"
)

func TestWebAuthn_RoundTripsEnvelope(t *testing.T) {
	t.Setenv("VAULTDMS_WEBAUTHN_HMAC_SECRET", "test-secret-of-sufficient-length-32b")

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
	t.Setenv("VAULTDMS_WEBAUTHN_HMAC_SECRET", "test-secret")
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
	t.Setenv("VAULTDMS_WEBAUTHN_HMAC_SECRET", "test-secret")
	tok, _ := WrapSession(SessionEnvelope{
		UserID:   "u-1",
		Flow:     "register",
		Session:  json.RawMessage(`{}`),
		IssuedAt: time.Now().Unix(),
	})
	// Flip a byte in the token — base64url alphabet means '_' is
	// a valid char, so substituting any single char produces a
	// well-formed but tag-failing token.
	bad := []byte(tok)
	bad[len(bad)-2] = '_'
	if _, err := UnwrapSession(string(bad), "register"); err != ErrInvalidSession {
		t.Errorf("tampered token should fail; got %v", err)
	}
}

func TestWebAuthn_DifferentSecretRejects(t *testing.T) {
	// A token wrapped with secret-A must NOT verify under secret-B.
	t.Setenv("VAULTDMS_WEBAUTHN_HMAC_SECRET", "secret-A")
	tok, _ := WrapSession(SessionEnvelope{
		UserID: "u-1", Flow: "register", IssuedAt: time.Now().Unix(),
	})
	t.Setenv("VAULTDMS_WEBAUTHN_HMAC_SECRET", "secret-B")
	if _, err := UnwrapSession(tok, "register"); err != ErrInvalidSession {
		t.Errorf("token from different secret should fail; got %v", err)
	}
}

func TestWebAuthn_ExpiredTokenRejected(t *testing.T) {
	t.Setenv("VAULTDMS_WEBAUTHN_HMAC_SECRET", "test-secret")
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
	t.Setenv("VAULTDMS_WEBAUTHN_RPID", "")
	if cfg := LoadWebAuthnConfigFromEnv(); cfg != nil {
		t.Errorf("expected nil config when RPID missing; got %+v", cfg)
	}
}

func TestWebAuthn_LoadConfig_DefaultOriginsToHTTPSRPID(t *testing.T) {
	t.Setenv("VAULTDMS_WEBAUTHN_RPID", "vaultdms.example.com")
	t.Setenv("VAULTDMS_WEBAUTHN_ORIGINS", "")
	cfg := LoadWebAuthnConfigFromEnv()
	if cfg == nil {
		t.Fatal("expected config")
	}
	if len(cfg.RPOrigins) != 1 || cfg.RPOrigins[0] != "https://vaultdms.example.com" {
		t.Errorf("default origin = %v; want https://vaultdms.example.com", cfg.RPOrigins)
	}
}
