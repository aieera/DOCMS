// Microsoft 365 connector orchestration. Mirrors the Google variant
// in this directory — save credentials, start OAuth, handle the
// callback, persist sealed tokens. The Graph methods (sites, drives,
// mail, channels) live behind a thin getter that builds an
// m365.Client on demand.
//
// Why a thin getter and not a long-lived Client cached on Service?
//   * Tokens belong to the request — a hot path that resolves a
//     Client per call re-reads connector_configs and naturally
//     picks up token refreshes from another process.
//   * Cache invalidation across pods is a noisier problem than the
//     per-request Postgres read; we'll revisit when latency calls
//     for it.
package service

import (
	"context"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aieera/sedoc/pkg/esign"
	"github.com/aieera/sedoc/services/connector/internal/model"
	"github.com/aieera/sedoc/services/connector/internal/providers/m365"
)

// SaveM365Config stores per-tenant Entra app credentials. Sealed via
// the same key + envelope as Google.
func (s *Service) SaveM365Config(ctx context.Context, tenantID, userID, clientID, clientSecret, entraTenant string) error {
	if clientID == "" || clientSecret == "" {
		return errors.New("client_id and client_secret are required")
	}
	if s.connSealingKey == nil {
		return errors.New("connector sealing key not configured")
	}
	cfgPlain, _ := stdjson.Marshal(model.ProviderConfig{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		// Tenant directory ID stored in Extra so unsealProviderConfig
		// stays Google-compatible; default "common" is applied at
		// connector construction time.
		Extra: map[string]string{"entra_tenant": strings.TrimSpace(entraTenant)},
	})
	sealed, err := esign.SealString(cfgPlain, s.connSealingKey)
	if err != nil {
		return fmt.Errorf("seal m365 config: %w", err)
	}
	cc := &model.ConnectorConfig{
		TenantID:        tenantID,
		ConnectorType:   m365.ProviderName,
		DisplayName:     "Microsoft 365",
		ConfigEncrypted: []byte(sealed),
		IsActive:        true,
		CreatedBy:       userID,
	}
	return s.repo.UpsertConnector(ctx, cc)
}

