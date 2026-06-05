package handler

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

// TestVerifyHS256RoundTrip pins the OnlyOffice callback JWT verification:
// a token signed with hs256 verifies; a tampered signature or wrong secret
// is rejected.
func TestVerifyHS256RoundTrip(t *testing.T) {
	const secret = "onlyoffice-shared-secret"
	tok, err := hs256(map[string]any{"status": 2, "key": "v1", "url": "http://ds/out.docx"}, secret)
	if err != nil {
		t.Fatalf("hs256: %v", err)
	}

	payload, ok := verifyHS256(tok, secret)
	if !ok {
		t.Fatal("valid token rejected")
	}
	if !strings.Contains(string(payload), `"status":2`) {
		t.Fatalf("payload not recovered: %s", payload)
	}

	if _, ok := verifyHS256(tok, "wrong-secret"); ok {
		t.Fatal("wrong secret accepted")
	}
	if _, ok := verifyHS256(tok+"x", secret); ok {
		t.Fatal("tampered signature accepted")
	}
	if _, ok := verifyHS256("not.a.jwt.at.all", secret); ok {
		t.Fatal("malformed token accepted")
	}
	if _, ok := verifyHS256("", secret); ok {
		t.Fatal("empty token accepted")
	}
}

// TestCallbackRejectsForgedWhenSecretSet ensures the callback returns 403 on
// a missing/invalid JWT (so a save event can't be forged) and 200 on a valid
// one, when SEDOC_ONLYOFFICE_JWT is configured.
func TestCallbackRejectsForgedWhenSecretSet(t *testing.T) {
	const secret = "onlyoffice-shared-secret"
	t.Setenv("SEDOC_ONLYOFFICE_JWT", secret)
	h := NewOnlyOfficeHandler(zerolog.Nop())

	// No token → 403.
	req := httptest.NewRequest("POST", "/cb", strings.NewReader(`{"status":2,"key":"v1"}`))
	rec := httptest.NewRecorder()
	h.callback(rec, req)
	if rec.Code != 403 {
		t.Fatalf("unsigned callback: want 403, got %d", rec.Code)
	}

	// Valid token in Authorization header → 200.
	tok, _ := hs256(map[string]any{"status": 2, "key": "v1"}, secret)
	req2 := httptest.NewRequest("POST", "/cb", strings.NewReader(`{"status":2,"key":"v1","token":"`+tok+`"}`))
	req2.Header.Set("Authorization", "Bearer "+tok)
	rec2 := httptest.NewRecorder()
	h.callback(rec2, req2)
	if rec2.Code != 200 {
		t.Fatalf("signed callback: want 200, got %d", rec2.Code)
	}
}
