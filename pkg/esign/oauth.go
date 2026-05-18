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
	"io"
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
// redirected to. State binds the callback to (tenant, provider) so
// the callback handler can recover the provider — DocuSign + Adobe
// Sign both echo `state` back verbatim but do NOT include a provider
// query param of their own. The provider therefore has to ride
// inside `state`.
func (c OAuthConfig) AuthorizeURLBuilder(tenantID string) (string, string, error) {
	state, err := signState(tenantID, string(c.Provider), c.HMACSecret)
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

// ParseState returns the (tenantID, provider) tuple encoded in state
// WITHOUT verifying the HMAC. The callback handler uses this BEFORE
// it has a config (the provider tells it which OAuthConfig to load);
// the loaded config's VerifyState then checks the HMAC.
func ParseState(state string) (tenantID, provider string, ok bool) {
	first := strings.IndexByte(state, '.')
	if first < 0 {
		return "", "", false
	}
	second := strings.IndexByte(state[first+1:], '.')
	if second < 0 {
		// Legacy 2-part state from a previous build — no provider
		// encoded. Caller will have to fall back to ?provider= for
		// any in-flight flow started against the old format.
		return state[:first], "", false
	}
	tenantID = state[:first]
	provider = state[first+1 : first+1+second]
	return tenantID, provider, tenantID != "" && provider != ""
}

// VerifyState returns the bound tenant id when the HMAC checks out.
// Empty string + false on any failure (length, format, HMAC mismatch).
func (c OAuthConfig) VerifyState(state string) (string, bool) {
	first := strings.IndexByte(state, '.')
	if first < 0 {
		return "", false
	}
	second := strings.IndexByte(state[first+1:], '.')
	if second < 0 {
		return "", false
	}
	tenantID := state[:first]
	provider := state[first+1 : first+1+second]
	got := state[first+1+second+1:]
	mac := hmac.New(sha256.New, c.HMACSecret)
	mac.Write([]byte(tenantID + "|" + provider))
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(got), []byte(want)) {
		return "", false
	}
	return tenantID, true
}

func signState(tenantID, provider string, secret []byte) (string, error) {
	if len(secret) < 32 {
		return "", errors.New("oauth: HMAC secret must be ≥ 32 bytes")
	}
	if provider == "" {
		return "", errors.New("oauth: provider required")
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(tenantID + "|" + provider))
	return tenantID + "." + provider + "." + hex.EncodeToString(mac.Sum(nil)), nil
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

// FetchAccountInfo resolves the user's default DocuSign account_id +
// base_uri from /oauth/userinfo. DocuSign's token-exchange response
// does NOT carry these — they live behind a separate userinfo call.
// Without them the REST API URL (/restapi/v2.1/accounts/<id>/...)
// cannot be built and every Send fails with "account_id required".
//
// The userinfo host is derived from AuthorizeURL — same host, just
// the /oauth/userinfo path. Sandbox: account-d.docusign.com;
// production: account.docusign.com.
//
// Adobe Sign returns api_access_point in the token response itself,
// so this helper short-circuits for it.
func (c OAuthConfig) FetchAccountInfo(ctx context.Context, hc *http.Client, accessToken string) (accountID, baseURI string, err error) {
	if c.Provider == ProviderAdobeSign {
		// Adobe Sign: token-exchange already populated these via the
		// `api_access_point` field; FetchAccountInfo is a no-op.
		return "", "", nil
	}
	if c.Provider != ProviderDocuSign {
		return "", "", nil
	}
	// AuthorizeURL is like https://account-d.docusign.com/oauth/auth;
	// userinfo lives at the same host under /oauth/userinfo.
	authURL, err := url.Parse(c.AuthorizeURL)
	if err != nil {
		return "", "", fmt.Errorf("userinfo: parse authorize_url: %w", err)
	}
	uiURL := authURL.Scheme + "://" + authURL.Host + "/oauth/userinfo"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uiURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := hc.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("%w: userinfo: %v", ErrTransport, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("userinfo status %d: %s", resp.StatusCode, string(body))
	}
	var raw struct {
		Accounts []struct {
			AccountID   string `json:"account_id"`
			AccountName string `json:"account_name"`
			IsDefault   bool   `json:"is_default"`
			BaseURI     string `json:"base_uri"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", "", fmt.Errorf("userinfo: decode: %w", err)
	}
	if len(raw.Accounts) == 0 {
		return "", "", errors.New("userinfo: no accounts returned")
	}
	// Default account first; if none flagged default, take the first.
	for _, a := range raw.Accounts {
		if a.IsDefault {
			return a.AccountID, a.BaseURI, nil
		}
	}
	return raw.Accounts[0].AccountID, raw.Accounts[0].BaseURI, nil
}
