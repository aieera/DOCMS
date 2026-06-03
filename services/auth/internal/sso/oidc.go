package sso

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"golang.org/x/oauth2"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
)

// OIDCService runs the OAuth 2.0 Authorization Code flow + PKCE against a
// tenant-configured IdP. The ID token is validated by go-oidc (signature,
// issuer, audience, exp). A UserInfo call fetches attributes not present
// in the ID token (typically groups).
type OIDCService struct {
	pool        *pgxpool.Pool
	rdb         *redis.Client
	cfgRepo     ConfigRepository
	prov        SAMLProvisioner // interface is SSO-generic; same find-or-create
	log         zerolog.Logger
	publicURL   string
	httpClient  *http.Client
	now         func() time.Time
}

// OIDCServiceConfig bundles DI.
type OIDCServiceConfig struct {
	Pool        *pgxpool.Pool
	Redis       *redis.Client
	ConfigRepo  ConfigRepository
	Provisioner SAMLProvisioner
	Logger      zerolog.Logger
	PublicURL   string
	HTTPClient  *http.Client // defaults to http.DefaultClient (used for discovery + token endpoint)
}

// NewOIDCService constructs the service.
func NewOIDCService(cfg OIDCServiceConfig) *OIDCService {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	return &OIDCService{
		pool:       cfg.Pool,
		rdb:        cfg.Redis,
		cfgRepo:    cfg.ConfigRepo,
		prov:       cfg.Provisioner,
		log:        cfg.Logger,
		publicURL:  strings.TrimRight(cfg.PublicURL, "/"),
		httpClient: hc,
		now:        time.Now,
	}
}

// ---- Login (SP-initiated) -------------------------------------------------

// BuildAuthorizeURL generates a PKCE code_verifier, stores it in Redis
// keyed by `state`, and returns the URL the browser should navigate to.
// The TTL on the verifier is 10 minutes — well above most IdP round-trips.
func (s *OIDCService) BuildAuthorizeURL(ctx context.Context, tenantSlug string, tenantID uuid.UUID) (string, error) {
	cfg, err := s.loadOIDCConfig(ctx, tenantID)
	if err != nil {
		return "", err
	}

	provider, err := oidc.NewProvider(s.oidcContext(ctx), cfg.IssuerURL)
	if err != nil {
		return "", fmt.Errorf("oidc discovery: %w", err)
	}

	state, err := randToken()
	if err != nil {
		return "", err
	}
	nonce, err := randToken()
	if err != nil {
		return "", err
	}
	verifier, challenge, err := pkcePair()
	if err != nil {
		return "", err
	}

	redirectURL := s.publicURL + "/api/v1/auth/oidc/" + tenantSlug + "/callback"

	oauthCfg := &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  redirectURL,
		Endpoint:     provider.Endpoint(),
		Scopes:       oidcScopes(cfg),
	}

	// Persist verifier + nonce + tenant binding. Single JSON blob keeps this
	// as one round-trip; TTL 10 min.
	payload, _ := json.Marshal(map[string]string{
		"tenant_slug":   tenantSlug,
		"tenant_id":     tenantID.String(),
		"code_verifier": verifier,
		"nonce":         nonce,
	})
	if err := s.rdb.Set(ctx, oidcStateKey(state), payload, 10*time.Minute).Err(); err != nil {
		return "", fmt.Errorf("redis set oidc state: %w", err)
	}

	u := oauthCfg.AuthCodeURL(state,
		oauth2.SetAuthURLParam("code_challenge", challenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
		oidc.Nonce(nonce),
	)
	return u, nil
}

// ---- Callback -------------------------------------------------------------

