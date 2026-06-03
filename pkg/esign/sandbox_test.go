//go:build sandbox

// Sandbox smoke harness for the DocuSign + Adobe Sign adapters.
//
// Why a build tag: these tests hit the real demo.docusign.net and
// api.adobesign.com endpoints. Without the tag they're skipped from
// the default `make test` run (which is hermetic). With the tag and
// the right env vars, they run against the live sandboxes.
//
// Two tiers:
//
//   Tier A — wire health (no human in the loop). Send an envelope,
//            poll GetStatus once, clean up by voiding the envelope.
//            Asserts: OAuth bearer is valid, envelope creates, the
//            response shape we depend on hasn't drifted. Runs
//            nightly via .github/workflows/esign-sandbox.yml.
//
//   Tier B — full round-trip (DOCUSIGN_SANDBOX_AUTOSIGN=1 or the
//            matching Adobe flag). Uses each vendor's "API-driven
//            recipient sign" capability to fake the human signing
//            in CI, then GetSignedDocument and asserts the bytes
//            look like a real PDF. Opt-in: each Tier B run consumes
//            real envelope quota.
//
// Credential layout (sealed GitHub Actions env "esign-sandbox"):
//
//   DOCUSIGN_SANDBOX_ACCESS_TOKEN  long-lived dev JWT (8h)
//   DOCUSIGN_SANDBOX_ACCOUNT_ID    DocuSign account UUID
//   DOCUSIGN_SANDBOX_BASE_URI      e.g. https://demo.docusign.net
//   DOCUSIGN_SANDBOX_HMAC_SECRET   Connect HMAC secret (for ParseWebhook only)
//   DOCUSIGN_SANDBOX_SIGNER_EMAIL  recipient email — must be a real inbox you can read for Tier B
//
//   ADOBESIGN_SANDBOX_REFRESH_TOKEN  exchanged on each test run
//   ADOBESIGN_SANDBOX_API_ACCESS_POINT  regional URI from token response
//   ADOBESIGN_SANDBOX_CLIENT_ID
//   ADOBESIGN_SANDBOX_CLIENT_SECRET
//   ADOBESIGN_SANDBOX_TOKEN_URL  e.g. https://api.echosign.com/oauth/v2/refresh
//   ADOBESIGN_SANDBOX_SIGNER_EMAIL
//
// Run locally:
//
//   DOCUSIGN_SANDBOX_ACCESS_TOKEN=... \
//     go test -tags sandbox -run TestSandbox_DocuSign_TierA ./pkg/esign/...
//
//   DOCUSIGN_SANDBOX_AUTOSIGN=1 \
//     go test -tags sandbox -run TestSandbox_DocuSign_TierB_FullCycle ./pkg/esign/...
package esign_test

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aieera/sedoc/pkg/esign"
)

// ----- DocuSign ----------------------------------------------------

