// InfoCert GoSign adapter.
//
// API docs: https://api.test.infocert.it/swagger/index.html
//
// InfoCert sits closest to "OAuth2 + plain JSON" of the three. The
// only quirk vs. Intesi: each call carries an `org_id` header
// identifying the tenant under our partner contract. Without it,
// the QTSP returns 403.
package tsp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"
)

type InfoCertConfig struct {
	BaseURL      string
	ClientID     string
	ClientSecret string
	OrgID        string
	HTTPClient   *http.Client
}

type InfoCertClient struct {
	cfg  InfoCertConfig
	http *http.Client

	mu          sync.Mutex
	cachedToken string
	tokenExpiry time.Time
}

func NewInfoCert(cfg InfoCertConfig) (*InfoCertClient, error) {
	if cfg.BaseURL == "" || cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.OrgID == "" {
		return nil, fmt.Errorf("%w: infocert base_url + client creds + org_id required", ErrNotConfigured)
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &InfoCertClient{cfg: cfg, http: hc}, nil
}

func (c *InfoCertClient) Provider() Provider { return ProviderInfoCert }

func (c *InfoCertClient) token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cachedToken != "" && time.Until(c.tokenExpiry) > time.Minute {
		return c.cachedToken, nil
	}
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {c.cfg.ClientID},
		"client_secret": {c.cfg.ClientSecret},
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.cfg.BaseURL+"/auth/token", bytes.NewBufferString(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: token: %v", ErrTransport, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: token status %d", ErrUnauthorized, resp.StatusCode)
	}
	var t struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return "", fmt.Errorf("infocert: decode token: %w", err)
	}
	c.cachedToken = t.AccessToken
	c.tokenExpiry = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
	return c.cachedToken, nil
}

func (c *InfoCertClient) Register(ctx context.Context, req RegisterReq) (*RegisterResp, error) {
	body := map[string]any{
		"email":  req.SignerEmail,
		"name":   req.SignerName,
		"locale": req.CountryCode,
	}
	var resp struct {
		ID string `json:"id"`
	}
	if err := c.do(ctx, "POST", "/gosign/v1/users", body, &resp); err != nil {
		return nil, err
	}
	if resp.ID == "" {
		resp.ID = "infocert:" + req.SignerEmail
	}
	return &RegisterResp{SubjectID: resp.ID}, nil
}

func (c *InfoCertClient) Authorize(ctx context.Context, req AuthorizeReq) (*AuthorizeResp, error) {
	body := map[string]any{
		"user_id":      req.SubjectID,
		"hash":         req.DocumentHash,
		"hash_alg":     req.HashAlgo,
		"redirect_uri": req.ReturnURL,
		"signature_format": "PAdES",
	}
	var resp struct {
		SessionID    string    `json:"session_id"`
		AuthorizeURL string    `json:"authorize_url"`
		ExpiresAt    time.Time `json:"expires_at"`
	}
	if err := c.do(ctx, "POST", "/gosign/v1/sign/authorize", body, &resp); err != nil {
		return nil, err
	}
	exp := resp.ExpiresAt
	if exp.IsZero() {
		exp = time.Now().Add(15 * time.Minute)
	}
	return &AuthorizeResp{RedirectURL: resp.AuthorizeURL, ExternalID: resp.SessionID, ExpiresAt: exp}, nil
}

func (c *InfoCertClient) Sign(ctx context.Context, req SignReq) (*SignResp, error) {
	body := map[string]any{
		"session_id": req.ExternalID,
		"code":       req.AuthCode,
	}
	var resp struct {
		Status        string `json:"status"`
		Signature     string `json:"signature"`
		Certificate   string `json:"certificate"`
		Chain         string `json:"chain"`
		SubjectDN     string `json:"subject_dn"`
		IssuerDN      string `json:"issuer_dn"`
		SerialHex     string `json:"serial_hex"`
		NotBefore     time.Time `json:"not_before"`
		NotAfter      time.Time `json:"not_after"`
		Revocation    json.RawMessage `json:"revocation"`
	}
	if err := c.do(ctx, "POST", "/gosign/v1/sign/finalize", body, &resp); err != nil {
		return nil, err
	}
	switch resp.Status {
	case "expired":
		return nil, ErrSessionExpired
	case "consumed":
		return nil, ErrAlreadyConsumed
	case "completed":
		// fall through
	default:
		return nil, fmt.Errorf("infocert sign: status=%s", resp.Status)
	}
	signed, err := decodeB64(resp.Signature)
	if err != nil {
		return nil, fmt.Errorf("infocert sign: decode: %w", err)
	}
	return &SignResp{
		SignedHash:    signed,
		CertPEM:       resp.Certificate,
		ChainPEM:      resp.Chain,
		SubjectDN:     resp.SubjectDN,
		IssuerDN:      resp.IssuerDN,
		SerialHex:     resp.SerialHex,
		NotBefore:     resp.NotBefore,
		NotAfter:      resp.NotAfter,
		LTVRevocation: resp.Revocation,
	}, nil
}

func (c *InfoCertClient) Validate(ctx context.Context, req ValidateReq) (*ValidateResp, error) {
	body := map[string]any{"certificate": req.CertPEM}
	var resp struct {
		Valid          bool       `json:"valid"`
		OnTrustList    bool       `json:"on_trust_list"`
		RevocationTime *time.Time `json:"revocation_time,omitempty"`
		Reason         string     `json:"reason"`
	}
	if err := c.do(ctx, "POST", "/gosign/v1/validate", body, &resp); err != nil {
		return nil, err
	}
	return &ValidateResp{
		Valid:          resp.Valid,
		Reason:         resp.Reason,
		OnTrustList:    resp.OnTrustList,
		RevocationTime: resp.RevocationTime,
	}, nil
}

func (c *InfoCertClient) do(ctx context.Context, method, path string, in, out any) error {
	tok, err := c.token(ctx)
	if err != nil {
		return err
	}
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("X-InfoCert-Org", c.cfg.OrgID)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s %s: %v", ErrTransport, method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		c.mu.Lock()
		c.cachedToken = ""
		c.mu.Unlock()
		return fmt.Errorf("%w: status %d", ErrUnauthorized, resp.StatusCode)
	}
	if resp.StatusCode >= 500 {
		return fmt.Errorf("%w: status %d", ErrTransport, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("infocert: decode %s: %w", path, err)
	}
	return nil
}

var _ TSPClient = (*InfoCertClient)(nil)
