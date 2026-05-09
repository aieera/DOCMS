// Intesi Group SignHub adapter.
//
// API docs: https://docs.intesigroup.com/signhub/v1
//
// Intesi uses OAuth2 client_credentials for service-to-service +
// a separate user-facing redirect URL. The sandbox CA is NOT in
// Mozilla's bundle (see runbook), so Config.PinnedCAPEM swaps in a
// custom RootCAs. Don't ship without that pinned cert in prod —
// the validator will reject the chain.
package tsp

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"
)

type IntesiConfig struct {
	BaseURL      string
	ClientID     string
	ClientSecret string
	// PinnedCAPEM is the Intesi-issued CA bundle the sandbox uses.
	// Empty in prod (system trust store).
	PinnedCAPEM string
	HTTPClient  *http.Client
}

type IntesiClient struct {
	cfg  IntesiConfig
	http *http.Client

	mu          sync.Mutex
	cachedToken string
	tokenExpiry time.Time
}

func NewIntesi(cfg IntesiConfig) (*IntesiClient, error) {
	if cfg.BaseURL == "" || cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, fmt.Errorf("%w: intesi base_url + client creds required", ErrNotConfigured)
	}
	hc := cfg.HTTPClient
	if hc == nil {
		tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
		if cfg.PinnedCAPEM != "" {
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM([]byte(cfg.PinnedCAPEM)) {
				return nil, fmt.Errorf("intesi: failed to parse pinned CA")
			}
			tlsCfg.RootCAs = pool
		}
		hc = &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		}
	}
	return &IntesiClient{cfg: cfg, http: hc}, nil
}

func (c *IntesiClient) Provider() Provider { return ProviderIntesi }

// token returns a cached OAuth2 access_token, refreshing within ~60s
// of expiry. Concurrent callers serialize on a mutex — token endpoint
// rate-limits at the QTSP, so doing one refresh under contention is
// the right tradeoff.
func (c *IntesiClient) token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cachedToken != "" && time.Until(c.tokenExpiry) > time.Minute {
		return c.cachedToken, nil
	}
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {c.cfg.ClientID},
		"client_secret": {c.cfg.ClientSecret},
		"scope":         {"signature qes"},
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.cfg.BaseURL+"/oauth/token", bytes.NewBufferString(form.Encode()))
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
		return "", fmt.Errorf("intesi: decode token: %w", err)
	}
	c.cachedToken = t.AccessToken
	c.tokenExpiry = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
	return c.cachedToken, nil
}

func (c *IntesiClient) Register(ctx context.Context, req RegisterReq) (*RegisterResp, error) {
	body := map[string]any{
		"email":   req.SignerEmail,
		"name":    req.SignerName,
		"country": req.CountryCode,
	}
	var resp struct {
		SubjectID string `json:"subject_id"`
	}
	if err := c.do(ctx, "POST", "/v1/subjects", body, &resp); err != nil {
		return nil, err
	}
	if resp.SubjectID == "" {
		// Idempotent fallback: deterministic hash of email.
		resp.SubjectID = "intesi:" + req.SignerEmail
	}
	return &RegisterResp{SubjectID: resp.SubjectID}, nil
}

func (c *IntesiClient) Authorize(ctx context.Context, req AuthorizeReq) (*AuthorizeResp, error) {
	body := map[string]any{
		"subject_id":     req.SubjectID,
		"signer_email":   req.SignerEmail,
		"document_hash":  req.DocumentHash,
		"hash_algorithm": req.HashAlgo,
		"return_url":     req.ReturnURL,
		"signature_level": "PAdES-B-LT",
		"reason":         req.Reason,
		"location":       req.Location,
	}
	var resp struct {
		TransactionID string `json:"transaction_id"`
		ConsentURL    string `json:"consent_url"`
		ExpiresAt     string `json:"expires_at"`
	}
	if err := c.do(ctx, "POST", "/v1/transactions", body, &resp); err != nil {
		return nil, err
	}
	exp, _ := time.Parse(time.RFC3339, resp.ExpiresAt)
	if exp.IsZero() {
		exp = time.Now().Add(15 * time.Minute)
	}
	return &AuthorizeResp{RedirectURL: resp.ConsentURL, ExternalID: resp.TransactionID, ExpiresAt: exp}, nil
}

func (c *IntesiClient) Sign(ctx context.Context, req SignReq) (*SignResp, error) {
	body := map[string]any{
		"transaction_id": req.ExternalID,
		"auth_code":      req.AuthCode,
		"document_hash":  req.DocumentHash,
	}
	var resp struct {
		Status        string `json:"status"`
		SignedHashB64 string `json:"signed_hash"`
		Certificate   struct {
			LeafPEM   string    `json:"leaf_pem"`
			ChainPEM  string    `json:"chain_pem"`
			SubjectDN string    `json:"subject_dn"`
			IssuerDN  string    `json:"issuer_dn"`
			Serial    string    `json:"serial_hex"`
			NotBefore time.Time `json:"not_before"`
			NotAfter  time.Time `json:"not_after"`
		} `json:"certificate"`
		Revocation json.RawMessage `json:"revocation"`
	}
	if err := c.do(ctx, "POST", "/v1/transactions/sign", body, &resp); err != nil {
		return nil, err
	}
	if resp.Status == "expired" {
		return nil, ErrSessionExpired
	}
	if resp.Status == "consumed" {
		return nil, ErrAlreadyConsumed
	}
	if resp.Status != "completed" {
		return nil, fmt.Errorf("intesi sign: status=%s", resp.Status)
	}
	signed, err := decodeB64(resp.SignedHashB64)
	if err != nil {
		return nil, fmt.Errorf("intesi sign: decode: %w", err)
	}
	return &SignResp{
		SignedHash:    signed,
		CertPEM:       resp.Certificate.LeafPEM,
		ChainPEM:      resp.Certificate.ChainPEM,
		SubjectDN:     resp.Certificate.SubjectDN,
		IssuerDN:      resp.Certificate.IssuerDN,
		SerialHex:     resp.Certificate.Serial,
		NotBefore:     resp.Certificate.NotBefore,
		NotAfter:      resp.Certificate.NotAfter,
		LTVRevocation: resp.Revocation,
	}, nil
}

func (c *IntesiClient) Validate(ctx context.Context, req ValidateReq) (*ValidateResp, error) {
	body := map[string]any{"certificate_pem": req.CertPEM}
	var resp struct {
		Valid           bool       `json:"valid"`
		OnTrustList     bool       `json:"on_trust_list"`
		RevocationTime  *time.Time `json:"revocation_time,omitempty"`
		Reason          string     `json:"reason"`
	}
	if err := c.do(ctx, "POST", "/v1/validate", body, &resp); err != nil {
		return nil, err
	}
	return &ValidateResp{
		Valid:          resp.Valid,
		Reason:         resp.Reason,
		OnTrustList:    resp.OnTrustList,
		RevocationTime: resp.RevocationTime,
	}, nil
}

func (c *IntesiClient) do(ctx context.Context, method, path string, in, out any) error {
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
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s %s: %v", ErrTransport, method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		// Token might have been invalidated server-side — drop our
		// cache so the next call refreshes.
		c.mu.Lock()
		c.cachedToken = ""
		c.mu.Unlock()
		return fmt.Errorf("%w: status %d", ErrUnauthorized, resp.StatusCode)
	}
	if resp.StatusCode >= 500 {
		return fmt.Errorf("%w: status %d", ErrTransport, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("intesi: decode %s: %w", path, err)
	}
	return nil
}

var _ TSPClient = (*IntesiClient)(nil)
