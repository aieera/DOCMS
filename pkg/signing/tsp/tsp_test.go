package tsp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vaultdms/vaultdms/pkg/signing/tsp"
)

// ----- State HMAC ------------------------------------------------

func TestState_RoundTrip(t *testing.T) {
	secret, err := tsp.NewStateSecret()
	if err != nil {
		t.Fatal(err)
	}
	state := tsp.SignState("session-abc", secret)
	got, ok := tsp.VerifyState(state, secret)
	if !ok || got != "session-abc" {
		t.Fatalf("verify state failed: ok=%v got=%q", ok, got)
	}
}

func TestState_TamperDetected(t *testing.T) {
	secret, _ := tsp.NewStateSecret()
	state := tsp.SignState("sess-1", secret)
	// flip one byte after the dot
	tampered := state[:len(state)-1] + "0"
	if _, ok := tsp.VerifyState(tampered, secret); ok {
		t.Fatal("expected tampered state to fail HMAC")
	}
}

func TestState_WrongSecret(t *testing.T) {
	a, _ := tsp.NewStateSecret()
	b, _ := tsp.NewStateSecret()
	state := tsp.SignState("s1", a)
	if _, ok := tsp.VerifyState(state, b); ok {
		t.Fatal("verifying with wrong secret must fail")
	}
}

// ----- MockClient -----------------------------------------------

