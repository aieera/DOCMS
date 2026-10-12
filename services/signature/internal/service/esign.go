// ADR 0071 — third-party connector orchestration.
//
// Three responsibilities:
//
//  1. OAuth lifecycle — start, callback, refresh, disconnect.
//  2. Send + ingest — call the connector when a request's provider
//     is docusign/adobe_sign, persist envelope id; when a webhook
//     lands, insert an event row + (on completed) pull the signed
//     PDF and hand off to the document service for "create new
//     version" through the existing version-uploaded path
//     (ADR 0021).
//  3. Reconcile — a 5-minute poll fills webhook gaps.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/esign"
	"github.com/aieera/sedoc/services/signature/internal/repository"
)

// ESignConfig is what main.go wires up at boot.
type ESignConfig struct {
	// SealingKey is the 32-byte AES-256 key used to seal access +
	// refresh tokens at rest. Derived from SEDOC_LOCAL_KEK.
	SealingKey []byte
	// OAuthByProvider holds the per-provider authorize/token URLs +
	// client creds + redirect_uri. The HMACSecret on each is the
	// state-binding secret; can be the same value across providers.
	OAuthByProvider map[esign.Provider]esign.OAuthConfig
	// DefaultRedirectURI is the OAuth callback to use when a tenant
	// has DB-saved credentials but no env-var entry to inherit the
	// redirect from. Same shape as the env-mode redirect URIs.
	DefaultRedirectURI string
	// DefaultHMACSecret signs the state parameter on OAuth start when
	// the tenant's DB config is used. Must be ≥32 bytes. Same value
	// across providers — state is provider-scoped via the tenant id.
	DefaultHMACSecret []byte
	// HTTPClient is shared across adapter constructions.
	HTTPClient *http.Client
	// PollInterval is the reconcile loop period. Default 5 min.
	PollInterval time.Duration
}

// RedirectURIFor returns the OAuth callback URL for the given provider.
// Prefers the env-mode per-provider value when present (so existing
// deployments keep working unchanged); falls back to DefaultRedirectURI
// for tenants connecting via the UI flow.
func (c *ESignConfig) RedirectURIFor(provider esign.Provider) string {
	if cfg, ok := c.OAuthByProvider[provider]; ok && cfg.RedirectURI != "" {
		return cfg.RedirectURI
	}
	return c.DefaultRedirectURI
}

// HMACSecretFor returns the state-signing HMAC for the given provider.
// Same precedence as RedirectURIFor.
func (c *ESignConfig) HMACSecretFor(provider esign.Provider) []byte {
	if cfg, ok := c.OAuthByProvider[provider]; ok && len(cfg.HMACSecret) > 0 {
		return cfg.HMACSecret
	}
	return c.DefaultHMACSecret
}

// AddESign wires the configuration into an existing Service. Called
// from main after construction.
func (s *Service) AddESign(cfg ESignConfig) {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 5 * time.Minute
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	s.esign = &cfg
}

// ----- OAuth ------------------------------------------------------

// resolveOAuthConfig returns the effective OAuth config for (tenant,
// provider). Priority order:
//
//  1. Per-tenant DB row in esign_provider_configs — paste-from-UI flow.
//  2. Env-var-configured OAuthByProvider — deployment-wide fallback.
//
// Mock mode is NOT considered here; StartOAuth/HandleOAuthCallback
// branch into mock-handling explicitly when the caller opts in.
func (s *Service) resolveOAuthConfig(ctx context.Context, tenantID string, provider esign.Provider) (esign.OAuthConfig, error) {
	if s.esign == nil {
		return esign.OAuthConfig{}, errors.New("esign not configured")
	}
	if row, err := s.repo.GetESignProviderConfig(ctx, tenantID, string(provider)); err == nil && row != nil {
		secret, err := esign.UnsealString(row.ClientSecretSealed, s.esign.SealingKey)
		if err != nil {
			return esign.OAuthConfig{}, fmt.Errorf("esign: unseal client_secret: %w", err)
		}
		authzURL, tokenURL := vendorEndpoints(provider, row.Environment, row.Region)
		// Defense-in-depth: only honor an override that still passes the domain
		// allowlist, so a row written before this validation existed can't
		// redirect the token exchange off-vendor. Otherwise keep vendorEndpoints.
		if row.AuthorizeURLOverride != "" && validateProviderOverride(provider, row.AuthorizeURLOverride) == nil {
			authzURL = row.AuthorizeURLOverride
		}
		if row.TokenURLOverride != "" && validateProviderOverride(provider, row.TokenURLOverride) == nil {
			tokenURL = row.TokenURLOverride
		}
		cfg := esign.OAuthConfig{
			Provider:     provider,
			AuthorizeURL: authzURL,
			TokenURL:     tokenURL,
			ClientID:     row.ClientID,
			ClientSecret: secret,
			RedirectURI:  s.esign.RedirectURIFor(provider),
			Scope:        defaultScope(provider),
			HMACSecret:   s.esign.HMACSecretFor(provider),
		}
		return cfg, nil
	}
	if cfg, ok := s.esign.OAuthByProvider[provider]; ok {
		return cfg, nil
	}
	return esign.OAuthConfig{}, fmt.Errorf("esign: no credentials configured for %q (save them via /admin/integrations)", provider)
}

