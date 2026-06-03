// ADR 0071 — third-party connector orchestration.
//
// Three responsibilities:
//
//   1. OAuth lifecycle — start, callback, refresh, disconnect.
//   2. Send + ingest — call the connector when a request's provider
//      is docusign/adobe_sign, persist envelope id; when a webhook
//      lands, insert an event row + (on completed) pull the signed
//      PDF and hand off to the document service for "create new
//      version" through the existing version-uploaded path
//      (ADR 0021).
//   3. Reconcile — a 5-minute poll fills webhook gaps.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
	// refresh tokens at rest. Derived from VAULTDMS_LOCAL_KEK.
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
		if row.AuthorizeURLOverride != "" {
			authzURL = row.AuthorizeURLOverride
		}
		if row.TokenURLOverride != "" {
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

// StartOAuth returns the URL to redirect the admin's browser to.
// The state binds the redirect to the requesting tenant.
func (s *Service) StartOAuth(ctx context.Context, tenantID string, provider esign.Provider) (string, error) {
	if s.esign == nil {
		return "", errors.New("esign not configured")
	}
	cfg, err := s.resolveOAuthConfig(ctx, tenantID, provider)
	if err != nil {
		return "", err
	}
	url, _, err := cfg.AuthorizeURLBuilder(tenantID)
	return url, err
}

// HandleOAuthCallback exchanges the auth code for tokens, persists
// them sealed, and returns the user-facing redirect URL the handler
// should 302 the admin to.
func (s *Service) HandleOAuthCallback(ctx context.Context, provider esign.Provider, code, state, userID string) (string, error) {
	if s.esign == nil {
		return "", errors.New("esign not configured")
	}
	// State is <tenantID>.<provider>.<hmac>; tenantID and provider are
	// plaintext so we can use them to look up the tenant's effective
	// config (DB row → env-mode fallback). VerifyState then checks
	// the HMAC against the resolved config's secret.
	tenantIDFromState, _, ok := esign.ParseState(state)
	if !ok {
		return "", errors.New("esign: malformed state")
	}
	cfg, err := s.resolveOAuthConfig(ctx, tenantIDFromState, provider)
	if err != nil {
		return "", err
	}
	tenantID, ok := cfg.VerifyState(state)
	if !ok {
		return "", errors.New("esign: state hmac mismatch")
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
		ConnectedBy: userID,
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

// Disconnect clears the connection.
func (s *Service) Disconnect(ctx context.Context, tenantID string, provider esign.Provider) error {
	return s.repo.DeleteESignToken(ctx, tenantID, string(provider))
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
		oc := s.esign.OAuthByProvider[provider]
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

// SendViaProvider creates an envelope at the vendor + stamps the
// envelope_id onto the signature_request inside one tx. Pre-existing
// CreateRequest already inserted the request row; this fills in the
// vendor reference + flips status to in_progress.
func (s *Service) SendViaProvider(ctx context.Context, tenantID, requestID string, provider esign.Provider, docName string, docBytes []byte, recipients []esign.Recipient, returnURL, subject, message string) (*esign.SendResp, error) {
	client, err := s.resolveClient(ctx, tenantID, provider)
	if err != nil {
		return nil, err
	}
	resp, err := client.Send(ctx, esign.SendReq{
		TenantID: tenantID, RequestID: requestID,
		Subject: subject, Message: message,
		DocumentName: docName, DocumentBytes: docBytes,
		Recipients: recipients, ReturnURL: returnURL,
	})
	if err != nil {
		return nil, err
	}
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return nil, err
	}
	err = database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		return s.repo.SetEnvelopeIDTx(ctx, tx, tenantID, requestID, resp.EnvelopeID)
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

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
		if err := s.repo.InsertESignEventTx(ctx, tx, row); err != nil {
			// Unique-violation = redelivery; treat as success.
			if isUniqueViolation(err) {
				s.log.Debug().Str("external_id", evt.ExternalID).Msg("esign webhook dedup")
				return nil
			}
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
		if err := s.ingestSignedDocument(ctx, tenantID, requestID, evt.EnvelopeID, evt.Provider); err != nil {
			s.log.Error().Err(err).Str("request_id", requestID).Msg("esign ingest signed pdf")
			return err
		}
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

	client, err := s.resolveClient(ctx, tenantID, provider)
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
	if s.ingest != nil {
		region, _ := s.ingest.ResolveDocumentRegion(ctx, tenantID, existing.DocumentID)
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
	return database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		if err := s.repo.CompleteTx(ctx, tx, tenantID, requestID); err != nil {
			return err
		}
		evt := database.NewOutboxEvent(tenantUUID, "dms.signature.completed.v1",
			"signature_request", reqUUID, completedPayload)
		return s.outbox.Insert(ctx, tx, evt)
	})
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
		client, err := s.resolveClient(ctx, r.TenantID, esign.Provider(r.Provider))
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

// HandleWebhook is the handler-facing entry point. Resolves a
// connector for the path-param provider, verifies HMAC via
// ParseWebhook, then ingests through the shared path.
func (s *Service) HandleWebhook(ctx context.Context, tenantID string, provider esign.Provider, headers http.Header, body []byte) error {
	client, err := s.resolveClient(ctx, tenantID, provider)
	if err != nil {
		return err
	}
	evt, err := client.ParseWebhook(headers, body)
	if err != nil {
		return err
	}
	return s.IngestWebhook(ctx, tenantID, evt)
}

// ListInProgressESign returns active vendor envelopes for the
// /admin status tab.
func (s *Service) ListInProgressESign(ctx context.Context, tenantID string) ([]map[string]any, error) {
	stale, err := s.repo.FindStaleInProgressRequests(ctx, 0)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(stale))
	for _, r := range stale {
		if r.TenantID != tenantID {
			continue
		}
		out = append(out, map[string]any{
			"request_id":   r.ID,
			"envelope_id":  r.EnvelopeID,
			"provider":     r.Provider,
			"status":       "in_progress",
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
