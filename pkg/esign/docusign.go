// DocuSign eSignature REST API adapter.
//
// API docs: https://developers.docusign.com/docs/esign-rest-api/
//
// Auth: OAuth2 authcode → access token. Token bearer goes on
// every request; the token cache is HELD BY THE SERVICE LAYER (one
// row per tenant in esign_oauth_tokens), not the adapter. The
// adapter constructor takes the bearer string directly so a single
// *DocuSignClient can be reused per request — this matters because
// real DocuSign requests need a fresh, encrypted-at-rest token.
//
// Webhook product: DocuSign Connect. Verifies via HMAC-SHA256 over
// the body using the Connect "HMAC Signature 1" secret.
package esign

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// DocuSignConfig — per-call config. AccessToken + AccountID +
// BaseURI come from esign_oauth_tokens; HMACSecret is a tenant-
// scoped value the admin pasted in when configuring Connect.
type DocuSignConfig struct {
	AccessToken string
	AccountID   string
	BaseURI     string // e.g. "https://demo.docusign.net" — vendor-discovered, regional
	HMACSecret  string
	HTTPClient  *http.Client
}

type DocuSignClient struct {
	cfg  DocuSignConfig
	http *http.Client
}

// NewDocuSign builds a client for one envelope-scoped operation.
func NewDocuSign(cfg DocuSignConfig) (*DocuSignClient, error) {
	if cfg.AccessToken == "" || cfg.AccountID == "" || cfg.BaseURI == "" {
		return nil, fmt.Errorf("%w: docusign access_token + account_id + base_uri required", ErrNotConfigured)
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &DocuSignClient{cfg: cfg, http: hc}, nil
}

func (c *DocuSignClient) Provider() Provider { return ProviderDocuSign }

// Send creates an envelope with one document + the recipient list.
// Recipients flagged `Embedded` come back with a generated signing
// URL; email-only recipients receive the standard DocuSign email.
func (c *DocuSignClient) Send(ctx context.Context, req SendReq) (*SendResp, error) {
	docB64 := base64.StdEncoding.EncodeToString(req.DocumentBytes)
	signers := make([]map[string]any, 0, len(req.Recipients))
	for i, r := range req.Recipients {
		s := map[string]any{
			"email":        r.Email,
			"name":         r.Name,
			"recipientId":  fmt.Sprintf("%d", i+1),
			"routingOrder": fmt.Sprintf("%d", r.Order),
			"clientUserId": "", // set below for embedded
		}
		if r.Embedded {
			// `clientUserId` is what flips a recipient from email-
			// signed to embedded. The exact value doesn't matter
			// to DocuSign — just that it's stable per signer.
			s["clientUserId"] = req.RequestID + ":" + r.Email
		}
		signers = append(signers, s)
	}
	payload := map[string]any{
		"emailSubject": orDefault(req.Subject, "Please sign this document"),
		"emailBlurb":   req.Message,
		"status":       "sent",
		// customFields lets us pass our request_id through to the
		// webhook ParseWebhook so we can correlate without a separate
		// lookup table.
		"customFields": map[string]any{
			"textCustomFields": []map[string]any{
				{"name": "vaultdms_request_id", "value": req.RequestID, "show": "false", "required": "false"},
			},
		},
		"documents": []map[string]any{{
			"documentBase64": docB64,
			"name":           req.DocumentName,
			"fileExtension":  "pdf",
			"documentId":     "1",
		}},
		"recipients": map[string]any{"signers": signers},
	}
	var resp struct {
		EnvelopeID string `json:"envelopeId"`
		Status     string `json:"status"`
		StatusDateTime string `json:"statusDateTime"`
	}
	path := fmt.Sprintf("/restapi/v2.1/accounts/%s/envelopes", c.cfg.AccountID)
	if err := c.do(ctx, "POST", path, payload, &resp); err != nil {
		return nil, err
	}
	created, _ := time.Parse(time.RFC3339, resp.StatusDateTime)
	out := &SendResp{
		EnvelopeID: resp.EnvelopeID, Status: normalizeDocuSignStatus(resp.Status),
		SigningURLs: map[string]string{},
		CreatedAt:   created,
	}
	// Resolve embedded URLs in a second call per signer. Skipped
	// when nobody opted in to embedded signing.
	for _, r := range req.Recipients {
		if !r.Embedded {
			continue
		}
		urlStr, err := c.recipientView(ctx, resp.EnvelopeID, r, req)
		if err != nil {
			// Non-fatal: the email path still works; we just won't
			// embed for this signer. Log via the wrapping service.
			continue
		}
		out.SigningURLs[r.Email] = urlStr
	}
	return out, nil
}

func (c *DocuSignClient) recipientView(ctx context.Context, envelopeID string, r Recipient, req SendReq) (string, error) {
	body := map[string]any{
		"authenticationMethod": "none",
		"clientUserId":         req.RequestID + ":" + r.Email,
		"email":                r.Email,
		"userName":             r.Name,
		"returnUrl":            req.ReturnURL,
	}
	var resp struct {
		URL string `json:"url"`
	}
	path := fmt.Sprintf("/restapi/v2.1/accounts/%s/envelopes/%s/views/recipient", c.cfg.AccountID, envelopeID)
	if err := c.do(ctx, "POST", path, body, &resp); err != nil {
		return "", err
	}
	return resp.URL, nil
}

func (c *DocuSignClient) GetStatus(ctx context.Context, req StatusReq) (*StatusResp, error) {
	var raw struct {
		Status     string `json:"status"`
		StatusDateTime string `json:"statusDateTime"`
		Recipients struct {
			Signers []struct {
				Email      string `json:"email"`
				Status     string `json:"status"`
				SignedDate string `json:"signedDateTime"`
				ClientIP   string `json:"clientIp"`
			} `json:"signers"`
		} `json:"recipients"`
	}
	path := fmt.Sprintf("/restapi/v2.1/accounts/%s/envelopes/%s?include=recipients", c.cfg.AccountID, req.EnvelopeID)
	if err := c.do(ctx, "GET", path, nil, &raw); err != nil {
		return nil, err
	}
	updated, _ := time.Parse(time.RFC3339, raw.StatusDateTime)
	resp := &StatusResp{
		EnvelopeID: req.EnvelopeID,
		Status:     normalizeDocuSignStatus(raw.Status),
		UpdatedAt:  updated,
	}
	for _, s := range raw.Recipients.Signers {
		var signed *time.Time
		if s.SignedDate != "" {
			t, err := time.Parse(time.RFC3339, s.SignedDate)
			if err == nil {
				signed = &t
			}
		}
		resp.Recipients = append(resp.Recipients, RecipientStatus{
			Email: s.Email, Status: normalizeRecipientStatus(s.Status),
			SignedAt: signed, IPAddress: s.ClientIP,
		})
	}
	return resp, nil
}

// GetSignedDocument pulls combinedDocuments + Certificate of
// Completion in one PDF if IncludeCoC=true, else just the signed
// document. DocuSign returns a single combined PDF in either case;
// we surface CoCPDF as nil and SignedPDF as the combined bytes for
// simplicity.
func (c *DocuSignClient) GetSignedDocument(ctx context.Context, req GetSignedReq) (*GetSignedResp, error) {
	path := fmt.Sprintf("/restapi/v2.1/accounts/%s/envelopes/%s/documents/combined", c.cfg.AccountID, req.EnvelopeID)
	if req.IncludeCoC {
		path += "?certificate=true"
	}
	url := strings.TrimRight(c.cfg.BaseURI, "/") + path
	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.cfg.AccessToken)
	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%w: get signed: %v", ErrTransport, err)
	}
	defer func() { _ = httpResp.Body.Close() }()
	if httpResp.StatusCode == http.StatusNotFound {
		return nil, ErrUnknownEnvelope
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: get signed status %d", ErrTransport, httpResp.StatusCode)
	}
	buf := &bytes.Buffer{}
	if _, err := buf.ReadFrom(httpResp.Body); err != nil {
		return nil, err
	}
	return &GetSignedResp{
		SignedPDF:   buf.Bytes(),
		ContentType: "application/pdf",
	}, nil
}

