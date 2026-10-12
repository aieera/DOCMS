// Google Workspace connector orchestration: save credentials, start
// OAuth, handle callback, store tokens. Drive/Gmail import is a
// follow-up — this file gets us through the connect handshake.
//
// Pattern mirrors the eSign per-tenant flow:
//  1. Admin pastes client_id + client_secret in the modal → SaveGoogleConfig
//     seals them into connector_configs.config_encrypted.
//  2. Admin clicks "Connect" → StartGoogleOAuth resolves the saved
//     config, builds the Google authorize URL with an HMAC-signed
//     state, returns it; the browser is redirected.
//  3. Google redirects back to /api/v1/connectors/google/oauth/callback
//     with ?code=...&state=... — HandleGoogleOAuthCallback verifies
//     state, exchanges the code for tokens, seals them into
//     connector_configs.oauth_tokens_encrypted.
package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/aieera/sedoc/pkg/esign"
	"github.com/aieera/sedoc/services/connector/internal/model"
	"github.com/aieera/sedoc/services/connector/internal/providers/google"
	"github.com/aieera/sedoc/services/connector/internal/repository"
)

// connectorStateTTL bounds how long an OAuth authorize→callback round-trip
// state stays valid.
const connectorStateTTL = 10 * time.Minute

// SetConnectorDeps wires the per-tenant credential paths for native
// connectors. Called from main.go after Service construction. The
// sealing key MUST be stable across restarts so previously-saved
// configs remain decryptable.
func (s *Service) SetConnectorDeps(sealingKey, hmacSecret []byte, defaultRedirect string) {
	s.connSealingKey = sealingKey
	s.connHMAC = hmacSecret
	s.connRedirect = defaultRedirect
}

// ErrOAuthStateInvalid rejects a callback whose state fails the HMAC or
// expiry check, names another connector, or whose server-side nonce is
// unknown or already consumed (replay).
var ErrOAuthStateInvalid = errors.New("invalid or already-used oauth state")

// ErrOAuthStateNotYours rejects a callback completed by a different user (or
// without a session) than the one who started the flow — connect-account
// CSRF: a third party consenting with their own Google/M365 account must
// not get their tokens sealed into the tenant. The handler answers 403.
var ErrOAuthStateNotYours = errors.New("oauth state was not issued to this user")

// beginConnectorOAuth starts one authorize round-trip: a random nonce and a
// PKCE code_verifier are stored server-side (connector_oauth_states, single
// use, connectorStateTTL), and the returned state binds tenant, connector,
// nonce and expiry under the HMAC. The caller puts codeChallenge (S256 of
// the verifier) on the authorize URL.
func (s *Service) beginConnectorOAuth(ctx context.Context, tenantID, connectorType, actorID string) (state, codeChallenge string, err error) {
	if len(s.connHMAC) < 32 {
		return "", "", errors.New("connector hmac secret not configured (≥32 bytes)")
	}
	// The flow is bound to the user who starts it; the callback must come
	// back on that user's session.
	if _, err := uuid.Parse(actorID); err != nil {
		return "", "", errors.New("connecting a provider requires a signed-in user")
	}
	nb := make([]byte, 16)
	if _, err := rand.Read(nb); err != nil {
		return "", "", err
	}
	nonce := hex.EncodeToString(nb)
	vb := make([]byte, 32)
	if _, err := rand.Read(vb); err != nil {
		return "", "", err
	}
	verifier := base64.RawURLEncoding.EncodeToString(vb)
	sum := sha256.Sum256([]byte(verifier))
	codeChallenge = base64.RawURLEncoding.EncodeToString(sum[:])

	expAt := time.Now().Add(connectorStateTTL)
	if err := s.repo.CreateOAuthState(ctx, tenantID, nonce, connectorType, verifier, actorID, expAt); err != nil {
		return "", "", fmt.Errorf("record oauth state: %w", err)
	}
	return s.signConnectorStateWith(tenantID, connectorType, nonce, strconv.FormatInt(expAt.Unix(), 10)), codeChallenge, nil
}

