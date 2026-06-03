// Google Workspace connector orchestration: save credentials, start
// OAuth, handle callback, store tokens. Drive/Gmail import is a
// follow-up — this file gets us through the connect handshake.
//
// Pattern mirrors the eSign per-tenant flow:
//   1. Admin pastes client_id + client_secret in the modal → SaveGoogleConfig
//      seals them into connector_configs.config_encrypted.
//   2. Admin clicks "Connect" → StartGoogleOAuth resolves the saved
//      config, builds the Google authorize URL with an HMAC-signed
//      state, returns it; the browser is redirected.
//   3. Google redirects back to /api/v1/connectors/google/oauth/callback
//      with ?code=...&state=... — HandleGoogleOAuthCallback verifies
//      state, exchanges the code for tokens, seals them into
//      connector_configs.oauth_tokens_encrypted.
package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	stdjson "encoding/json"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aieera/sedoc/pkg/esign"
	"github.com/aieera/sedoc/services/connector/internal/model"
	"github.com/aieera/sedoc/services/connector/internal/providers/google"
)

// SetConnectorDeps wires the per-tenant credential paths for native
// connectors. Called from main.go after Service construction. The
// sealing key MUST be stable across restarts so previously-saved
// configs remain decryptable.
func (s *Service) SetConnectorDeps(sealingKey, hmacSecret []byte, defaultRedirect string) {
	s.connSealingKey = sealingKey
	s.connHMAC = hmacSecret
	s.connRedirect = defaultRedirect
}

// connectorOAuthState binds the OAuth round-trip to a single tenant +
// connector_type. Same shape as the eSign state HMAC.
func (s *Service) signConnectorState(tenantID, connectorType string) (string, error) {
	if len(s.connHMAC) < 32 {
		return "", errors.New("connector hmac secret not configured (≥32 bytes)")
	}
	mac := hmac.New(sha256.New, s.connHMAC)
	mac.Write([]byte(tenantID + "|" + connectorType))
	return tenantID + "." + connectorType + "." + hex.EncodeToString(mac.Sum(nil)), nil
}

func (s *Service) verifyConnectorState(state string) (tenantID, connectorType string, ok bool) {
	first := strings.IndexByte(state, '.')
	if first < 0 {
		return "", "", false
	}
	second := strings.IndexByte(state[first+1:], '.')
	if second < 0 {
		return "", "", false
	}
	tenantID = state[:first]
	connectorType = state[first+1 : first+1+second]
	got := state[first+1+second+1:]
	mac := hmac.New(sha256.New, s.connHMAC)
	mac.Write([]byte(tenantID + "|" + connectorType))
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(got), []byte(want)) {
		return "", "", false
	}
	return tenantID, connectorType, true
}

// SaveGoogleConfig stores per-tenant Google OAuth credentials. The
// secret is sealed before persistence so a DB dump alone cannot yield
// usable client credentials.
func (s *Service) SaveGoogleConfig(ctx context.Context, tenantID, userID, clientID, clientSecret string) error {
	if clientID == "" || clientSecret == "" {
		return errors.New("client_id and client_secret are required")
	}
	if s.connSealingKey == nil {
		return errors.New("connector sealing key not configured")
	}
	cfgPlain, _ := stdjson.Marshal(model.ProviderConfig{
		ClientID:     clientID,
		ClientSecret: clientSecret,
	})
	sealed, err := esign.SealString(cfgPlain, s.connSealingKey)
	if err != nil {
		return fmt.Errorf("seal google config: %w", err)
	}
	cc := &model.ConnectorConfig{
		TenantID:        tenantID,
		ConnectorType:   "google",
		DisplayName:     "Google Workspace",
		ConfigEncrypted: []byte(sealed),
		IsActive:        true,
		CreatedBy:       userID,
	}
	return s.repo.UpsertConnector(ctx, cc)
}

// GetGoogleConfigPublic returns a secret-stripped view for the admin UI.
func (s *Service) GetGoogleConfigPublic(ctx context.Context, tenantID string) (*GoogleConfigPublic, error) {
	row, err := s.repo.GetConnector(ctx, tenantID, "google")
	if err != nil || row == nil {
		return nil, err
	}
	plain, err := s.unsealProviderConfig(row)
	if err != nil {
		return nil, err
	}
	return &GoogleConfigPublic{
		ClientID:    plain.ClientID,
		HasSecret:   plain.ClientSecret != "",
		IsActive:    row.IsActive,
		SyncStatus:  row.SyncStatus,
		Authorized:  len(row.OAuthTokensEncrypted) > 0,
		UpdatedAt:   row.UpdatedAt,
	}, nil
}