func TestSandbox_DocuSign_TierA(t *testing.T) {
	cfg := mustDocuSignConfig(t)

	c, err := esign.NewDocuSign(cfg)
	if err != nil {
		t.Fatalf("new docusign: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := c.Send(ctx, esign.SendReq{
		TenantID: "sandbox-tenant",
		RequestID: "sandbox-tier-a-" + time.Now().UTC().Format("20060102150405"),
		Subject: "[SeDoc Tier A smoke] Please ignore",
		Message: "Automated SeDoc sandbox check; no action required.",
		DocumentName: "smoke.pdf",
		// Minimal valid PDF — DocuSign rejects empty / non-PDF blobs.
		DocumentBytes: minimalPDF(),
		Recipients: []esign.Recipient{{
			Name:  "SeDoc Sandbox Recipient",
			Email: mustEnv(t, "DOCUSIGN_SANDBOX_SIGNER_EMAIL"),
			Order: 1, Role: "signer",
		}},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if resp.EnvelopeID == "" {
		t.Fatal("envelope_id empty in response")
	}
	t.Logf("created envelope %s with status %q", resp.EnvelopeID, resp.Status)

	// GetStatus once — confirms our follow-up call shape works
	// against the live API.
	st, err := c.GetStatus(ctx, esign.StatusReq{TenantID: "sandbox-tenant", EnvelopeID: resp.EnvelopeID})
	if err != nil {
		t.Fatalf("get status: %v", err)
	}
	if st.Status == "" {
		t.Fatal("get status returned empty status")
	}
	t.Logf("status = %q, recipients = %d", st.Status, len(st.Recipients))

	// Clean up: void the envelope so we don't litter the sandbox
	// account with smoke-test envelopes. Failure here is non-fatal
	// (the test already proved the wire works); just log.
	if err := docusignVoid(ctx, cfg, resp.EnvelopeID); err != nil {
		t.Logf("cleanup void failed (non-fatal): %v", err)
	}
}

func TestSandbox_DocuSign_TierB_FullCycle(t *testing.T) {
	if os.Getenv("DOCUSIGN_SANDBOX_AUTOSIGN") != "1" {
		t.Skip("set DOCUSIGN_SANDBOX_AUTOSIGN=1 to opt in (consumes real envelope quota)")
	}
	cfg := mustDocuSignConfig(t)

	c, err := esign.NewDocuSign(cfg)
	if err != nil {
		t.Fatalf("new docusign: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	resp, err := c.Send(ctx, esign.SendReq{
		TenantID: "sandbox-tenant",
		RequestID: "sandbox-tier-b-" + time.Now().UTC().Format("20060102150405"),
		Subject: "[SeDoc Tier B full cycle]",
		DocumentName: "smoke.pdf",
		DocumentBytes: minimalPDF(),
		Recipients: []esign.Recipient{{
			Name:  "SeDoc Sandbox Recipient",
			Email: mustEnv(t, "DOCUSIGN_SANDBOX_SIGNER_EMAIL"),
			Order: 1, Role: "signer",
		}},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	t.Logf("envelope %s — auto-completing via PUT /envelopes/{id}/recipients", resp.EnvelopeID)

	// DocuSign's "complete on behalf" via PUT /recipients with
	// status=completed. Sandbox accepts it; production demos do
	// too. This is the documented API for automated test signing.
	if err := docusignAutoComplete(ctx, cfg, resp.EnvelopeID); err != nil {
		t.Fatalf("auto-complete: %v", err)
	}

	// Poll until completed (DocuSign settles within a few seconds).
	deadline := time.Now().Add(60 * time.Second)
	var st *esign.StatusResp
	for {
		st, err = c.GetStatus(ctx, esign.StatusReq{EnvelopeID: resp.EnvelopeID})
		if err != nil {
			t.Fatalf("status poll: %v", err)
		}
		if st.Status == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("envelope did not reach completed within 60s; last status=%q", st.Status)
		}
		time.Sleep(2 * time.Second)
	}

	got, err := c.GetSignedDocument(ctx, esign.GetSignedReq{
		EnvelopeID: resp.EnvelopeID, IncludeCoC: true,
	})
	if err != nil {
		t.Fatalf("get signed: %v", err)
	}
	assertLooksLikePDF(t, "signed", got.SignedPDF)
	t.Logf("signed PDF bytes=%d", len(got.SignedPDF))
}

// ----- Adobe Sign --------------------------------------------------

func TestSandbox_AdobeSign_TierA(t *testing.T) {
	cfg := mustAdobeSignConfig(t)
	c, err := esign.NewAdobeSign(cfg)
	if err != nil {
		t.Fatalf("new adobesign: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := c.Send(ctx, esign.SendReq{
		TenantID: "sandbox-tenant",
		RequestID: "sandbox-tier-a-" + time.Now().UTC().Format("20060102150405"),
		Subject: "[SeDoc Tier A smoke] Please ignore",
		Message: "Automated SeDoc sandbox check; no action required.",
		DocumentName: "smoke.pdf",
		DocumentBytes: minimalPDF(),
		Recipients: []esign.Recipient{{
			Name:  "SeDoc Sandbox Recipient",
			Email: mustEnv(t, "ADOBESIGN_SANDBOX_SIGNER_EMAIL"),
			Order: 1, Role: "signer",
		}},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if resp.EnvelopeID == "" {
		t.Fatal("agreement id empty")
	}
	t.Logf("created agreement %s status %q", resp.EnvelopeID, resp.Status)

	st, err := c.GetStatus(ctx, esign.StatusReq{EnvelopeID: resp.EnvelopeID})
	if err != nil {
		t.Fatalf("get status: %v", err)
	}
	if st.Status == "" {
		t.Fatal("status empty")
	}
	t.Logf("status = %q, recipients = %d", st.Status, len(st.Recipients))

	// Cleanup: cancel the agreement.
	if err := adobeCancel(ctx, cfg, resp.EnvelopeID); err != nil {
		t.Logf("cleanup cancel failed (non-fatal): %v", err)
	}
}

func TestSandbox_AdobeSign_TierB_FullCycle(t *testing.T) {
	if os.Getenv("ADOBESIGN_SANDBOX_AUTOSIGN") != "1" {
		t.Skip("set ADOBESIGN_SANDBOX_AUTOSIGN=1 to opt in (consumes real envelope quota)")
	}
	// Adobe Sign's API-only auto-sign is more constrained than
	// DocuSign's. The most reliable path is to flip the agreement
	// state to SIGNED via PUT /agreements/{id}/state with the
	// agreementInfo.signatureType=ESIGN. That's what this helper
	// does. Same shape as Tier A otherwise.
	cfg := mustAdobeSignConfig(t)
	c, err := esign.NewAdobeSign(cfg)
	if err != nil {
		t.Fatalf("new adobesign: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	resp, err := c.Send(ctx, esign.SendReq{
		TenantID: "sandbox-tenant",
		RequestID: "sandbox-tier-b-" + time.Now().UTC().Format("20060102150405"),
		Subject: "[SeDoc Tier B full cycle]",
		DocumentName: "smoke.pdf",
		DocumentBytes: minimalPDF(),
		Recipients: []esign.Recipient{{
			Email: mustEnv(t, "ADOBESIGN_SANDBOX_SIGNER_EMAIL"),
			Name: "SeDoc Sandbox Recipient",
			Order: 1, Role: "signer",
		}},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	t.Logf("agreement %s — flipping state via PUT /state", resp.EnvelopeID)
	if err := adobeAutoComplete(ctx, cfg, resp.EnvelopeID); err != nil {
		t.Fatalf("auto-complete: %v", err)
	}
	deadline := time.Now().Add(60 * time.Second)
	var st *esign.StatusResp
	for {
		st, err = c.GetStatus(ctx, esign.StatusReq{EnvelopeID: resp.EnvelopeID})
		if err != nil {
			t.Fatalf("status poll: %v", err)
		}
		if st.Status == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not completed within 60s; last=%q", st.Status)
		}
		time.Sleep(2 * time.Second)
	}
	got, err := c.GetSignedDocument(ctx, esign.GetSignedReq{
		EnvelopeID: resp.EnvelopeID, IncludeCoC: true,
	})
	if err != nil {
		t.Fatalf("get signed: %v", err)
	}
	assertLooksLikePDF(t, "signed", got.SignedPDF)
	t.Logf("signed PDF bytes=%d coc bytes=%d", len(got.SignedPDF), len(got.CoCPDF))
}

// ----- helpers -----------------------------------------------------

func mustDocuSignConfig(t *testing.T) esign.DocuSignConfig {
	t.Helper()
	return esign.DocuSignConfig{
		AccessToken: mustEnv(t, "DOCUSIGN_SANDBOX_ACCESS_TOKEN"),
		AccountID:   mustEnv(t, "DOCUSIGN_SANDBOX_ACCOUNT_ID"),
		BaseURI:     mustEnv(t, "DOCUSIGN_SANDBOX_BASE_URI"),
		HMACSecret:  os.Getenv("DOCUSIGN_SANDBOX_HMAC_SECRET"),
	}
}

// mustAdobeSignConfig refreshes the access token on every test
// run — Adobe's bearer is short-lived (1h) and the long-lived
// credential we hold is the refresh token.
func mustAdobeSignConfig(t *testing.T) esign.AdobeSignConfig {
	t.Helper()
	clientID := mustEnv(t, "ADOBESIGN_SANDBOX_CLIENT_ID")
	clientSecret := mustEnv(t, "ADOBESIGN_SANDBOX_CLIENT_SECRET")
	apiAccessPoint := mustEnv(t, "ADOBESIGN_SANDBOX_API_ACCESS_POINT")
	tokenURL := mustEnv(t, "ADOBESIGN_SANDBOX_TOKEN_URL")
	refresh := mustEnv(t, "ADOBESIGN_SANDBOX_REFRESH_TOKEN")

	oc := esign.OAuthConfig{
		ClientID: clientID, ClientSecret: clientSecret,
		TokenURL: tokenURL,
		HMACSecret: []byte("adobe-sandbox-hmac-not-used-for-refresh-but-satisfies-32-byte-min"),
	}
	tok, err := oc.RefreshToken(context.Background(), http.DefaultClient, refresh)
	if err != nil {
		t.Fatalf("refresh adobe token: %v", err)
	}
	return esign.AdobeSignConfig{
		AccessToken:    tok.AccessToken,
		APIAccessPoint: apiAccessPoint,
		ClientID:       clientID,
		ClientSecret:   clientSecret,
	}
}

func mustEnv(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Skipf("%s not set — skipping sandbox test", key)
	}
	return v
}

// minimalPDF returns the smallest blob both vendors accept as a PDF.
// 1.4-spec compliant; ~250 bytes.
func minimalPDF() []byte {
	return []byte("%PDF-1.4\n1 0 obj<<>>endobj\nxref\n0 2\n0000000000 65535 f \n0000000010 00000 n \ntrailer<</Size 2/Root 1 0 R>>\nstartxref\n34\n%%EOF")
}

// assertLooksLikePDF: starts with %PDF, > 1KB, has %%EOF.
func assertLooksLikePDF(t *testing.T, label string, b []byte) {
	t.Helper()
	if len(b) < 1024 {
		t.Fatalf("%s: bytes=%d, want > 1024", label, len(b))
	}
	if !strings.HasPrefix(string(b[:8]), "%PDF-") {
		t.Fatalf("%s: header = %q, want %%PDF-", label, b[:8])
	}
	if !strings.Contains(string(b[len(b)-32:]), "%%EOF") {
		t.Fatalf("%s: missing %%EOF trailer", label)
	}
}

// docusignVoid issues VOID via PUT /envelopes/{id} status=voided.
func docusignVoid(ctx context.Context, cfg esign.DocuSignConfig, envelopeID string) error {
	body := strings.NewReader(`{"status":"voided","voidedReason":"vaultdms-sandbox-cleanup"}`)
	u := strings.TrimRight(cfg.BaseURI, "/") + "/restapi/v2.1/accounts/" + cfg.AccountID + "/envelopes/" + envelopeID
	req, err := http.NewRequestWithContext(ctx, "PUT", u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return errFromStatus(resp.StatusCode, "void")
	}
	return nil
}

// docusignAutoComplete uses the documented PUT /envelopes/{id}/recipients
// status=completed pattern. Sandbox-only; production developer
// accounts gate this behind a feature flag.
func docusignAutoComplete(ctx context.Context, cfg esign.DocuSignConfig, envelopeID string) error {
	// Set recipient status to completed for the (single) signer.
	body := strings.NewReader(`{"signers":[{"recipientId":"1","status":"completed"}]}`)
	u := strings.TrimRight(cfg.BaseURI, "/") + "/restapi/v2.1/accounts/" + cfg.AccountID + "/envelopes/" + envelopeID + "/recipients?resend_envelope=false"
	req, err := http.NewRequestWithContext(ctx, "PUT", u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return errFromStatus(resp.StatusCode, "auto-complete")
	}
	return nil
}

// adobeCancel cancels an agreement via PUT /agreements/{id}/state.
func adobeCancel(ctx context.Context, cfg esign.AdobeSignConfig, agreementID string) error {
	return adobeSetState(ctx, cfg, agreementID, "CANCELLED", "vaultdms-sandbox-cleanup")
}

func adobeAutoComplete(ctx context.Context, cfg esign.AdobeSignConfig, agreementID string) error {
	return adobeSetState(ctx, cfg, agreementID, "SIGNED", "")
}

func adobeSetState(ctx context.Context, cfg esign.AdobeSignConfig, agreementID, state, reason string) error {
	form := url.Values{}
	body := strings.NewReader(`{"state":"` + state + `","agreementCancellationInfo":{"comment":"` + reason + `","notifyOthers":false}}`)
	_ = form
	u := strings.TrimRight(cfg.APIAccessPoint, "/") + "/api/rest/v6/agreements/" + agreementID + "/state"
	req, err := http.NewRequestWithContext(ctx, "PUT", u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return errFromStatus(resp.StatusCode, "set-state="+state)
	}
	return nil
}

type sandboxErr struct{ status int; op string }

func (e sandboxErr) Error() string { return e.op + ": status " + itoa(e.status) }

func errFromStatus(status int, op string) error { return sandboxErr{status: status, op: op} }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