// signConnectorStateWith renders tenant.connector.nonce.exp.sig. Fields are
// dot-free (UUID tenant, slug type, hex nonce, decimal exp, hex sig), so a
// dot delimiter is unambiguous.
func (s *Service) signConnectorStateWith(tenantID, connectorType, nonce, exp string) string {
	sig := s.connStateMAC(tenantID, connectorType, nonce, exp)
	return strings.Join([]string{tenantID, connectorType, nonce, exp, sig}, ".")
}

func (s *Service) connStateMAC(tenantID, connectorType, nonce, exp string) string {
	mac := hmac.New(sha256.New, s.connHMAC)
	mac.Write([]byte(tenantID + "|" + connectorType + "|" + nonce + "|" + exp))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Service) verifyConnectorState(state string) (tenantID, connectorType, nonce string, ok bool) {
	parts := strings.Split(state, ".")
	if len(parts) != 5 {
		return "", "", "", false
	}
	tenantID, connectorType, nonce, exp, sig := parts[0], parts[1], parts[2], parts[3], parts[4]
	if !hmac.Equal([]byte(sig), []byte(s.connStateMAC(tenantID, connectorType, nonce, exp))) {
		return "", "", "", false
	}
	expUnix, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || time.Now().Unix() > expUnix {
		return "", "", "", false
	}
	return tenantID, connectorType, nonce, true
}

// consumeConnectorState verifies the state and atomically consumes its
// server-side nonce for userID (the session user on the callback),
// returning the tenant and the PKCE verifier. A second callback with the
// same state finds no row (ErrOAuthStateInvalid); a callback by anyone
// other than the user who started the flow — or with no session — is
// refused without consuming the nonce (ErrOAuthStateNotYours).
func (s *Service) consumeConnectorState(ctx context.Context, state, wantType, userID string) (tenantID, codeVerifier string, err error) {
	tenantID, connectorType, nonce, ok := s.verifyConnectorState(state)
	if !ok || connectorType != wantType {
		return "", "", ErrOAuthStateInvalid
	}
	codeVerifier, outcome, err := s.repo.ConsumeOAuthState(ctx, tenantID, nonce, connectorType, userID)
	if err != nil {
		return "", "", err
	}
	switch outcome {
	case repository.OAuthStateConsumed:
		return tenantID, codeVerifier, nil
	case repository.OAuthStateNotYours:
		return "", "", ErrOAuthStateNotYours
	default:
		return "", "", ErrOAuthStateInvalid
	}
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
		ClientID:   plain.ClientID,
		HasSecret:  plain.ClientSecret != "",
		IsActive:   row.IsActive,
		SyncStatus: row.SyncStatus,
		Authorized: len(row.OAuthTokensEncrypted) > 0,
		UpdatedAt:  row.UpdatedAt,
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
func (s *Service) StartGoogleOAuth(ctx context.Context, tenantID, actorID string) (string, error) {
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
	conn := s.googleConnector(cfg)
	state, challenge, err := s.beginConnectorOAuth(ctx, tenantID, "google", actorID)
	if err != nil {
		return "", err
	}
	return conn.AuthURLPKCE(s.connRedirect, state, challenge), nil
}

// googleConnector builds the provider client; oauthTokenEndpoint (tests
// only) points the code exchange at a local server.
func (s *Service) googleConnector(cfg *model.ProviderConfig) *google.Connector {
	conn := google.New(cfg.ClientID, cfg.ClientSecret, s.log)
	if s.oauthTokenEndpoint != "" {
		conn.TokenEndpoint = s.oauthTokenEndpoint
	}
	return conn
}

// HandleGoogleOAuthCallback verifies the state, exchanges the auth
// code for tokens, seals the tokens, and persists them. Returns the
// admin-facing redirect URL the handler should 303 to.
func (s *Service) HandleGoogleOAuthCallback(ctx context.Context, code, state, userID string) (string, error) {
	// Consumed before anything else: a replayed state is rejected even if
	// the rest of this callback later fails.
	tenantID, verifier, err := s.consumeConnectorState(ctx, state, "google", userID)
	if err != nil {
		return "", err
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
	conn := s.googleConnector(cfg)
	tokens, err := conn.ExchangeCodePKCE(ctx, code, s.connRedirect, verifier)
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