// M365ConfigPublic is the UI-safe view; secrets never travel back.
type M365ConfigPublic struct {
	ClientID    string    `json:"client_id"`
	HasSecret   bool      `json:"has_secret"`
	EntraTenant string    `json:"entra_tenant"`
	IsActive    bool      `json:"is_active"`
	SyncStatus  string    `json:"sync_status,omitempty"`
	Authorized  bool      `json:"authorized"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// GetM365ConfigPublic returns the secret-stripped status row for the
// admin modal. nil + nil error = "not configured yet."
func (s *Service) GetM365ConfigPublic(ctx context.Context, tenantID string) (*M365ConfigPublic, error) {
	row, err := s.repo.GetConnector(ctx, tenantID, m365.ProviderName)
	if err != nil || row == nil {
		return nil, err
	}
	plain, err := s.unsealProviderConfig(row)
	if err != nil {
		return nil, err
	}
	entra := ""
	if plain.Extra != nil {
		entra = plain.Extra["entra_tenant"]
	}
	return &M365ConfigPublic{
		ClientID:    plain.ClientID,
		HasSecret:   plain.ClientSecret != "",
		EntraTenant: entra,
		IsActive:    row.IsActive,
		SyncStatus:  row.SyncStatus,
		Authorized:  len(row.OAuthTokensEncrypted) > 0,
		UpdatedAt:   row.UpdatedAt,
	}, nil
}

// StartM365OAuth resolves the saved config and builds the authorize
// URL with the connector-state HMAC binding tenant→provider.
func (s *Service) StartM365OAuth(ctx context.Context, tenantID string) (string, error) {
	row, err := s.repo.GetConnector(ctx, tenantID, m365.ProviderName)
	if err != nil {
		return "", err
	}
	if row == nil {
		return "", errors.New("m365 not configured for this tenant; save credentials first")
	}
	cfg, err := s.unsealProviderConfig(row)
	if err != nil {
		return "", err
	}
	conn := m365.New(m365.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		TenantID:     extra(cfg, "entra_tenant"),
		Log:          s.log,
	})
	state, err := s.signConnectorState(tenantID, m365.ProviderName)
	if err != nil {
		return "", err
	}
	return conn.AuthURL(s.connRedirect, state), nil
}

// HandleM365OAuthCallback verifies state, exchanges the code, seals
// the resulting tokens. Returns the admin-facing redirect URL the
// handler should 303 to.
func (s *Service) HandleM365OAuthCallback(ctx context.Context, code, state string) (string, error) {
	tenantID, connType, ok := s.verifyConnectorState(state)
	if !ok {
		return "", errors.New("invalid state")
	}
	if connType != m365.ProviderName {
		return "", fmt.Errorf("state encodes wrong connector_type %q", connType)
	}
	row, err := s.repo.GetConnector(ctx, tenantID, m365.ProviderName)
	if err != nil {
		return "", err
	}
	if row == nil {
		return "", errors.New("m365 config disappeared between start and callback")
	}
	cfg, err := s.unsealProviderConfig(row)
	if err != nil {
		return "", err
	}
	conn := m365.New(m365.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		TenantID:     extra(cfg, "entra_tenant"),
		Log:          s.log,
	})
	tokens, err := conn.ExchangeCode(ctx, code, s.connRedirect)
	if err != nil {
		return "", fmt.Errorf("m365: exchange: %w", err)
	}
	tokens.ClientID = cfg.ClientID
	tokens.ClientSecret = cfg.ClientSecret
	// Microsoft's token response includes a `scope` claim that
	// reflects the actually-granted set (it can be narrower than
	// requested when the admin trims at consent). We persist what
	// Graph says it granted; the client surfaces this so the UI
	// can warn if a required scope was withheld.
	if len(tokens.Scopes) == 0 {
		tokens.Scopes = m365.DefaultScopes
	}
	tokJSON, _ := stdjson.Marshal(tokens)
	sealed, err := esign.SealString(tokJSON, s.connSealingKey)
	if err != nil {
		return "", fmt.Errorf("seal m365 tokens: %w", err)
	}
	if err := s.repo.UpdateConnectorTokens(ctx, tenantID, m365.ProviderName, []byte(sealed)); err != nil {
		return "", err
	}
	return "/admin/integrations?connected=m365", nil
}

// DisconnectM365 clears the tenant's tokens; the saved credentials
// stay so the admin can re-authorize without re-pasting them.
func (s *Service) DisconnectM365(ctx context.Context, tenantID string) error {
	return s.repo.UpdateConnectorTokens(ctx, tenantID, m365.ProviderName, nil)
}

// m365Client returns a ready-to-call Graph client for the tenant.
// Returns ErrNoTenantTokens when the tenant hasn't authorized yet —
// HTTP handlers translate that to 409 Conflict.
func (s *Service) m365Client(ctx context.Context, tenantID string) (*m365.Client, error) {
	row, err := s.repo.GetConnector(ctx, tenantID, m365.ProviderName)
	if err != nil {
		return nil, err
	}
	if row == nil || len(row.OAuthTokensEncrypted) == 0 {
		return nil, ErrNoTenantTokens
	}
	cfg, err := s.unsealProviderConfig(row)
	if err != nil {
		return nil, err
	}
	tokPlain, err := esign.UnsealString(string(row.OAuthTokensEncrypted), s.connSealingKey)
	if err != nil {
		return nil, fmt.Errorf("unseal m365 tokens: %w", err)
	}
	var tokens model.OAuthTokens
	if err := stdjson.Unmarshal([]byte(tokPlain), &tokens); err != nil {
		return nil, fmt.Errorf("decode m365 tokens: %w", err)
	}
	conn := m365.New(m365.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		TenantID:     extra(cfg, "entra_tenant"),
		Log:          s.log,
	})
	// onRefresh re-seals the fresh tokens. The closure captures the
	// outer Service + tenantID — that's fine, the Service is a
	// process-lifetime singleton.
	onRefresh := func(fresh *model.OAuthTokens) {
		fresh.ClientID = cfg.ClientID
		fresh.ClientSecret = cfg.ClientSecret
		j, _ := stdjson.Marshal(fresh)
		sealed, err := esign.SealString(j, s.connSealingKey)
		if err != nil {
			s.log.Error().Err(err).Msg("m365 refresh re-seal failed")
			return
		}
		if err := s.repo.UpdateConnectorTokens(context.Background(), tenantID, m365.ProviderName, []byte(sealed)); err != nil {
			s.log.Error().Err(err).Msg("m365 refresh persist failed")
		}
	}
	return conn.NewClient(&tokens, m365.WithRefreshCallback(onRefresh)), nil
}

// ErrNoTenantTokens — sentinel for "configured but unauthorized."
// HTTP handlers translate to 409 so the FE shows "click Connect."
var ErrNoTenantTokens = errors.New("m365: tenant has no oauth tokens; admin must connect")

// ---- public wrappers (called by the HTTP handler) -----------------

// ListM365Sites is the HTTP-handler-facing wrapper around
// Client.ListSites.
func (s *Service) ListM365Sites(ctx context.Context, tenantID, actingAs string) ([]m365.Site, error) {
	cl, err := s.m365Client(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return cl.ListSites(ctx, actingAs)
}

// ListM365DriveItems is the wrapper for Client.ListDriveItems.
func (s *Service) ListM365DriveItems(ctx context.Context, tenantID, driveID, folderID, actingAs string) ([]m365.DriveItem, error) {
	cl, err := s.m365Client(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return cl.ListDriveItems(ctx, driveID, folderID, actingAs)
}

// GetM365DriveItemContent streams a file's bytes. Caller MUST Close
// the returned ReadCloser.
func (s *Service) GetM365DriveItemContent(ctx context.Context, tenantID, driveID, itemID, actingAs string) (io.ReadCloser, error) {
	cl, err := s.m365Client(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return cl.GetDriveItemContent(ctx, driveID, itemID, actingAs)
}

// ---- helpers ------------------------------------------------------

func extra(c *model.ProviderConfig, k string) string {
	if c == nil || c.Extra == nil {
		return ""
	}
	return c.Extra[k]
}