func TestMock_HappyPath(t *testing.T) {
	ctx := context.Background()
	c := tsp.NewMock()
	if _, err := c.Register(ctx, tsp.RegisterReq{SignerEmail: "a@b.com"}); err != nil {
		t.Fatal(err)
	}
	auth, err := c.Authorize(ctx, tsp.AuthorizeReq{
		DocumentHash: "deadbeef00000000",
		ReturnURL:    "https://app/return?session=s",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Auth code is encoded into the redirect URL by the mock.
	idx := -1
	const marker = "&code="
	for i := 0; i+len(marker) <= len(auth.RedirectURL); i++ {
		if auth.RedirectURL[i:i+len(marker)] == marker {
			idx = i + len(marker)
			break
		}
	}
	if idx < 0 {
		t.Fatalf("redirect missing &code=: %s", auth.RedirectURL)
	}
	code := auth.RedirectURL[idx:]
	signed, err := c.Sign(ctx, tsp.SignReq{
		ExternalID: auth.ExternalID, AuthCode: code,
		DocumentHash: "deadbeef00000000",
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if len(signed.SignedHash) == 0 {
		t.Fatal("signed hash empty")
	}
	if signed.SubjectDN == "" {
		t.Fatal("subject dn empty")
	}
}

func TestMock_ExpiredErrorSurfaces(t *testing.T) {
	c := tsp.NewMock()
	c.SetAuthCodeError("expired")
	auth, _ := c.Authorize(context.Background(), tsp.AuthorizeReq{
		DocumentHash: "deadbeef", ReturnURL: "https://x?session=s",
	})
	_, err := c.Sign(context.Background(), tsp.SignReq{
		ExternalID: auth.ExternalID, AuthCode: "ignored", DocumentHash: "deadbeef",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errIs(err, tsp.ErrSessionExpired) {
		t.Fatalf("expected ErrSessionExpired, got %v", err)
	}
}

func TestMock_DoubleSignFails(t *testing.T) {
	ctx := context.Background()
	c := tsp.NewMock()
	auth, _ := c.Authorize(ctx, tsp.AuthorizeReq{
		DocumentHash: "deadbeefdeadbeef", ReturnURL: "https://x?session=s",
	})
	// Recover code from URL
	codeStart := -1
	for i := 0; i+6 <= len(auth.RedirectURL); i++ {
		if auth.RedirectURL[i:i+6] == "&code=" {
			codeStart = i + 6
			break
		}
	}
	code := auth.RedirectURL[codeStart:]
	_, err := c.Sign(ctx, tsp.SignReq{ExternalID: auth.ExternalID, AuthCode: code, DocumentHash: "deadbeefdeadbeef"})
	if err != nil {
		t.Fatalf("first sign: %v", err)
	}
	_, err = c.Sign(ctx, tsp.SignReq{ExternalID: auth.ExternalID, AuthCode: code, DocumentHash: "deadbeefdeadbeef"})
	if !errIs(err, tsp.ErrAlreadyConsumed) {
		t.Fatalf("expected ErrAlreadyConsumed, got %v", err)
	}
}

// ----- Sandbox: Intesi against httptest -------------------------

// TestIntesi_Sandbox stubs the Intesi sandbox at three endpoints
// (/oauth/token, /v1/transactions, /v1/transactions/sign) and walks
// the full ceremony. Demonstrates the adapter handles token caching
// + the "completed" status path.
func TestIntesi_Sandbox(t *testing.T) {
	calls := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls[r.URL.Path]++
		switch r.URL.Path {
		case "/oauth/token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "tok-1", "expires_in": 3600,
			})
		case "/v1/subjects":
			_ = json.NewEncoder(w).Encode(map[string]any{"subject_id": "sub-1"})
		case "/v1/transactions":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"transaction_id": "tx-1",
				"consent_url":    "https://intesi/consent?tx=tx-1",
				"expires_at":     time.Now().Add(time.Hour).Format(time.RFC3339),
			})
		case "/v1/transactions/sign":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":      "completed",
				"signed_hash": "ZGVhZGJlZWY=", // base64("deadbeef")
				"certificate": map[string]any{
					"leaf_pem": "-----BEGIN CERTIFICATE-----\nMOCK\n-----END CERTIFICATE-----",
					"chain_pem": "",
					"subject_dn": "CN=Tester",
					"issuer_dn":  "CN=Intesi Test CA",
					"serial_hex": "01",
					"not_before": time.Now().Add(-time.Hour),
					"not_after":  time.Now().Add(365 * 24 * time.Hour),
				},
				"revocation": map[string]string{"ocsp": "yes"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := tsp.NewIntesi(tsp.IntesiConfig{
		BaseURL: srv.URL, ClientID: "id", ClientSecret: "secret",
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := c.Register(ctx, tsp.RegisterReq{SignerEmail: "u@example.com"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	auth, err := c.Authorize(ctx, tsp.AuthorizeReq{
		SubjectID: "sub-1", DocumentHash: "abcd", HashAlgo: "SHA-256",
		ReturnURL: srv.URL + "/return", SignerEmail: "u@example.com",
	})
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if auth.ExternalID != "tx-1" {
		t.Fatalf("external_id=%q", auth.ExternalID)
	}
	signed, err := c.Sign(ctx, tsp.SignReq{
		ExternalID: auth.ExternalID, AuthCode: "code-1", DocumentHash: "abcd",
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if string(signed.SignedHash) != "deadbeef" {
		t.Fatalf("decoded signature mismatch: %q", signed.SignedHash)
	}
	if signed.SubjectDN != "CN=Tester" {
		t.Fatalf("subject_dn=%q", signed.SubjectDN)
	}

	// Token caching: only ONE /oauth/token call across 3 API calls.
	if calls["/oauth/token"] != 1 {
		t.Fatalf("expected 1 token call, got %d", calls["/oauth/token"])
	}
}

// TestInfoCert_Sandbox parallels TestIntesi but verifies the X-InfoCert-Org
// header is sent on every protected call.
func TestInfoCert_Sandbox(t *testing.T) {
	gotOrg := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/token":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
		case "/gosign/v1/sign/authorize":
			if r.Header.Get("X-InfoCert-Org") == "org-7" {
				gotOrg = true
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"session_id":    "sess",
				"authorize_url": "https://infocert/auth",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, err := tsp.NewInfoCert(tsp.InfoCertConfig{
		BaseURL: srv.URL, ClientID: "id", ClientSecret: "s", OrgID: "org-7",
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Authorize(context.Background(), tsp.AuthorizeReq{
		SubjectID: "u", DocumentHash: "h", ReturnURL: "https://x",
	}); err != nil {
		t.Fatal(err)
	}
	if !gotOrg {
		t.Fatal("expected X-InfoCert-Org header to be sent")
	}
}

func errIs(err, target error) bool {
	for e := err; e != nil; {
		if e == target {
			return true
		}
		un, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = un.Unwrap()
	}
	return false
}