// vendorEndpoints maps environment + region to vendor OAuth hosts so
// the admin UI doesn't have to ship 4 URL fields per provider. The
// sandbox/production radio in the modal selects one of the two pairs.
// allowedOverrideSuffixes lists the domain families a per-tenant OAuth endpoint
// override may point at, per provider. Restricting overrides to the vendor's own
// domains stops a tenant admin — or anyone who reaches this write path — from
// redirecting the client_secret / auth-code exchange (which carries the sealed
// credentials) to an arbitrary attacker host: SSRF + credential/token exfil.
var allowedOverrideSuffixes = map[esign.Provider][]string{
	esign.ProviderDocuSign:  {"docusign.com", "docusign.net"},
	esign.ProviderAdobeSign: {"adobesign.com", "echosign.com"},
}

// validateProviderOverride rejects an OAuth endpoint override that is not an
// absolute https URL on one of the provider's own domains. Empty = no override.
func validateProviderOverride(provider esign.Provider, rawURL string) error {
	if rawURL == "" {
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("must be an absolute https URL")
	}
	host := strings.ToLower(u.Hostname())
	for _, suf := range allowedOverrideSuffixes[provider] {
		if host == suf || strings.HasSuffix(host, "."+suf) {
			return nil
		}
	}
	return fmt.Errorf("host %q is not an allowed %s endpoint", host, provider)
}

func vendorEndpoints(provider esign.Provider, environment, region string) (authorize, token string) {
	switch provider {
	case esign.ProviderDocuSign:
		if environment == "production" {
			return "https://account.docusign.com/oauth/auth",
				"https://account.docusign.com/oauth/token"
		}
		return "https://account-d.docusign.com/oauth/auth",
			"https://account-d.docusign.com/oauth/token"
	case esign.ProviderAdobeSign:
		host := "secure.na1.adobesign.com"
		if region != "" {
			host = "secure." + region + ".adobesign.com"
		}
		return "https://" + host + "/public/oauth/v2",
			"https://" + host + "/oauth/v2/token"
	}
	return "", ""
}

func defaultScope(provider esign.Provider) string {
	switch provider {
	case esign.ProviderDocuSign:
		return "signature"
	case esign.ProviderAdobeSign:
		return "agreement_send agreement_read"
	}
	return ""
}

// SaveProviderConfig stores per-tenant OAuth client credentials. The
// secret is sealed before persistence. Called from the admin Connect
// modal before kicking off the OAuth handshake.
func (s *Service) SaveProviderConfig(ctx context.Context, tenantID, userID string, provider esign.Provider, clientID, clientSecret, environment, region, authorizeOverride, tokenOverride string) error {
	if s.esign == nil {
		return errors.New("esign not configured")
	}
	if clientID == "" || clientSecret == "" {
		return errors.New("client_id and client_secret are required")
	}
	if environment != "sandbox" && environment != "production" {
		return errors.New("environment must be 'sandbox' or 'production'")
	}
	// Constrain the endpoint overrides to the vendor's own domains; the token
	// exchange POSTs the client_secret to TokenURL, so an arbitrary host is a
	// credential-exfiltration + SSRF primitive.
	if err := validateProviderOverride(provider, authorizeOverride); err != nil {
		return fmt.Errorf("authorize_url_override: %w", err)
	}
	if err := validateProviderOverride(provider, tokenOverride); err != nil {
		return fmt.Errorf("token_url_override: %w", err)
	}
	sealed, err := esign.SealString([]byte(clientSecret), s.esign.SealingKey)
	if err != nil {
		return fmt.Errorf("seal client_secret: %w", err)
	}
	row := &repository.ESignProviderConfig{
		TenantID:             tenantID,
		Provider:             string(provider),
		ClientID:             clientID,
		ClientSecretSealed:   sealed,
		Environment:          environment,
		Region:               region,
		AuthorizeURLOverride: authorizeOverride,
		TokenURLOverride:     tokenOverride,
		ConfiguredBy:         userID,
	}
	if err := s.repo.UpsertESignProviderConfig(ctx, row); err != nil {
		return fmt.Errorf("save provider config: %w", err)
	}
	// Clear any leftover mock-account token row so the Connections
	// page no longer claims "connected (mock-account)" once real creds
	// are saved. The next OAuth round-trip writes a real token row.
	if existing, err := s.repo.GetESignToken(ctx, tenantID, string(provider)); err == nil && existing != nil && existing.AccountID == "mock-account" {
		_ = s.repo.DeleteESignToken(ctx, tenantID, string(provider))
	}
	return nil
}

// GetProviderConfig returns the per-tenant config in a UI-safe shape:
// secret is NEVER returned. Used by the admin modal to pre-fill the
// Integration Key field when re-opening.
func (s *Service) GetProviderConfig(ctx context.Context, tenantID string, provider esign.Provider) (*ProviderConfigPublic, error) {
	row, err := s.repo.GetESignProviderConfig(ctx, tenantID, string(provider))
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	return &ProviderConfigPublic{
		Provider:    row.Provider,
		ClientID:    row.ClientID,
		HasSecret:   row.ClientSecretSealed != "",
		Environment: row.Environment,
		Region:      row.Region,
		UpdatedAt:   row.UpdatedAt,
	}, nil
}

// DeleteProviderConfig removes a tenant's saved OAuth client creds.
// Also clears the matching esign_oauth_tokens row so the Connections
// list doesn't keep showing "connected" with a token signed by creds
// the tenant just deleted (the token couldn't be refreshed anyway).
func (s *Service) DeleteProviderConfig(ctx context.Context, tenantID string, provider esign.Provider) error {
	if err := s.repo.DeleteESignProviderConfig(ctx, tenantID, string(provider)); err != nil {
		return err
	}
	_ = s.repo.DeleteESignToken(ctx, tenantID, string(provider))
	return nil
}

