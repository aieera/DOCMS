package esign_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vaultdms/vaultdms/pkg/esign"
)

// ----- OAuth state HMAC -------------------------------------------

func TestOAuth_StateRoundTrip(t *testing.T) {
	secret, _ := esign.NewHMACSecret()
	cfg := esign.OAuthConfig{Provider: esign.ProviderDocuSign, HMACSecret: secret, ClientID: "id", ClientSecret: "s",
		AuthorizeURL: "https://x", TokenURL: "https://x/t", RedirectURI: "https://us/cb"}
	url, state, err := cfg.AuthorizeURLBuilder("tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(url, "state=") {
		t.Fatal("expected state= in url")
	}
	got, ok := cfg.VerifyState(state)
	if !ok || got != "tenant-1" {
		t.Fatalf("verify failed: ok=%v got=%q", ok, got)
	}
	// Provider must round-trip via ParseState so the callback handler
	// can recover it without a ?provider= query param.
	_, gotProvider, ok := esign.ParseState(state)
	if !ok || gotProvider != string(esign.ProviderDocuSign) {
		t.Fatalf("ParseState provider=%q ok=%v", gotProvider, ok)
	}
}

func TestOAuth_StateTamperDetected(t *testing.T) {
	secret, _ := esign.NewHMACSecret()
	cfg := esign.OAuthConfig{Provider: esign.ProviderDocuSign, HMACSecret: secret}
	_, state, _ := cfg.AuthorizeURLBuilder("tenant-1")
	// flip a character in the HMAC half
	tampered := state[:len(state)-1] + "0"
	if _, ok := cfg.VerifyState(tampered); ok {
		t.Fatal("tampered state must fail")
	}
}

// ----- DocuSign webhook HMAC --------------------------------------

func TestDocuSign_Webhook_HMACGood(t *testing.T) {
	body := []byte(`{"event":"envelope-completed","envelopeId":"E1","eventDateTime":"2026-05-09T12:00:00Z","status":"Completed"}`)
	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write(body)
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	c, err := esign.NewDocuSign(esign.DocuSignConfig{
		AccessToken: "t", AccountID: "a", BaseURI: "https://x",
		HMACSecret: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	headers := http.Header{}
	headers.Set("X-DocuSign-Signature-1", sig)
	evt, err := c.ParseWebhook(headers, body)
	if err != nil {
		t.Fatalf("parse webhook: %v", err)
	}
	if evt.EnvelopeID != "E1" {
		t.Fatalf("envelope_id = %q", evt.EnvelopeID)
	}
	if evt.Status != "completed" {
		t.Fatalf("status = %q, want completed", evt.Status)
	}
}

func TestDocuSign_Webhook_HMACBadRejects(t *testing.T) {
	body := []byte(`{"event":"envelope-completed","envelopeId":"E1"}`)
	c, _ := esign.NewDocuSign(esign.DocuSignConfig{
		AccessToken: "t", AccountID: "a", BaseURI: "https://x", HMACSecret: "secret",
	})
	headers := http.Header{}
	headers.Set("X-DocuSign-Signature-1", "AAAA") // wrong sig
	_, err := c.ParseWebhook(headers, body)
	if !errors.Is(err, esign.ErrSignatureMismatch) {
		t.Fatalf("expected ErrSignatureMismatch, got %v", err)
	}
}

// ----- Adobe Sign webhook HMAC ------------------------------------

func TestAdobeSign_Webhook_HMACGood(t *testing.T) {
	body := []byte(`{"event":"AGREEMENT_CREATED","agreementId":"A1","eventDate":"2026-05-09T12:00:00Z","id":"evt-1","agreement":{"status":"OUT_FOR_SIGNATURE"}}`)
	clientID, secret := "client-1", "secret-1"
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(clientID))
	mac.Write(body)
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	c, err := esign.NewAdobeSign(esign.AdobeSignConfig{
		AccessToken: "t", APIAccessPoint: "https://x",
		ClientID: clientID, ClientSecret: secret,
	})
	if err != nil {
		t.Fatal(err)
	}
	headers := http.Header{}
	headers.Set("x-adobesign-checksum-sha256", sig)
	evt, err := c.ParseWebhook(headers, body)
	if err != nil {
		t.Fatalf("parse webhook: %v", err)
	}
	if evt.EnvelopeID != "A1" {
		t.Fatalf("envelope_id = %q", evt.EnvelopeID)
	}
	if evt.Status != "in_progress" {
		t.Fatalf("status = %q", evt.Status)
	}
}

// ----- DocuSign Send + GetStatus against httptest -----------------

func TestDocuSign_Send_Sandbox(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Bearer header must be set on every request.
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "no auth", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/envelopes"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"envelopeId":     "E1",
				"status":         "sent",
				"statusDateTime": "2026-05-09T12:00:00Z",
			})
		case strings.HasSuffix(r.URL.Path, "/views/recipient"):
			_ = json.NewEncoder(w).Encode(map[string]any{"url": "https://docusign/sign/E1"})
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/envelopes/E1"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":         "completed",
				"statusDateTime": "2026-05-09T13:00:00Z",
				"recipients": map[string]any{
					"signers": []map[string]any{
						{"email": "a@b.com", "status": "completed", "signedDateTime": "2026-05-09T12:30:00Z", "clientIp": "1.2.3.4"},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, err := esign.NewDocuSign(esign.DocuSignConfig{
		AccessToken: "tok", AccountID: "acct", BaseURI: srv.URL,
		HMACSecret: "secret", HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Send(context.Background(), esign.SendReq{
		TenantID: "t", RequestID: "req-1",
		DocumentName: "doc.pdf", DocumentBytes: []byte("%PDF-1.7"),
		Recipients: []esign.Recipient{{Name: "Alice", Email: "a@b.com", Order: 1, Role: "signer", Embedded: true}},
		ReturnURL: "https://us/done",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if resp.EnvelopeID != "E1" {
		t.Fatalf("envelope_id = %q", resp.EnvelopeID)
	}
	if resp.SigningURLs["a@b.com"] == "" {
		t.Fatal("expected embedded signing URL")
	}
	st, err := c.GetStatus(context.Background(), esign.StatusReq{EnvelopeID: "E1"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != "completed" {
		t.Fatalf("status = %q", st.Status)
	}
	if len(st.Recipients) != 1 || st.Recipients[0].Status != "signed" {
		t.Fatalf("recipients = %+v", st.Recipients)
	}
}

// ----- Mock smoke -------------------------------------------------

func TestMock_SendCompleteFetch(t *testing.T) {
	m := esign.NewMock()
	resp, err := m.Send(context.Background(), esign.SendReq{
		TenantID: "t", RequestID: "r-1", DocumentName: "x.pdf",
		DocumentBytes: []byte("%PDF"),
		Recipients: []esign.Recipient{{Email: "a@b.com", Name: "Alice", Order: 1, Role: "signer"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	m.CompleteEnvelope(resp.EnvelopeID)
	got, err := m.GetSignedDocument(context.Background(), esign.GetSignedReq{
		EnvelopeID: resp.EnvelopeID, IncludeCoC: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got.SignedPDF), "MOCK-SIGNED-") {
		t.Fatalf("signed pdf header = %q", got.SignedPDF[:min(20, len(got.SignedPDF))])
	}
	if len(got.CoCPDF) == 0 {
		t.Fatal("expected CoC bytes")
	}
}

func min(a, b int) int { if a < b { return a }; return b }
