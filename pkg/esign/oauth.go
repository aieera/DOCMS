// OAuth2 helpers shared by both adapters.
//
// Each vendor uses authorization-code OAuth2 with HMAC-protected
// state. The shape is:
//
//   1. Admin clicks "Connect <vendor>" in /admin/integrations.
//   2. Frontend POSTs /api/v1/esign/oauth/start?provider=…
//   3. Handler builds AuthorizeURL(state) where state =
//      `<tenant>.<hmac>` so the callback can verify the redirect
//      came from us, not a malicious cross-site link.
//   4. Vendor redirects to /api/v1/esign/oauth/callback?code=…&state=…
//   5. Handler verifies state, exchanges code via TokenExchange,
//      persists the encrypted token row.
package esign

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// OAuthConfig holds the per-provider OAuth2 endpoints + client
// credentials. RedirectURI MUST be absolute (the vendor validates
// it against an exact match in their app config).
type OAuthConfig struct {
	Provider     Provider
	AuthorizeURL string
	TokenURL     string
	ClientID     string
	ClientSecret string
	RedirectURI  string
	Scope        string
	// HMACSecret signs the state parameter so a third party can't
	// craft a redirect that completes a connect for someone else.
	// Must be ≥ 32 bytes; service-layer factory enforces.
	HMACSecret []byte
}

// AuthorizeURLBuilder returns the URL the browser should be
// redirected to. State binds the callback to the requesting tenant.
func (c OAuthConfig) AuthorizeURLBuilder(tenantID string) (string, string, error) {
	state, err := signState(tenantID, c.HMACSecret)
	if err != nil {
		return "", "", err
	}
	q := url.Values{
		"response_type": {"code"},
		"client_id":     {c.ClientID},
		"redirect_uri":  {c.RedirectURI},
		"state":         {state},
	}
	if c.Scope != "" {
		q.Set("scope", c.Scope)
	}
	return c.AuthorizeURL + "?" + q.Encode(), state, nil
}

// VerifyState returns the bound tenant id when the HMAC checks out.
// Empty string + false on any failure (length, format, HMAC mismatch).
func (c OAuthConfig) VerifyState(state string) (string, bool) {
	dot := strings.IndexByte(state, '.')
	if dot < 0 {
		return "", false
	}
	tenantID := state[:dot]
	got := state[dot+1:]
	mac := hmac.New(sha256.New, c.HMACSecret)
	mac.Write([]byte(tenantID))
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(got), []byte(want)) {
		return "", false
	}
	return tenantID, true
}

func signState(tenantID string, secret []byte) (string, error) {
	if len(secret) < 32 {
		return "", errors.New("oauth: HMAC secret must be ≥ 32 bytes")
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(tenantID))
	return tenantID + "." + hex.EncodeToString(mac.Sum(nil)), nil
}

// TokenExchangeResult is the post-callback shape both vendors
// converge on. Adapters add their own discovery fields (DocuSign
// accountId, Adobe baseUri) to the response struct shape inside
// TokenExchange.
type TokenExchangeResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	Scope        string
	AccountID    string
	BaseURI      string
}

// TokenExchange swaps an authorization code for tokens. Used by both
// the initial OAuth callback and the periodic refresh-token rotator.
//
// `extra` lets adapters pass vendor-specific form fields (DocuSign
// requires `code_verifier` for PKCE in some flows; we don't use PKCE
// today but the hook is here).
func (c OAuthConfig) TokenExchange(ctx context.Context, hc *http.Client, code string, extra url.Values) (*TokenExchangeResult, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {c.ClientID},
		"client_secret": {c.ClientSecret},
		"redirect_uri":  {c.RedirectURI},
	}
	for k, v := range extra {
		form[k] = v
	}
	return doTokenRequest(ctx, hc, c.TokenURL, form)
}

// RefreshToken exchanges a refresh token for a new access token.
// Both vendors keep the refresh token across rotations — we only
// overwrite when the response includes a new one.
func (c OAuthConfig) RefreshToken(ctx context.Context, hc *http.Client, refresh string) (*TokenExchangeResult, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refresh},
		"client_id":     {c.ClientID},
		"client_secret": {c.ClientSecret},
	}
	return doTokenRequest(ctx, hc, c.TokenURL, form)
}

func doTokenRequest(ctx context.Context, hc *http.Client, tokenURL string, form url.Values) (*TokenExchangeResult, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: token: %v", ErrTransport, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%w: token status %d", ErrUnauthorized, resp.StatusCode)
	}
	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("%w: token status %d", ErrTransport, resp.StatusCode)
	}
	var raw struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Scope        string `json:"scope"`
		// DocuSign: returns accountId in the userInfo call, not here.
		// Adobe: api_access_point is in the token response.
		APIAccessPoint string `json:"api_access_point"`
		AccountID      string `json:"account_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("oauth: decode: %w", err)
	}
	return &TokenExchangeResult{
		AccessToken:  raw.AccessToken,
		RefreshToken: raw.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(raw.ExpiresIn) * time.Second),
		Scope:        raw.Scope,
		AccountID:    raw.AccountID,
		BaseURI:      raw.APIAccessPoint,
	}, nil
}

// NewHMACSecret returns 32 random bytes. Used by the service to
// initialize the OAuth state HMAC if no value is configured.
func NewHMACSecret() ([]byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}