// ProviderConfigPublic is the secret-stripped view of a saved config.
// The secret is sealed in DB and never travels back to the browser —
// re-entering it is required to change it.
type ProviderConfigPublic struct {
	Provider    string    `json:"provider"`
	ClientID    string    `json:"client_id"`
	HasSecret   bool      `json:"has_secret"`
	Environment string    `json:"environment"`
	Region      string    `json:"region,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// oauthStateTTL bounds how long a started e-sign connect stays completable.
const oauthStateTTL = 10 * time.Minute

// StartOAuth returns the URL to redirect the admin's browser to.
//
// Connecting a vendor account binds every future envelope of the tenant to
// it, so it is an owner/admin action (S-2). The state is a fresh random
// nonce, stored hashed and bound to (tenant, admin, provider) for
// oauthStateTTL; HandleOAuthCallback consumes it exactly once.
func (s *Service) StartOAuth(ctx context.Context, actor Actor, provider esign.Provider) (string, error) {
	if s.esign == nil {
		return "", errors.New("esign not configured")
	}
	if !isTenantAdmin(actor.Role) {
		return "", fmt.Errorf("%w: only an owner or admin may connect an e-sign provider", ErrForbidden)
	}
	cfg, err := s.resolveOAuthConfig(ctx, actor.TenantID, provider)
	if err != nil {
		return "", err
	}
	nonce, err := esign.NewStateNonce()
	if err != nil {
		return "", err
	}
	if err := s.repo.CreateOAuthState(ctx, actor.TenantID, actor.UserID, string(provider),
		esign.HashStateNonce(nonce), time.Now().UTC().Add(oauthStateTTL)); err != nil {
		return "", fmt.Errorf("esign: persist oauth state: %w", err)
	}
	return cfg.AuthorizeURLWithState(esign.NonceState(actor.TenantID, provider, nonce)), nil
}

// errOAuthState is the single answer for any state that does not consume a
// stored nonce — malformed, forged, replayed, expired, another tenant's or
// another admin's — so the callback reveals nothing about which.
var errOAuthState = fmt.Errorf("%w: e-sign connect state is invalid, expired or already used — start the connection again", ErrInvalidRequest)

// HandleOAuthCallback exchanges the auth code for tokens, persists
// them sealed, and returns the user-facing redirect URL the handler
// should 302 the admin to. actor is the session that landed on the
// callback; the state's nonce must have been minted for that very tenant,
// user and provider, and is consumed before the code is exchanged.
func (s *Service) HandleOAuthCallback(ctx context.Context, actor Actor, code, state string) (string, error) {
	if s.esign == nil {
		return "", errors.New("esign not configured")
	}
	if !isTenantAdmin(actor.Role) {
		return "", fmt.Errorf("%w: only an owner or admin may connect an e-sign provider", ErrForbidden)
	}
	tenantID, provider, nonce, ok := esign.ParseNonceState(state)
	if !ok || tenantID != actor.TenantID {
		return "", errOAuthState
	}
	consumed, err := s.repo.ConsumeOAuthState(ctx, tenantID, actor.UserID, string(provider), esign.HashStateNonce(nonce))
	if err != nil {
		return "", fmt.Errorf("esign: consume oauth state: %w", err)
	}
	if !consumed {
		return "", errOAuthState
	}
	cfg, err := s.resolveOAuthConfig(ctx, tenantID, provider)
	if err != nil {
		return "", err
	}
	tok, err := cfg.TokenExchange(ctx, s.esign.HTTPClient, code, nil)
	if err != nil {
		return "", fmt.Errorf("esign: exchange: %w", err)
	}
	access, err := esign.SealString([]byte(tok.AccessToken), s.esign.SealingKey)
	if err != nil {
		return "", err
	}
	refresh, err := esign.SealString([]byte(tok.RefreshToken), s.esign.SealingKey)
	if err != nil {
		return "", err
	}
	// DocuSign's token-exchange response carries neither account_id
	// nor base_uri — both live behind a separate /oauth/userinfo
	// call. Without them every Send fails with "account_id required",
	// so resolve them here before the row is written. Adobe Sign
	// returns api_access_point in the token itself; FetchAccountInfo
	// is a no-op for that provider.
	accountID, baseURI := tok.AccountID, tok.BaseURI
	if accountID == "" || baseURI == "" {
		acc, base, err := cfg.FetchAccountInfo(ctx, s.esign.HTTPClient, tok.AccessToken)
		if err != nil {
			return "", fmt.Errorf("esign: fetch account info: %w", err)
		}
		if accountID == "" {
			accountID = acc
		}
		if baseURI == "" {
			baseURI = base
		}
	}
	row := &repository.ESignToken{
		TenantID: tenantID, Provider: string(provider),
		AccessToken: access, RefreshToken: refresh,
		ExpiresAt: tok.ExpiresAt, AccountID: accountID,
		BaseURI: baseURI, Scope: tok.Scope,
		ConnectedBy: actor.UserID,
		ConnectedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.repo.UpsertESignToken(ctx, row); err != nil {
		return "", err
	}
	return "/admin/integrations?connected=" + string(provider), nil
}

// ListConnections returns sanitized status (no tokens) for the
// admin UI.
func (s *Service) ListConnections(ctx context.Context, tenantID string) ([]*repository.ESignToken, error) {
	return s.repo.ListESignTokens(ctx, tenantID)
}

// RefreshAccessToken trades the stored refresh token for a fresh
// access token + (when the vendor rotates it) a new refresh token.
// The new row keeps every field that wasn't replaced — account_id,
// base_uri, scope, connected_by — so the UI's connected-by-and-when
// stays intact across silent refreshes. Returns the persisted token
// or an error classifying the failure (so the caller can surface
// "refresh token revoked → admin must reconnect" distinctly from
// transport errors that warrant a retry).
func (s *Service) RefreshAccessToken(ctx context.Context, tenantID string, provider esign.Provider) (*repository.ESignToken, error) {
	if s.esign == nil {
		return nil, errors.New("esign not configured")
	}
	cur, err := s.repo.GetESignToken(ctx, tenantID, string(provider))
	if err != nil || cur == nil {
		return nil, fmt.Errorf("esign: no token for %s/%s", tenantID, provider)
	}
	if cur.RefreshToken == "" {
		return nil, errors.New("esign: refresh token absent; admin must reconnect")
	}
	plainRefresh, err := esign.UnsealString(cur.RefreshToken, s.esign.SealingKey)
	if err != nil {
		return nil, fmt.Errorf("esign: unseal refresh: %w", err)
	}
	cfg, err := s.resolveOAuthConfig(ctx, tenantID, provider)
	if err != nil {
		return nil, err
	}
	res, err := cfg.RefreshToken(ctx, s.esign.HTTPClient, plainRefresh)
	if err != nil {
		return nil, fmt.Errorf("esign: refresh: %w", err)
	}
	access, err := esign.SealString([]byte(res.AccessToken), s.esign.SealingKey)
	if err != nil {
		return nil, err
	}
	// DocuSign rotates the refresh token on every exchange; Adobe
	// keeps it. Preserve the existing one when the response doesn't
	// include a new value so we don't overwrite a valid refresh
	// token with the empty string.
	refresh := cur.RefreshToken
	if res.RefreshToken != "" {
		sealed, sErr := esign.SealString([]byte(res.RefreshToken), s.esign.SealingKey)
		if sErr != nil {
			return nil, sErr
		}
		refresh = sealed
	}
	row := &repository.ESignToken{
		TenantID:     tenantID,
		Provider:     string(provider),
		AccessToken:  access,
		RefreshToken: refresh,
		ExpiresAt:    res.ExpiresAt,
		AccountID:    cur.AccountID,
		BaseURI:      cur.BaseURI,
		Scope:        cur.Scope,
		ConnectedBy:  cur.ConnectedBy,
		ConnectedAt:  cur.ConnectedAt,
		UpdatedAt:    time.Now().UTC(),
	}
	if err := s.repo.UpsertESignToken(ctx, row); err != nil {
		return nil, err
	}
	return row, nil
}

// RefreshDueTokens scans every tenant's eSign tokens and refreshes
// any that are within `window` of their expiry. Errors per token are
// logged but don't fail the sweep. Intended to be called from a
// ticker loop in signature/cmd/server/main.go.
func (s *Service) RefreshDueTokens(ctx context.Context, window time.Duration) (int, int, error) {
	if s.esign == nil {
		return 0, 0, errors.New("esign not configured")
	}
	due, err := s.repo.ListESignTokensExpiringWithin(ctx, window)
	if err != nil {
		return 0, 0, err
	}
	ok, fail := 0, 0
	for _, t := range due {
		if _, err := s.RefreshAccessToken(ctx, t.TenantID, esign.Provider(t.Provider)); err != nil {
			fail++
			continue
		}
		ok++
	}
	return ok, fail, nil
}

// Disconnect clears the connection. Owner/admin only (S-2).
func (s *Service) Disconnect(ctx context.Context, actor Actor, provider esign.Provider) error {
	if !isTenantAdmin(actor.Role) {
		return fmt.Errorf("%w: only an owner or admin may disconnect an e-sign provider", ErrForbidden)
	}
	return s.repo.DeleteESignToken(ctx, actor.TenantID, string(provider))
}

// ----- Send + status ----------------------------------------------

// resolveClient returns a configured connector for the request's
// provider. Refreshes the token if it's within 5 min of expiry —
// no separate goroutine, the refresh is lazy on the next request.
func (s *Service) resolveClient(ctx context.Context, tenantID string, provider esign.Provider) (esign.ESignClient, error) {
	if s.esign == nil {
		return nil, errors.New("esign not configured")
	}
	row, err := s.repo.GetESignToken(ctx, tenantID, string(provider))
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("esign: provider %q not connected for tenant", provider)
	}
	if time.Until(row.ExpiresAt) < 5*time.Minute {
		if err := s.refresh(ctx, row, provider); err != nil {
			return nil, err
		}
	}
	access, err := esign.UnsealString(row.AccessToken, s.esign.SealingKey)
	if err != nil {
		return nil, err
	}
	switch provider {
	case esign.ProviderDocuSign:
		return esign.NewDocuSign(esign.DocuSignConfig{
			AccessToken: access, AccountID: row.AccountID, BaseURI: row.BaseURI,
			HMACSecret: s.esign.OAuthByProvider[provider].ClientSecret,
			HTTPClient: s.esign.HTTPClient,
		})
	case esign.ProviderAdobeSign:
		// The tenant's stored provider config (else the deployment-wide
		// one) — the same credentials the webhook check uses. Reading only
		// the env config left per-tenant Adobe integrations without a
		// client id/secret, so every vendor call failed ErrNotConfigured.
		oc, err := s.resolveOAuthConfig(ctx, tenantID, provider)
		if err != nil {
			return nil, err
		}
		return esign.NewAdobeSign(esign.AdobeSignConfig{
			AccessToken: access, APIAccessPoint: row.BaseURI,
			ClientID: oc.ClientID, ClientSecret: oc.ClientSecret,
			HTTPClient: s.esign.HTTPClient,
		})
	default:
		return nil, fmt.Errorf("esign: unknown provider %q", provider)
	}
}

func (s *Service) refresh(ctx context.Context, row *repository.ESignToken, provider esign.Provider) error {
	cfg := s.esign.OAuthByProvider[provider]
	current, err := esign.UnsealString(row.RefreshToken, s.esign.SealingKey)
	if err != nil {
		return err
	}
	tok, err := cfg.RefreshToken(ctx, s.esign.HTTPClient, current)
	if err != nil {
		return err
	}
	access, err := esign.SealString([]byte(tok.AccessToken), s.esign.SealingKey)
	if err != nil {
		return err
	}
	refresh := row.RefreshToken
	if tok.RefreshToken != "" {
		refresh, err = esign.SealString([]byte(tok.RefreshToken), s.esign.SealingKey)
		if err != nil {
			return err
		}
	}
	row.AccessToken = access
	row.RefreshToken = refresh
	row.ExpiresAt = tok.ExpiresAt
	row.UpdatedAt = time.Now().UTC()
	return s.repo.UpsertESignToken(ctx, row)
}

// esignClient returns the vendor client for (tenant, provider).
func (s *Service) esignClient(ctx context.Context, tenantID string, provider esign.Provider) (esign.ESignClient, error) {
	if s.esignClientFn != nil {
		return s.esignClientFn(ctx, tenantID, provider)
	}
	return s.resolveClient(ctx, tenantID, provider)
}

// SendESignInput is an e-sign send. It deliberately carries no document
// bytes, hash or recipient list (DEC-17): the request row decides what is
// sent and to whom.
type SendESignInput struct {
	Actor     Actor
	RequestID string
	// Provider is optional; when set it must match the request's provider.
	Provider     esign.Provider
	DocumentName string
	Subject      string
	Message      string
	ReturnURL    string
	// EmbeddedSignerIDs selects which of the request's signers get an
	// embedded (in-app) signing URL instead of the vendor's email.
	EmbeddedSignerIDs []string
}

// SendViaProvider creates the vendor envelope for an existing request and
// stamps its envelope id onto the request (status → in_progress).
//
// Review finding S-1: the old version never loaded the request, so any
// member could rebind any request to their own envelope and have the
// completed envelope's bytes ingested as a new version of the victim's
// document under the creator's identity. Now: the request is loaded in the
// caller's tenant; only its creator or an owner/admin may send it; it must
// be open, unsent and of a vendor provider; the bytes are the request's own
// (document, version) read as the caller; recipients are the request's
// signer rows; and the envelope id is stamped with a conditional update.
func (s *Service) SendViaProvider(ctx context.Context, in SendESignInput) (*esign.SendResp, error) {
	tenantID := in.Actor.TenantID
	req, err := s.repo.GetByID(ctx, tenantID, in.RequestID)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, ErrRequestNotFound
	}
	if req.CreatedBy != in.Actor.UserID && !isTenantAdmin(in.Actor.Role) {
		return nil, fmt.Errorf("%w: only the request creator or an admin may send it", ErrForbidden)
	}
	provider := esign.Provider(req.Provider)
	if provider != esign.ProviderDocuSign && provider != esign.ProviderAdobeSign {
		return nil, fmt.Errorf("%w: request provider %q is not an e-sign vendor", ErrInvalidRequest, req.Provider)
	}
	if in.Provider != "" && in.Provider != provider {
		return nil, fmt.Errorf("%w: request was created for %s, not %s", ErrInvalidRequest, provider, in.Provider)
	}
	if err := checkSignable(req, time.Now().UTC()); err != nil {
		return nil, err
	}
	if req.Status != "pending" || req.ExternalID != "" {
		return nil, fmt.Errorf("%w: request was already sent", ErrRequestNotSignable)
	}

	docBytes, err := s.versionBytes(ctx, in.Actor, req.DocumentID, req.VersionID)
	if err != nil {
		return nil, err
	}
	embedded := make(map[string]bool, len(in.EmbeddedSignerIDs))
	for _, id := range in.EmbeddedSignerIDs {
		embedded[id] = true
	}
	recipients := make([]esign.Recipient, 0, len(req.Signers))
	for _, sg := range req.Signers {
		recipients = append(recipients, esign.Recipient{
			Name: sg.Name, Email: sg.Email, Order: sg.Order, Role: sg.Role,
			Embedded: embedded[sg.ID],
		})
	}
	docName := in.DocumentName
	if docName == "" {
		docName = "document.pdf"
	}

	client, err := s.esignClient(ctx, tenantID, provider)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUpstream, err)
	}
	resp, err := client.Send(ctx, esign.SendReq{
		TenantID: tenantID, RequestID: req.ID,
		Subject: in.Subject, Message: in.Message,
		DocumentName: docName, DocumentBytes: docBytes,
		Recipients: recipients, ReturnURL: in.ReturnURL,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUpstream, err)
	}
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return nil, err
	}
	err = database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		return s.repo.SetEnvelopeIDTx(ctx, tx, tenantID, req.ID, resp.EnvelopeID)
	})
	if errors.Is(err, repository.ErrStateConflict) {
		// A concurrent send or cancel won. The envelope we just created is
		// orphaned at the vendor; log it so an operator can void it.
		s.log.Error().Str("request_id", req.ID).Str("envelope_id", resp.EnvelopeID).
			Msg("esign send lost a race; vendor envelope orphaned")
		return nil, fmt.Errorf("%w: request changed while sending", ErrRequestNotSignable)
	}
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// isTenantAdmin reports whether role carries tenant-administrator rights.
func isTenantAdmin(role string) bool { return role == "owner" || role == "admin" }

// IngestWebhook handles an inbound vendor webhook. The handler has
// already verified HMAC; we dedup at the DB and (on completed)
// pull the signed PDF for the document service.
func (s *Service) IngestWebhook(ctx context.Context, tenantID string, evt *esign.WebhookEvent) error {
	requestID, err := s.repo.FindRequestByEnvelope(ctx, tenantID, evt.EnvelopeID)
	if err != nil {
		return err
	}
	if requestID == "" {
		// Dropped: webhook for an envelope we don't know about.
		// Could be a stale Connect subscription pointing at an
		// older system; safe to ignore.
		s.log.Warn().Str("envelope_id", evt.EnvelopeID).Msg("esign webhook for unknown envelope")
		return nil
	}
	row := &repository.ESignEvent{
		ID: repository.NewID(), TenantID: tenantID,
		RequestID: requestID, Provider: string(evt.Provider),
		EnvelopeID: evt.EnvelopeID, EventType: evt.EventType,
		ExternalID: evt.ExternalID, RawPayload: evt.RawPayload,
		ReceivedAt: time.Now().UTC(),
	}
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return err
	}
	err = database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		// The insert runs in a savepoint: a unique violation aborts the
		// enclosing Postgres transaction, so "treat as success" used to
		// fail at commit ("commit unexpectedly resulted in rollback") and
		// answer every redelivery with a 500 — a deferred completion could
		// then never be ingested on retry.
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		if err := s.repo.InsertESignEventTx(ctx, sp, row); err != nil {
			_ = sp.Rollback(ctx)
			// Unique-violation = redelivery; treat as success.
			if isUniqueViolation(err) {
				s.log.Debug().Str("external_id", evt.ExternalID).Msg("esign webhook dedup")
				return nil
			}
			return err
		}
		if err := sp.Commit(ctx); err != nil {
			return err
		}
		// Audit-trail outbox event so the platform-wide audit feed
		// has the vendor signal too.
		audit, _ := json.Marshal(map[string]any{
			"specversion": "1.0", "type": "dms.audit.esign.event.v1",
			"source": "/vaultdms/signature",
			"data": map[string]string{
				"tenant_id":   tenantID,
				"request_id":  requestID,
				"provider":    string(evt.Provider),
				"envelope_id": evt.EnvelopeID,
				"event_type":  evt.EventType,
				"status":      evt.Status,
			},
		})
		reqUUID, _ := uuid.Parse(requestID)
		out := database.NewOutboxEvent(tenantUUID, "dms.audit.esign.event.v1",
			"signature_request", reqUUID, audit)
		return s.outbox.Insert(ctx, tx, out)
	})
	if err != nil {
		return err
	}
	if evt.Status == "completed" {
		if evt.Provider == esign.ProviderAdobeSign {
			// Adobe notifications carry no MAC (only the client id), so the
			// completion is confirmed with the vendor before anything is
			// pulled or written; an unconfirmed one is answered with a
			// retryable error and redelivered.
			if err := s.confirmAdobeCompletion(ctx, tenantID, evt.EnvelopeID); err != nil {
				s.log.Warn().Err(err).Str("request_id", requestID).Str("envelope_id", evt.EnvelopeID).
					Msg("esign webhook: completion not confirmed by Adobe; deferring ingest")
				return err
			}
		}
		if err := s.ingestSignedDocument(ctx, tenantID, requestID, evt.EnvelopeID, evt.Provider); err != nil {
			s.log.Error().Err(err).Str("request_id", requestID).Msg("esign ingest signed pdf")
			return err
		}
	}
	return nil
}

// ErrVendorCompletionUnconfirmed: a webhook reported completion that the
// vendor's API does not (yet) confirm. Not acknowledged, so it is retried.
var ErrVendorCompletionUnconfirmed = errors.New("esign: vendor has not confirmed the completion")

// adobeConfirmer is the Adobe-only completion check (pkg/esign).
type adobeConfirmer interface {
	ConfirmCompleted(ctx context.Context, agreementID string) (bool, error)
}

func (s *Service) confirmAdobeCompletion(ctx context.Context, tenantID, agreementID string) error {
	client, err := s.esignClient(ctx, tenantID, esign.ProviderAdobeSign)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrVendorCompletionUnconfirmed, err)
	}
	c, ok := client.(adobeConfirmer)
	if !ok {
		return fmt.Errorf("%w: client cannot confirm completion", ErrVendorCompletionUnconfirmed)
	}
	done, err := c.ConfirmCompleted(ctx, agreementID)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrVendorCompletionUnconfirmed, err)
	}
	if !done {
		return ErrVendorCompletionUnconfirmed
	}
	return nil
}

// ingestSignedDocument pulls the signed PDF + CoC from the vendor
// and hands off to the document service so the signed bytes
// materialize as a real new version (ADR 0021 path) rather than
// dying as byte counts in an outbox event.
//
// Idempotency: a redelivered webhook hits this twice. First call
// flips signature_requests.status to 'completed'; subsequent calls
// see status='completed' upfront and short-circuit. This lets us
// re-run safely without uploading duplicate blobs or minting
// duplicate versions.
//
// When the ingest pipeline isn't configured (storage / document
// services unreachable at boot), we still flip the status + emit
// the outbox event so observability survives. The new-version
// hand-off is the only thing that's skipped.
func (s *Service) ingestSignedDocument(ctx context.Context, tenantID, requestID, envelopeID string, provider esign.Provider) error {
	// Pre-check: short-circuit on already-completed requests.
	existing, err := s.repo.GetByID(ctx, tenantID, requestID)
	if err != nil {
		return err
	}
	if existing == nil {
		return errors.New("ingest: request not found")
	}
	if existing.Status == "completed" {
		s.log.Debug().Str("request_id", requestID).Msg("esign ingest: already completed — skipping (idempotent)")
		return nil
	}
	// Only an open request (pending / in_progress) may turn a vendor envelope
	// into a new version. A request that is cancelled, declined or expired
	// is closed: pull nothing, write nothing, and return nil so the webhook
	// is acknowledged and the vendor stops redelivering. Checking this only
	// at CompleteTx time — after the version was minted — answered 500 and
	// every vendor retry ingested another version.
	//
	// The gate is on STATUS only, not on SeDoc's expires_at: that date is
	// never sent to the vendor envelope and nothing moves a vendor request
	// to expired, so the vendor owns envelope expiry. An envelope completed
	// after day 14 must still ingest; otherwise it would be acked and lost,
	// and the reconciler would poll the vendor for it forever.
	if !requestOpen(existing.Status) {
		s.log.Warn().Str("request_id", requestID).Str("envelope_id", envelopeID).Str("status", existing.Status).
			Msg("esign ingest: request is closed; ignoring completed envelope (no new version)")
		return nil
	}

	client, err := s.esignClient(ctx, tenantID, provider)
	if err != nil {
		return err
	}
	got, err := client.GetSignedDocument(ctx, esign.GetSignedReq{
		TenantID: tenantID, EnvelopeID: envelopeID, IncludeCoC: true,
	})
	if err != nil {
		return err
	}

	// Hand off to the storage + document services. When the ingest
	// client isn't wired, log + continue so the request still
	// transitions to completed; that's better than blocking on a
	// dependency that doesn't exist in the local-dev environment.
	var versionID, contentBlobID, cocBlobID string
	// Resolve the residency region; on failure we SKIP the storage
	// writes entirely rather than default to us-east-1 (residency). The
	// request still transitions to completed below — the version_id is
	// left empty in the outbox event, which is observable, exactly as it
	// is for any other ingest failure on this best-effort path.
	var region string
	var regionErr error
	if s.ingest != nil {
		region, regionErr = s.ingest.ResolveRegionOrFail(ctx, tenantID, existing.DocumentID)
	}
	if s.ingest != nil && regionErr != nil {
		s.log.Error().Err(regionErr).Str("request_id", requestID).
			Msg("esign ingest: cannot resolve residency region; skipping signed-PDF hand-off (no default-region write)")
	}
	if s.ingest != nil && regionErr == nil {
		filename := "signed.pdf"
		change := fmt.Sprintf("Signed via %s envelope %s", provider, envelopeID)
		vID, blobID, err := s.ingest.PutAndCreateVersion(ctx, PutSignedBlobInput{
			TenantID: tenantID, UserID: existing.CreatedBy,
			Filename: filename, MimeType: "application/pdf",
			Bytes: got.SignedPDF, RegionPin: region,
		}, existing.DocumentID, existing.CreatedBy, change)
		if err != nil {
			s.log.Error().Err(err).Str("request_id", requestID).Msg("esign ingest: signed PDF hand-off failed")
			// Don't return — proceed to mark completed + emit the
			// audit event so the signature isn't stranded as
			// in_progress. The version-create failure is observable
			// in the outbox event below (version_id empty).
		} else {
			versionID = vID
			contentBlobID = blobID
		}
		// CoC: upload only — there's no semantic for "the audit log
		// IS a version of the doc", so we ship the bytes to storage
		// for future retrieval and embed the blob_id in the audit
		// event. Best-effort; failure here doesn't gate completion.
		if len(got.CoCPDF) > 0 {
			cocPut, err := s.ingest.client.PutSignedBlob(ctx, PutSignedBlobInput{
				TenantID: tenantID, UserID: existing.CreatedBy,
				Filename: "certificate-of-completion.pdf",
				MimeType: "application/pdf",
				Bytes:    got.CoCPDF, RegionPin: region,
			})
			if err != nil {
				s.log.Warn().Err(err).Str("request_id", requestID).Msg("esign ingest: CoC upload failed")
			} else {
				cocBlobID = cocPut.ContentBlobID
			}
		}
	} else {
		s.log.Warn().Str("request_id", requestID).Msg("esign ingest: pipeline not configured; skipping new-version hand-off")
	}

	tenantUUID, _ := uuid.Parse(tenantID)
	reqUUID, _ := uuid.Parse(requestID)
	// Flat payload — the outbox publisher wraps this as the CloudEvent `data`
	// (specversion/id/type/tenantid are added around it). Building a second
	// envelope here double-nested the fields at data.data.*, which broke
	// external webhook consumers reading data.document_id.
	completedPayload, _ := json.Marshal(map[string]any{
		"tenant_id":           tenantID,
		"request_id":          requestID,
		"document_id":         existing.DocumentID,
		"provider":            string(provider),
		"envelope_id":         envelopeID,
		"signed_pdf_bytes":    len(got.SignedPDF),
		"coc_pdf_bytes":       len(got.CoCPDF),
		"signed_version_id":   versionID,
		"content_blob_id":     contentBlobID,
		"coc_content_blob_id": cocBlobID,
	})
	err = database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		if err := s.repo.CompleteTx(ctx, tx, tenantID, requestID); err != nil {
			return err
		}
		evt := database.NewOutboxEvent(tenantUUID, "dms.signature.completed.v1",
			"signature_request", reqUUID, completedPayload)
		return s.outbox.Insert(ctx, tx, evt)
	})
	if errors.Is(err, repository.ErrStateConflict) {
		// The request closed (or another delivery completed it) between the
		// gate above and here. Retrying cannot help — the next delivery
		// would be ignored by the gate — so acknowledge instead of looping.
		s.log.Warn().Str("request_id", requestID).Str("signed_version_id", versionID).
			Msg("esign ingest: request closed while ingesting; not marking completed")
		return nil
	}
	return err
}

// StartReconciler runs the 5-minute poll loop that closes webhook
// gaps. For each stale `in_progress` request we hit GetStatus; if
// the vendor reports completed, we synthesize a webhook event and
// run IngestWebhook.
func (s *Service) StartReconciler(ctx context.Context) {
	if s.esign == nil {
		return
	}
	t := time.NewTicker(s.esign.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.reconcileOnce(ctx)
		}
	}
}

func (s *Service) reconcileOnce(ctx context.Context) {
	stale, err := s.repo.FindStaleInProgressRequests(ctx, 30*time.Minute)
	if err != nil {
		s.log.Error().Err(err).Msg("esign reconcile lookup")
		return
	}
	for _, r := range stale {
		client, err := s.esignClient(ctx, r.TenantID, esign.Provider(r.Provider))
		if err != nil {
			continue
		}
		st, err := client.GetStatus(ctx, esign.StatusReq{TenantID: r.TenantID, EnvelopeID: r.EnvelopeID})
		if err != nil {
			continue
		}
		if st.Status == "completed" {
			synth := &esign.WebhookEvent{
				Provider: esign.Provider(r.Provider), EnvelopeID: r.EnvelopeID,
				EventType: "envelope.completed", ExternalID: r.EnvelopeID + ":poll",
				OccurredAt: st.UpdatedAt, Status: "completed",
				RawPayload: []byte(`{"source":"reconciler"}`),
			}
			if err := s.IngestWebhook(ctx, r.TenantID, synth); err != nil {
				s.log.Error().Err(err).Str("request_id", r.ID).Msg("esign reconcile ingest")
			}
		}
	}
}

// ErrWebhookUnauthorized is the single outcome for a webhook that does not
// carry a valid signature for the tenant and provider in its URL — whatever
// the reason (bad or missing signature, unknown or malformed tenant, no
// stored secret). Collapsing them stops the endpoint from revealing which
// tenants exist or have an integration (S-9). → 401
var ErrWebhookUnauthorized = errors.New("unauthorized")

// HandleWebhook is the handler-facing entry point. It verifies the vendor's
// HMAC with the tenant's stored provider secret (per-tenant config, else the
// deployment-wide env config) BEFORE anything else: no token read, no token
// refresh and no vendor call happen for an unauthenticated body (S-9). Only
// a verified event reaches IngestWebhook.
func (s *Service) HandleWebhook(ctx context.Context, tenantID string, provider esign.Provider, headers http.Header, body []byte) error {
	if s.esign == nil {
		return ErrWebhookUnauthorized
	}
	if _, err := uuid.Parse(tenantID); err != nil {
		return ErrWebhookUnauthorized
	}
	if provider != esign.ProviderDocuSign && provider != esign.ProviderAdobeSign {
		return ErrWebhookUnauthorized
	}
	cfg, err := s.resolveOAuthConfig(ctx, tenantID, provider)
	if err != nil {
		s.log.Debug().Err(err).Str("provider", string(provider)).Msg("esign webhook: no verification secret")
		return ErrWebhookUnauthorized
	}
	evt, err := parseVerifiedWebhook(provider, cfg, headers, body)
	if err != nil {
		if errors.Is(err, esign.ErrSignatureMismatch) || errors.Is(err, esign.ErrNotConfigured) {
			return ErrWebhookUnauthorized
		}
		// Signature verified but the payload does not decode.
		return fmt.Errorf("%w: webhook payload: %v", ErrInvalidRequest, err)
	}
	return s.IngestWebhook(ctx, tenantID, evt)
}

// parseVerifiedWebhook checks the vendor HMAC with the stored secret and
// decodes the event. It builds a parse-only adapter: ParseWebhook uses only
// the signing secret (DocuSign: the client secret; Adobe Sign: client id +
// secret), so the access-token fields are placeholders that are never used.
func parseVerifiedWebhook(provider esign.Provider, cfg esign.OAuthConfig, headers http.Header, body []byte) (*esign.WebhookEvent, error) {
	if cfg.ClientSecret == "" {
		return nil, esign.ErrNotConfigured
	}
	const unused = "webhook-verification-only"
	switch provider {
	case esign.ProviderDocuSign:
		c, err := esign.NewDocuSign(esign.DocuSignConfig{
			AccessToken: unused, AccountID: unused, BaseURI: unused,
			HMACSecret: cfg.ClientSecret,
		})
		if err != nil {
			return nil, err
		}
		return c.ParseWebhook(headers, body)
	case esign.ProviderAdobeSign:
		c, err := esign.NewAdobeSign(esign.AdobeSignConfig{
			AccessToken: unused, APIAccessPoint: unused,
			ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret,
		})
		if err != nil {
			return nil, err
		}
		return c.ParseWebhook(headers, body)
	}
	return nil, esign.ErrNotConfigured
}

// ListInProgressESign returns active vendor envelopes for the
// /admin status tab.
func (s *Service) ListInProgressESign(ctx context.Context, tenantID string) ([]map[string]any, error) {
	stale, err := s.repo.FindInProgressESignRequests(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(stale))
	for _, r := range stale {
		out = append(out, map[string]any{
			"request_id":  r.ID,
			"envelope_id": r.EnvelopeID,
			"provider":    r.Provider,
			"status":      "in_progress",
		})
	}
	return out, nil
}

// isUniqueViolation surfaces Postgres' SQLSTATE 23505 — the dedup
// path. We can't import pgconn here without churn; check by string
// substring which is stable across pgx versions.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for i := 0; i+5 <= len(msg); i++ {
		if msg[i:i+5] == "23505" {
			return true
		}
	}
	return false
}