// ParseWebhook verifies DocuSign Connect's HMAC and returns a
// normalized event. Connect signs over the full request body using
// HMAC-SHA256 + the configured secret; the sig arrives in the
// `X-DocuSign-Signature-1` header (base64-encoded).
func (c *DocuSignClient) ParseWebhook(headers http.Header, raw []byte) (*WebhookEvent, error) {
	if c.cfg.HMACSecret == "" {
		return nil, ErrNotConfigured
	}
	got := headers.Get("X-DocuSign-Signature-1")
	if got == "" {
		return nil, ErrSignatureMismatch
	}
	mac := hmac.New(sha256.New, []byte(c.cfg.HMACSecret))
	mac.Write(raw)
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(got), []byte(want)) {
		return nil, ErrSignatureMismatch
	}
	var msg struct {
		Event       string `json:"event"`
		EnvelopeID  string `json:"envelopeId"`
		EventID     string `json:"generatedDateTime"` // best correlation key in JSON Connect
		Status      string `json:"status"`
		EventDateTime string `json:"eventDateTime"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil, fmt.Errorf("docusign: decode webhook: %w", err)
	}
	occurred, _ := time.Parse(time.RFC3339, msg.EventDateTime)
	return &WebhookEvent{
		Provider:   ProviderDocuSign,
		EnvelopeID: msg.EnvelopeID,
		EventType:  "envelope." + strings.TrimPrefix(strings.ToLower(msg.Event), "envelope-"),
		ExternalID: msg.EnvelopeID + ":" + msg.EventID,
		OccurredAt: occurred,
		Status:     normalizeDocuSignStatus(msg.Status),
		RawPayload: raw,
	}, nil
}

// normalizeDocuSignStatus maps DocuSign's vocab to ours.
func normalizeDocuSignStatus(s string) string {
	switch strings.ToLower(s) {
	case "sent", "delivered":
		return "in_progress"
	case "completed":
		return "completed"
	case "declined":
		return "declined"
	case "voided":
		return "cancelled"
	case "expired":
		return "expired"
	case "created":
		return "pending"
	default:
		return "in_progress"
	}
}

func normalizeRecipientStatus(s string) string {
	switch strings.ToLower(s) {
	case "completed", "signed":
		return "signed"
	case "declined":
		return "declined"
	case "delivered", "sent":
		return "viewed"
	default:
		return "pending"
	}
}

func (c *DocuSignClient) do(ctx context.Context, method, path string, in, out any) error {
	var body *bytes.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	url := strings.TrimRight(c.cfg.BaseURI, "/") + path
	var reader *bytes.Reader
	if body != nil {
		reader = body
	}
	var req *http.Request
	var err error
	if reader != nil {
		req, err = http.NewRequestWithContext(ctx, method, url, reader)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, url, nil)
	}
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.AccessToken)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s %s: %v", ErrTransport, method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("%w: status %d", ErrUnauthorized, resp.StatusCode)
	}
	if resp.StatusCode == http.StatusNotFound {
		return ErrUnknownEnvelope
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%w: status %d", ErrTransport, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

var _ ESignClient = (*DocuSignClient)(nil)
