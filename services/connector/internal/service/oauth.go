// OAuth authorize-start + callback orchestration.
//
// State store: Redis key `connector:pkce:<state>` → JSON of
// {code_verifier, tenant_id, provider, redirect_uri}. TTL 10 min —
// enough for any interactive user flow; forces a fresh authorize
// if the user dawdles.
//
// This file deliberately does NOT persist tokens. Storage is
// deferred until the schema drift on `connector_configs` is
// reconciled (logged in docs/backlog/out-of-scope.md). Callers
// receive the tokens in-memory on callback and can inspect / test
// the round-trip without committing to the broken persistence
// path.

package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/vaultdms/vaultdms/services/connector/internal/model"
	"github.com/vaultdms/vaultdms/services/connector/internal/providers"
)

const (
	pkceKeyPrefix = "connector:pkce:"
	pkceTTL       = 10 * time.Minute
)

// ProviderRegistry maps a provider name → Provider implementation.
// Constructed in cmd/server/main.go from env-configured clients.
type ProviderRegistry map[string]providers.Provider

// pkceState is what we store in Redis between authorize and callback.
type pkceState struct {
	TenantID     string `json:"tenant_id"`
	Provider     string `json:"provider"`
	RedirectURI  string `json:"redirect_uri"`
	CodeVerifier string `json:"code_verifier"`
}

// AttachOAuth gives the Service the pieces it needs to run the OAuth
// dance. Called once at boot from main.go.
func (s *Service) AttachOAuth(rdb *redis.Client, registry ProviderRegistry) {
	s.rdb = rdb
	s.providers = registry
}

// BeginOAuth generates a fresh PKCE verifier, persists its state in
// Redis under a random opaque `state` token, and returns the provider
// auth URL + the state token. The caller (HTTP handler) returns both
// to the admin client so the redirect can happen browser-side.
func (s *Service) BeginOAuth(ctx context.Context, tenantID, providerName, redirectURI string) (authURL, state string, err error) {
	if s.rdb == nil || s.providers == nil {
		return "", "", fmt.Errorf("oauth not attached")
	}
	p, ok := s.providers[providerName]
	if !ok {
		return "", "", fmt.Errorf("unknown provider %q", providerName)
	}
	state, err = opaqueState()
	if err != nil {
		return "", "", err
	}
	verifier, err := providers.NewCodeVerifier()
	if err != nil {
		return "", "", err
	}
	body, err := json.Marshal(pkceState{
		TenantID: tenantID, Provider: providerName,
		RedirectURI: redirectURI, CodeVerifier: verifier,
	})
	if err != nil {
		return "", "", err
	}
	if err := s.rdb.Set(ctx, pkceKeyPrefix+state, body, pkceTTL).Err(); err != nil {
		return "", "", fmt.Errorf("pkce state persist: %w", err)
	}
	return p.AuthURL(redirectURI, state, providers.CodeChallenge(verifier)), state, nil
}

// CompleteOAuth retrieves the stored verifier for `state`, exchanges
// `code` for tokens, and returns the tokens. The verifier row is
// deleted whether the exchange succeeded or failed — a replayed
// state is never honoured. Tokens are NOT persisted here; that is
// the caller's (deferred) responsibility.
func (s *Service) CompleteOAuth(ctx context.Context, state, code string) (*model.OAuthTokens, string, error) {
	if s.rdb == nil || s.providers == nil {
		return nil, "", fmt.Errorf("oauth not attached")
	}
	key := pkceKeyPrefix + state
	raw, err := s.rdb.GetDel(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, "", fmt.Errorf("unknown or expired state")
	}
	if err != nil {
		return nil, "", fmt.Errorf("pkce state load: %w", err)
	}
	var st pkceState
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, "", fmt.Errorf("pkce state decode: %w", err)
	}
	p, ok := s.providers[st.Provider]
	if !ok {
		return nil, "", fmt.Errorf("provider %q no longer registered", st.Provider)
	}
	tokens, err := p.ExchangeCode(ctx, code, st.RedirectURI, st.CodeVerifier)
	if err != nil {
		return nil, st.TenantID, fmt.Errorf("exchange code: %w", err)
	}
	return tokens, st.TenantID, nil
}

// opaqueState returns a 32-byte URL-safe random string for use as
// the OAuth `state` param. Not the PKCE verifier — that's separate.
func opaqueState() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