// GoogleConfigPublic — UI-safe view; secrets never travel back.
type GoogleConfigPublic struct {
	ClientID   string    `json:"client_id"`
	HasSecret  bool      `json:"has_secret"`
	IsActive   bool      `json:"is_active"`
	SyncStatus string    `json:"sync_status,omitempty"`
	Authorized bool      `json:"authorized"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// unsealProviderConfig decrypts the saved config_encrypted blob.
func (s *Service) unsealProviderConfig(row *model.ConnectorConfig) (*model.ProviderConfig, error) {
	plain, err := esign.UnsealString(string(row.ConfigEncrypted), s.connSealingKey)
	if err != nil {
		return nil, fmt.Errorf("unseal config: %w", err)
	}
	var pc model.ProviderConfig
	if err := stdjson.Unmarshal([]byte(plain), &pc); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	return &pc, nil
}

// StartGoogleOAuth resolves the tenant's saved Google credentials,
// builds the authorize URL with state, and returns it for the browser
// to follow. Returns an error if the tenant hasn't saved credentials.
func (s *Service) StartGoogleOAuth(ctx context.Context, tenantID string) (string, error) {
	row, err := s.repo.GetConnector(ctx, tenantID, "google")
	if err != nil {
		return "", err
	}
	if row == nil {
		return "", errors.New("google not configured for this tenant; save credentials first")
	}
	cfg, err := s.unsealProviderConfig(row)
	if err != nil {
		return "", err
	}
	conn := google.New(cfg.ClientID, cfg.ClientSecret, s.log)
	state, err := s.signConnectorState(tenantID, "google")
	if err != nil {
		return "", err
	}
	return conn.AuthURL(s.connRedirect, state), nil
}

// HandleGoogleOAuthCallback verifies the state, exchanges the auth
// code for tokens, seals the tokens, and persists them. Returns the
// admin-facing redirect URL the handler should 303 to.
func (s *Service) HandleGoogleOAuthCallback(ctx context.Context, code, state string) (string, error) {
	tenantID, connectorType, ok := s.verifyConnectorState(state)
	if !ok {
		return "", errors.New("invalid state")
	}
	if connectorType != "google" {
		return "", fmt.Errorf("state encodes wrong connector_type %q", connectorType)
	}
	row, err := s.repo.GetConnector(ctx, tenantID, "google")
	if err != nil {
		return "", err
	}
	if row == nil {
		return "", errors.New("google config disappeared between start and callback")
	}
	cfg, err := s.unsealProviderConfig(row)
	if err != nil {
		return "", err
	}
	conn := google.New(cfg.ClientID, cfg.ClientSecret, s.log)
	tokens, err := conn.ExchangeCode(ctx, code, s.connRedirect)
	if err != nil {
		return "", fmt.Errorf("google: exchange: %w", err)
	}
	// Persist client_id + secret alongside the access/refresh so any
	// later refresh has everything it needs in one place.
	tokens.ClientID = cfg.ClientID
	tokens.ClientSecret = cfg.ClientSecret
	tokens.Scopes = []string{
		"https://www.googleapis.com/auth/gmail.readonly",
		"https://www.googleapis.com/auth/drive.readonly",
	}
	tokJSON, _ := stdjson.Marshal(tokens)
	sealed, err := esign.SealString(tokJSON, s.connSealingKey)
	if err != nil {
		return "", fmt.Errorf("seal tokens: %w", err)
	}
	if err := s.repo.UpdateConnectorTokens(ctx, tenantID, "google", []byte(sealed)); err != nil {
		return "", err
	}
	return "/admin/integrations?connected=google", nil
}

// DisconnectGoogle clears the tenant's tokens (keeps the saved
// credentials so the user can re-authorize without re-pasting them).
func (s *Service) DisconnectGoogle(ctx context.Context, tenantID string) error {
	return s.repo.UpdateConnectorTokens(ctx, tenantID, "google", nil)
}

