// Package providers implements OAuth connectors for external systems.
// Each provider handles token refresh, rate limiting, and error retry.
package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/services/connector/internal/model"
)

// Provider is the interface all connectors implement.
type Provider interface {
	Name() string
	AuthURL(redirectURI, state string) string
	ExchangeCode(ctx context.Context, code, redirectURI string) (*model.OAuthTokens, error)
	RefreshToken(ctx context.Context, tokens *model.OAuthTokens) (*model.OAuthTokens, error)
}

// BaseOAuth provides common OAuth2 token exchange and refresh logic.
type BaseOAuth struct {
	ProviderName string
	AuthEndpoint string
	TokenEndpoint string
	Scopes       []string
	ClientID     string
	ClientSecret string
	Log          zerolog.Logger
}

// AuthURL returns the OAuth2 authorization URL.
func (b *BaseOAuth) AuthURL(redirectURI, state string) string {
	params := url.Values{
		"client_id":     {b.ClientID},
		"response_type": {"code"},
		"redirect_uri":  {redirectURI},
		"scope":         {strings.Join(b.Scopes, " ")},
		"state":         {state},
	}
	return b.AuthEndpoint + "?" + params.Encode()
}

// ExchangeCode exchanges an authorization code for tokens.
func (b *BaseOAuth) ExchangeCode(ctx context.Context, code, redirectURI string) (*model.OAuthTokens, error) {
	data := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {b.ClientID},
		"client_secret": {b.ClientSecret},
	}
	return b.tokenRequest(ctx, data)
}

// RefreshToken refreshes an expired access token.
func (b *BaseOAuth) RefreshToken(ctx context.Context, tokens *model.OAuthTokens) (*model.OAuthTokens, error) {
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {tokens.RefreshToken},
		"client_id":     {b.ClientID},
		"client_secret": {b.ClientSecret},
	}
	newTokens, err := b.tokenRequest(ctx, data)
	if err != nil {
		return nil, err
	}
	if newTokens.RefreshToken == "" {
		newTokens.RefreshToken = tokens.RefreshToken
	}
	return newTokens, nil
}

// EnsureValid refreshes the token if expired. Returns the current valid token.
func (b *BaseOAuth) EnsureValid(ctx context.Context, tokens *model.OAuthTokens) (*model.OAuthTokens, error) {
	if time.Now().Before(tokens.TokenExpiry.Add(-60 * time.Second)) {
		return tokens, nil
	}
	return b.RefreshToken(ctx, tokens)
}

func (b *BaseOAuth) tokenRequest(ctx context.Context, data url.Values) (*model.OAuthTokens, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, b.TokenEndpoint, strings.NewReader(data.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("token error %d: %s", resp.StatusCode, string(body[:min(len(body), 500)]))
	}

	var raw struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}
	return &model.OAuthTokens{
		ClientID:     b.ClientID,
		ClientSecret: b.ClientSecret,
		AccessToken:  raw.AccessToken,
		RefreshToken: raw.RefreshToken,
		TokenExpiry:  time.Now().Add(time.Duration(raw.ExpiresIn) * time.Second),
		Scopes:       strings.Split(raw.Scope, " "),
	}, nil
}