// ExchangeCode completes the Authorization Code flow: pops the PKCE
// verifier + nonce from Redis (one-shot), exchanges the code for tokens,
// validates the ID token, pulls UserInfo, then provisions the user.
func (s *OIDCService) ExchangeCode(ctx context.Context, r *http.Request, tenantSlug string, tenantID uuid.UUID, ip, ua string) (*ACSResult, error) {
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if state == "" || code == "" {
		return nil, vdmserr.Validation("callback", "missing state or code")
	}
	if errStr := r.URL.Query().Get("error"); errStr != "" {
		return nil, vdmserr.Validation("callback", "idp error: "+errStr)
	}

	blob, err := s.rdb.GetDel(ctx, oidcStateKey(state)).Bytes()
	if err == redis.Nil {
		return nil, vdmserr.ErrUnauthorized
	}
	if err != nil {
		return nil, fmt.Errorf("redis getdel: %w", err)
	}
	var rec struct {
		TenantSlug   string `json:"tenant_slug"`
		TenantID     string `json:"tenant_id"`
		CodeVerifier string `json:"code_verifier"`
		Nonce        string `json:"nonce"`
	}
	if err := json.Unmarshal(blob, &rec); err != nil || rec.TenantSlug != tenantSlug {
		return nil, vdmserr.ErrUnauthorized
	}

	cfg, err := s.loadOIDCConfig(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	oidcCtx := s.oidcContext(ctx)
	provider, err := oidc.NewProvider(oidcCtx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}

	redirectURL := s.publicURL + "/api/v1/auth/oidc/" + tenantSlug + "/callback"
	oauthCfg := &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  redirectURL,
		Endpoint:     provider.Endpoint(),
		Scopes:       oidcScopes(cfg),
	}

	tok, err := oauthCfg.Exchange(oidcCtx, code,
		oauth2.SetAuthURLParam("code_verifier", rec.CodeVerifier),
	)
	if err != nil {
		s.log.Warn().Err(err).Msg("oidc token exchange failed")
		return nil, vdmserr.ErrUnauthorized
	}
	rawIDToken, ok := tok.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return nil, vdmserr.ErrUnauthorized
	}

	verifier := provider.Verifier(&oidc.Config{ClientID: cfg.ClientID})
	idt, err := verifier.Verify(oidcCtx, rawIDToken)
	if err != nil {
		s.log.Warn().Err(err).Msg("oidc id_token verification failed")
		return nil, vdmserr.ErrUnauthorized
	}
	if idt.Nonce != rec.Nonce {
		s.log.Warn().Msg("oidc nonce mismatch")
		return nil, vdmserr.ErrUnauthorized
	}

	var claims struct {
		Email         string   `json:"email"`
		EmailVerified bool     `json:"email_verified"`
		Name          string   `json:"name"`
		Groups        []string `json:"groups"`
	}
	if err := idt.Claims(&claims); err != nil {
		return nil, fmt.Errorf("claims: %w", err)
	}

	// UserInfo is only queried when ID token lacks email (some IdPs keep
	// email out of the ID token). Groups often require UserInfo too.
	if claims.Email == "" || len(claims.Groups) == 0 {
		if ui, err := provider.UserInfo(oidcCtx, oauth2.StaticTokenSource(tok)); err == nil {
			var uiClaims struct {
				Email  string   `json:"email"`
				Name   string   `json:"name"`
				Groups []string `json:"groups"`
			}
			_ = ui.Claims(&uiClaims)
			if claims.Email == "" {
				claims.Email = uiClaims.Email
			}
			if claims.Name == "" {
				claims.Name = uiClaims.Name
			}
			if len(claims.Groups) == 0 {
				claims.Groups = uiClaims.Groups
			}
		}
	}

	if claims.Email == "" {
		return nil, vdmserr.Validation("claims", "email missing from id_token and userinfo")
	}

	token, expiresAt, err := s.prov.FindOrCreateSAMLUser(ctx, tenantID, claims.Email, claims.Name, claims.Groups, ip, ua)
	if err != nil {
		return nil, err
	}

	redirectTo := s.publicURL + "/?sso=oidc"
	if rs := r.URL.Query().Get("relay_to"); rs != "" && isSafeRelay(rs, s.publicURL) {
		redirectTo = rs
	}
	return &ACSResult{SessionToken: token, ExpiresAt: expiresAt, RedirectTo: redirectTo}, nil
}

// ---- helpers --------------------------------------------------------------

func (s *OIDCService) loadOIDCConfig(ctx context.Context, tenantID uuid.UUID) (*OIDCConfig, error) {
	cfg, err := s.cfgRepo.GetActiveByTenantProvider(ctx, s.pool, tenantID, ProviderOIDC)
	if err != nil {
		return nil, vdmserr.Validation("tenant_slug", "OIDC not configured for tenant")
	}
	var oc OIDCConfig
	if err := json.Unmarshal(cfg.Config, &oc); err != nil {
		return nil, fmt.Errorf("oidc config parse: %w", err)
	}
	if oc.IssuerURL == "" || oc.ClientID == "" || oc.ClientSecret == "" {
		return nil, vdmserr.Validation("", "oidc config missing issuer_url/client_id/client_secret")
	}
	return &oc, nil
}

// oidcContext injects our HTTP client so oidc.NewProvider + token exchange
// honor our timeouts.
func (s *OIDCService) oidcContext(ctx context.Context) context.Context {
	return oidc.ClientContext(ctx, s.httpClient)
}

// oidcScopes returns [openid, profile, email] plus any admin-configured
// extras (e.g. "groups", "offline_access").
func oidcScopes(cfg *OIDCConfig) []string {
	scopes := []string{oidc.ScopeOpenID, "profile", "email"}
	for _, s := range cfg.Scopes {
		if s != "" && s != oidc.ScopeOpenID && s != "profile" && s != "email" {
			scopes = append(scopes, s)
		}
	}
	return scopes
}

// randToken returns a 32-byte base64url string for state/nonce usage.
func randToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// pkcePair generates an (RFC 7636) code_verifier + code_challenge via S256.
func pkcePair() (verifier, challenge string, err error) {
	vb := make([]byte, 48) // 48 bytes → 64 chars after base64url
	if _, err = rand.Read(vb); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(vb)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

func oidcStateKey(state string) string { return "oidc_pkce:" + state }

// silence unused import on some builds
var _ = url.Parse
