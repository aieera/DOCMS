// Adobe Sign (a.k.a. Acrobat Sign) REST API adapter.
//
// API docs: https://opensource.adobe.com/acrobat-sign/developer_guide/index.html
//
// Auth: OAuth2 authcode → bearer + refresh. Adobe returns
// `api_access_point` in the token exchange — that's the regional
// host all subsequent calls must use; we don't hardcode it.
//
// Webhook product: Adobe Sign Webhooks v1. Verifies via HMAC-SHA256
// over `clientId + clientSecret + payload` per their docs; the
// header is `x-adobesign-clientid` (an echo, not a sig) plus a
// signature in `x-adobesign-checksum-sha256`.
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

type AdobeSignConfig struct {
	AccessToken  string
	APIAccessPoint string // Adobe's regional base URI; from token response
	ClientID     string
	ClientSecret string
	HTTPClient   *http.Client
}

type AdobeSignClient struct {
	cfg  AdobeSignConfig
	http *http.Client
}

func NewAdobeSign(cfg AdobeSignConfig) (*AdobeSignClient, error) {
	if cfg.AccessToken == "" || cfg.APIAccessPoint == "" || cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, fmt.Errorf("%w: adobe_sign access_token + api_access_point + client creds required", ErrNotConfigured)
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &AdobeSignClient{cfg: cfg, http: hc}, nil
}

func (c *AdobeSignClient) Provider() Provider { return ProviderAdobeSign }

// Send creates a transient document then an agreement (Adobe's
// envelope equivalent). Two API calls; the second references the
// first by transientDocumentId.
func (c *AdobeSignClient) Send(ctx context.Context, req SendReq) (*SendResp, error) {
	transientID, err := c.uploadTransient(ctx, req.DocumentName, req.DocumentBytes)
	if err != nil {
		return nil, err
	}
	members := make([]map[string]any, 0, len(req.Recipients))
	for _, r := range req.Recipients {
		members = append(members, map[string]any{
			"memberInfos": []map[string]any{
				{"email": r.Email, "name": r.Name},
			},
			"order": r.Order,
			"role":  strings.ToUpper(r.Role),
		})
	}
	payload := map[string]any{
		"fileInfos":   []map[string]any{{"transientDocumentId": transientID}},
		"name":        orDefault(req.Subject, "Please sign this document"),
		"message":     req.Message,
		"signatureType": "ESIGN",
		"state":       "IN_PROCESS",
		"externalId": map[string]string{"id": req.RequestID},
		"participantSetsInfo": members,
	}
	var resp struct {
		ID string `json:"id"`
	}
	if err := c.do(ctx, "POST", "/api/rest/v6/agreements", payload, &resp); err != nil {
		return nil, err
	}
	return &SendResp{
		EnvelopeID: resp.ID,
		Status:     "in_progress",
		// Embedded URLs are resolved via /signingUrls per recipient
		// after the agreement is created — but Adobe only issues
		// them when state=IN_PROCESS and recipients have been
		// dispatched. We surface them via GetStatus's recipient
		// list later; embedded support for Adobe is best-effort
		// and mostly used for kiosk/POS, not the typical use case.
		SigningURLs: map[string]string{},
		CreatedAt:   time.Now().UTC(),
	}, nil
}

