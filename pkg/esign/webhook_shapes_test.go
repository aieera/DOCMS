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
	"os"
	"testing"
	"time"

	"github.com/aieera/sedoc/pkg/esign"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func docuSignClient(t *testing.T, secret string) *esign.DocuSignClient {
	t.Helper()
	c, err := esign.NewDocuSign(esign.DocuSignConfig{AccessToken: "t", AccountID: "a", BaseURI: "https://x", HMACSecret: secret})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func signedHeaders(secret string, body []byte) http.Header {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	h := http.Header{}
	h.Set("X-DocuSign-Signature-1", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	return h
}

// DocuSign Connect (JSON, v2.1) puts the envelope id and status under
// data / data.envelopeSummary; the top level only carries the event name
// and generatedDateTime.
func TestDocuSignWebhook_ParsesConnectPayload(t *testing.T) {
	c := docuSignClient(t, "connect-hmac-key")

	body := fixture(t, "docusign_connect_envelope_completed.json")
	evt, err := c.ParseWebhook(signedHeaders("connect-hmac-key", body), body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if evt.EnvelopeID != "93be49ab-34a1-4f5e-a6f0-1b2d3c4e5f60" {
		t.Errorf("EnvelopeID = %q", evt.EnvelopeID)
	}
	if evt.Status != "completed" {
		t.Errorf("Status = %q, want completed", evt.Status)
	}
	if evt.EventType != "envelope.completed" {
		t.Errorf("EventType = %q, want envelope.completed", evt.EventType)
	}
	if want := time.Date(2026, 10, 12, 10, 0, 1, 42578300, time.UTC); !evt.OccurredAt.Equal(want) {
		t.Errorf("OccurredAt = %v, want %v", evt.OccurredAt, want)
	}
	if evt.ExternalID == "" {
		t.Error("ExternalID (dedup key) is empty")
	}

	rbody := fixture(t, "docusign_connect_recipient_completed.json")
	revt, err := c.ParseWebhook(signedHeaders("connect-hmac-key", rbody), rbody)
	if err != nil {
		t.Fatalf("parse recipient event: %v", err)
	}
	if revt.EnvelopeID != evt.EnvelopeID {
		t.Errorf("recipient event EnvelopeID = %q", revt.EnvelopeID)
	}
	if revt.EventType != "recipient.signed" {
		t.Errorf("recipient EventType = %q, want recipient.signed", revt.EventType)
	}
	if revt.Status != "in_progress" {
		t.Errorf("recipient event Status = %q, want in_progress (envelope still out)", revt.Status)
	}
	if revt.ExternalID == evt.ExternalID {
		t.Error("distinct events must not share a dedup key")
	}
}

func TestDocuSignWebhook_RejectsPayloadWithoutEnvelope(t *testing.T) {
	c := docuSignClient(t, "k")
	body := []byte(`{"event":"envelope-completed","generatedDateTime":"2026-10-12T10:00:00Z","data":{}}`)
	if _, err := c.ParseWebhook(signedHeaders("k", body), body); err == nil {
		t.Fatal("a verified payload with no envelope id must not decode as an event")
	}
}

func adobeClient(t *testing.T) *esign.AdobeSignClient {
	t.Helper()
	c, err := esign.NewAdobeSign(esign.AdobeSignConfig{
		AccessToken: "t", APIAccessPoint: "https://x", ClientID: "CBJCHBCAABAAclient", ClientSecret: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Adobe Acrobat Sign webhooks carry no body MAC. Adobe documents the
// X-AdobeSign-ClientId header (the application's client id) as the check
// a receiver performs; the event lives under agreement.* with the
// notification id as the unique key.
func TestAdobeWebhook_ClientIDVerified(t *testing.T) {
	c := adobeClient(t)
	body := fixture(t, "adobesign_agreement_workflow_completed.json")

	h := http.Header{}
	h.Set("X-AdobeSign-ClientId", "CBJCHBCAABAAclient")
	evt, err := c.ParseWebhook(h, body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if evt.EnvelopeID != "CBJCHBCAABAAG0hS7Jx3Yq1k2L4mN6oP8rT0vW2xZ4" {
		t.Errorf("EnvelopeID = %q", evt.EnvelopeID)
	}
	if evt.Status != "completed" {
		t.Errorf("Status = %q, want completed", evt.Status)
	}
	if evt.EventType != "envelope.completed" {
		t.Errorf("EventType = %q, want envelope.completed", evt.EventType)
	}
	if evt.ExternalID != "a8c3e1f2-6b7d-4e5f-9a0b-1c2d3e4f5a6b" {
		t.Errorf("ExternalID = %q, want the webhookNotificationId", evt.ExternalID)
	}
	if want := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC); !evt.OccurredAt.Equal(want) {
		t.Errorf("OccurredAt = %v", evt.OccurredAt)
	}

	for name, hdr := range map[string]string{"missing": "", "wrong": "CBJCHBCAABAAother"} {
		h := http.Header{}
		if hdr != "" {
			h.Set("X-AdobeSign-ClientId", hdr)
		}
		if _, err := c.ParseWebhook(h, body); !errors.Is(err, esign.ErrSignatureMismatch) {
			t.Errorf("%s client id: err = %v, want ErrSignatureMismatch", name, err)
		}
	}
}

// Adobe webhooks are authenticated only by the (non-secret) client id, so a
// completion is confirmed with the agreements API before anything is
// ingested.
func TestAdobeSign_ConfirmCompleted(t *testing.T) {
	status := "OUT_FOR_SIGNATURE"
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		if status == "" {
			http.Error(w, "boom", http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "AGR-1", "status": status})
	}))
	defer srv.Close()
	c, err := esign.NewAdobeSign(esign.AdobeSignConfig{
		AccessToken: "stored-token", APIAccessPoint: srv.URL, ClientID: "cid", ClientSecret: "s", HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ok, err := c.ConfirmCompleted(context.Background(), "AGR-1")
	if err != nil || ok {
		t.Fatalf("OUT_FOR_SIGNATURE: ok=%v err=%v, want false/nil", ok, err)
	}
	if gotAuth != "Bearer stored-token" || gotPath != "/api/rest/v6/agreements/AGR-1" {
		t.Fatalf("request = %q %q", gotAuth, gotPath)
	}
	status = "SIGNED"
	if ok, err := c.ConfirmCompleted(context.Background(), "AGR-1"); err != nil || !ok {
		t.Fatalf("SIGNED: ok=%v err=%v, want true", ok, err)
	}
	status = ""
	if ok, err := c.ConfirmCompleted(context.Background(), "AGR-1"); err == nil || ok {
		t.Fatalf("vendor error: ok=%v err=%v, want false + error", ok, err)
	}
	if ok, err := c.ConfirmCompleted(context.Background(), ""); err == nil || ok {
		t.Fatal("an empty agreement id must be an error")
	}
}
