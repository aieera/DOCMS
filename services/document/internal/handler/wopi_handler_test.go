// ADR 0065 — WOPI invariants that don't require a real Redis.
//
// What we pin without a database/Redis:
//   - Token round-trip (issue + parse) under valid input
//   - Token rejection on tampered signature
//   - Token rejection on expired exp
//   - Token rejection on tenant/file_id mismatch with the URL path
//   - Discovery XML contains the doc/xls/ppt action URLs
//
// Lock + session paths need Redis and are exercised by the
// integration suite under -tags=integration.
package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestWOPIToken_RoundTrip(t *testing.T) {
	secret := "test-secret-rotate-in-prod"
	c := WOPIClaims{
		TenantID:  uuid.New(),
		UserID:    uuid.New(),
		FileID:    uuid.New(),
		ExpiresAt: time.Now().Add(1 * time.Hour),
		CanWrite:  true,
	}
	tok := IssueWOPIToken(secret, c)
	got, err := parseWOPIToken(secret, tok)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.TenantID != c.TenantID {
		t.Errorf("tenant=%v want %v", got.TenantID, c.TenantID)
	}
	if got.UserID != c.UserID {
		t.Errorf("user=%v want %v", got.UserID, c.UserID)
	}
	if got.FileID != c.FileID {
		t.Errorf("file=%v want %v", got.FileID, c.FileID)
	}
	if !got.CanWrite {
		t.Errorf("CanWrite=false want true")
	}
}

func TestWOPIToken_TamperedSignatureRejected(t *testing.T) {
	secret := "test-secret"
	c := WOPIClaims{TenantID: uuid.New(), UserID: uuid.New(), FileID: uuid.New(), ExpiresAt: time.Now().Add(time.Hour)}
	tok := IssueWOPIToken(secret, c)
	parts := strings.SplitN(tok, ".", 2)
	if len(parts) != 2 {
		t.Fatal("malformed token")
	}
	// Recompute the signature over body XOR'd with extra bytes —
	// guaranteed to differ.
	mac := hmac.New(sha256.New, []byte(secret+"extra"))
	mac.Write([]byte(parts[0]))
	bad := parts[0] + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	if _, err := parseWOPIToken(secret, bad); err == nil {
		t.Fatal("tampered signature must be rejected")
	}
}

func TestWOPIToken_ExpiredRejected(t *testing.T) {
	secret := "test-secret"
	c := WOPIClaims{TenantID: uuid.New(), UserID: uuid.New(), FileID: uuid.New(), ExpiresAt: time.Now().Add(-1 * time.Minute)}
	tok := IssueWOPIToken(secret, c)
	if _, err := parseWOPIToken(secret, tok); err == nil {
		t.Fatal("expired token must be rejected")
	}
}

func TestWOPIToken_PathMismatchRejected(t *testing.T) {
	secret := "test-secret"
	t.Setenv("SEDOC_WOPI_SECRET", secret)
	c := WOPIClaims{TenantID: uuid.New(), UserID: uuid.New(), FileID: uuid.New(), ExpiresAt: time.Now().Add(time.Hour)}
	tok := IssueWOPIToken(secret, c)

	// URL claims a DIFFERENT file_id than the token. authenticate
	// must reject.
	r := httptest.NewRequest("GET", "/wopi/files/"+uuid.New().String()+"?access_token="+tok, nil)
	r.SetPathValue("file_id", uuid.New().String())
	w := httptest.NewRecorder()

	h := &WOPIHandler{}
	got := h.authenticate(w, r)
	if got != nil {
		t.Errorf("authenticate must return nil on path/token mismatch")
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status=%d want 401", w.Code)
	}
}

func TestWOPIToken_NoEnvSecretReturnsServiceUnavailable(t *testing.T) {
	t.Setenv("SEDOC_WOPI_SECRET", "")
	r := httptest.NewRequest("GET", "/wopi/files/x?access_token=anything", nil)
	w := httptest.NewRecorder()
	(&WOPIHandler{}).authenticate(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status=%d want 503 when secret unset", w.Code)
	}
}

func TestDiscovery_ContainsAllOfficeMimes(t *testing.T) {
	t.Setenv("SEDOC_PUBLIC_URL", "https://test.example/api")
	r := httptest.NewRequest("GET", "/wopi/hosting/discovery", nil)
	w := httptest.NewRecorder()
	(&WOPIHandler{}).discovery(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		`name="Word"`, `name="Excel"`, `name="PowerPoint"`,
		`ext="docx"`, `ext="xlsx"`, `ext="pptx"`,
		`urlsrc="https://test.example/api/wopi/host/edit?WOPISrc=&lt;wopisrc&gt;"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("discovery body missing %q\n----\n%s", want, body)
		}
	}
}

func TestDiscovery_ContentTypeXML(t *testing.T) {
	r := httptest.NewRequest("GET", "/wopi/hosting/discovery", nil)
	w := httptest.NewRecorder()
	(&WOPIHandler{}).discovery(w, r)
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/xml") {
		t.Errorf("content-type=%q want application/xml", ct)
	}
}

func TestDecodeWopiHeader_PercentEncoded(t *testing.T) {
	// WOPI uses ISO-8859-1 + percent-encoded UTF-8 for filenames.
	// Decode "MyFile%20with%20spaces.docx" → "MyFile with spaces.docx".
	got := decodeWopiHeader(`"MyFile%20with%20spaces.docx"`)
	if got != "MyFile with spaces.docx" {
		t.Errorf("got %q want %q", got, "MyFile with spaces.docx")
	}
}