func (c *AdobeSignClient) uploadTransient(ctx context.Context, name string, body []byte) (string, error) {
	// multipart upload — keep this small + manual instead of
	// importing a multipart helper. Adobe's API expects exactly
	// the field names "File-Name", "Mime-Type", "File".
	var buf bytes.Buffer
	const boundary = "----vaultdms-esign-boundary"
	w := func(s string) { _, _ = buf.WriteString(s) }
	w("--" + boundary + "\r\n")
	w("Content-Disposition: form-data; name=\"File-Name\"\r\n\r\n")
	w(name + "\r\n")
	w("--" + boundary + "\r\n")
	w("Content-Disposition: form-data; name=\"Mime-Type\"\r\n\r\n")
	w("application/pdf\r\n")
	w("--" + boundary + "\r\n")
	w("Content-Disposition: form-data; name=\"File\"; filename=\"" + name + "\"\r\n")
	w("Content-Type: application/pdf\r\n\r\n")
	buf.Write(body)
	w("\r\n--" + boundary + "--\r\n")
	url := strings.TrimRight(c.cfg.APIAccessPoint, "/") + "/api/rest/v6/transientDocuments"
	req, err := http.NewRequestWithContext(ctx, "POST", url, &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.AccessToken)
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: transient upload: %v", ErrTransport, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: transient status %d", ErrTransport, resp.StatusCode)
	}
	var out struct {
		TransientDocumentID string `json:"transientDocumentId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.TransientDocumentID, nil
}

func (c *AdobeSignClient) GetStatus(ctx context.Context, req StatusReq) (*StatusResp, error) {
	var raw struct {
		ID       string `json:"id"`
		Status   string `json:"status"`
		ParticipantSetsInfo []struct {
			Status      string `json:"status"`
			MemberInfos []struct {
				Email     string `json:"email"`
				Status    string `json:"status"`
				CompletedDate string `json:"completedDate"`
			} `json:"memberInfos"`
		} `json:"participantSetsInfo"`
		StatusUpdateInfo struct {
			Date string `json:"date"`
		} `json:"statusUpdateInfo"`
	}
	path := "/api/rest/v6/agreements/" + req.EnvelopeID
	if err := c.do(ctx, "GET", path, nil, &raw); err != nil {
		return nil, err
	}
	updated, _ := time.Parse(time.RFC3339, raw.StatusUpdateInfo.Date)
	out := &StatusResp{
		EnvelopeID: req.EnvelopeID,
		Status:     normalizeAdobeStatus(raw.Status),
		UpdatedAt:  updated,
	}
	for _, set := range raw.ParticipantSetsInfo {
		for _, m := range set.MemberInfos {
			var signed *time.Time
			if m.CompletedDate != "" {
				t, err := time.Parse(time.RFC3339, m.CompletedDate)
				if err == nil {
					signed = &t
				}
			}
			out.Recipients = append(out.Recipients, RecipientStatus{
				Email: m.Email, Status: normalizeAdobeRecipientStatus(m.Status),
				SignedAt: signed,
			})
		}
	}
	return out, nil
}

// GetSignedDocument pulls the combined signed document; if
// IncludeCoC is true, additionally fetches the auditTrail PDF.
func (c *AdobeSignClient) GetSignedDocument(ctx context.Context, req GetSignedReq) (*GetSignedResp, error) {
	signed, err := c.fetchPDF(ctx, "/api/rest/v6/agreements/"+req.EnvelopeID+"/combinedDocument")
	if err != nil {
		return nil, err
	}
	out := &GetSignedResp{SignedPDF: signed, ContentType: "application/pdf"}
	if req.IncludeCoC {
		coc, err := c.fetchPDF(ctx, "/api/rest/v6/agreements/"+req.EnvelopeID+"/auditTrail")
		if err == nil {
			out.CoCPDF = coc
		}
		// Non-fatal — the signed doc is the primary artefact.
	}
	return out, nil
}

func (c *AdobeSignClient) fetchPDF(ctx context.Context, path string) ([]byte, error) {
	url := strings.TrimRight(c.cfg.APIAccessPoint, "/") + path
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.AccessToken)
	req.Header.Set("Accept", "application/pdf")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: fetch pdf: %v", ErrTransport, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrUnknownEnvelope
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: fetch pdf status %d", ErrTransport, resp.StatusCode)
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ParseWebhook verifies Adobe's HMAC.
//
// Adobe signs over `clientId + clientSecret + body`. The signature
// arrives in `x-adobesign-checksum-sha256` (base64). The clientId
// echo in `x-adobesign-clientid` is informational only — verifying
// the HMAC is what authenticates the request.
func (c *AdobeSignClient) ParseWebhook(headers http.Header, raw []byte) (*WebhookEvent, error) {
	if c.cfg.ClientID == "" || c.cfg.ClientSecret == "" {
		return nil, ErrNotConfigured
	}
	got := headers.Get("x-adobesign-checksum-sha256")
	if got == "" {
		return nil, ErrSignatureMismatch
	}
	mac := hmac.New(sha256.New, []byte(c.cfg.ClientSecret))
	mac.Write([]byte(c.cfg.ClientID))
	mac.Write(raw)
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(got), []byte(want)) {
		return nil, ErrSignatureMismatch
	}
	var msg struct {
		Event       string `json:"event"`
		EventDate   string `json:"eventDate"`
		WebhookID   string `json:"webhookId"`
		AgreementID string `json:"agreementId"`
		EventID     string `json:"id"`
		AgreementInfo struct {
			Status string `json:"status"`
		} `json:"agreement"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil, fmt.Errorf("adobe_sign: decode webhook: %w", err)
	}
	occurred, _ := time.Parse(time.RFC3339, msg.EventDate)
	envelopeID := msg.AgreementID
	return &WebhookEvent{
		Provider:   ProviderAdobeSign,
		EnvelopeID: envelopeID,
		EventType:  "envelope." + strings.ToLower(strings.TrimPrefix(msg.Event, "AGREEMENT_")),
		ExternalID: msg.EventID,
		OccurredAt: occurred,
		Status:     normalizeAdobeStatus(msg.AgreementInfo.Status),
		RawPayload: raw,
	}, nil
}

func normalizeAdobeStatus(s string) string {
	switch strings.ToUpper(s) {
	case "SIGNED", "APPROVED", "ACCEPTED", "FORMFILLED":
		return "completed"
	case "OUT_FOR_SIGNATURE", "OUT_FOR_APPROVAL", "OUT_FOR_DELIVERY", "OUT_FOR_FORM_FILLING":
		return "in_progress"
	case "CANCELLED":
		return "cancelled"
	case "EXPIRED":
		return "expired"
	case "DECLINED", "REJECTED":
		return "declined"
	case "DRAFT", "AUTHORING":
		return "pending"
	default:
		return "in_progress"
	}
}

func normalizeAdobeRecipientStatus(s string) string {
	switch strings.ToUpper(s) {
	case "COMPLETED", "SIGNED", "APPROVED":
		return "signed"
	case "DECLINED":
		return "declined"
	case "WAITING_FOR_MY_SIGNATURE", "WAITING_FOR_MY_APPROVAL", "WAITING_FOR_AUTHORING":
		return "viewed"
	default:
		return "pending"
	}
}

func (c *AdobeSignClient) do(ctx context.Context, method, path string, in, out any) error {
	var reader *bytes.Reader
	if in != nil {
		body, err := json.Marshal(in)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(body)
	}
	url := strings.TrimRight(c.cfg.APIAccessPoint, "/") + path
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

var _ ESignClient = (*AdobeSignClient)(nil)
