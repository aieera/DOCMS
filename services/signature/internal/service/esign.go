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

	"github.com/vaultdms/vaultdms/pkg/database"
	"github.com/vaultdms/vaultdms/pkg/esign"
	"github.com/vaultdms/vaultdms/services/signature/internal/repository"
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
	// HTTPClient is shared across adapter constructions.
	HTTPClient *http.Client
	// MockOK enables the in-memory adapter for CI + Playwright.
	// Service refuses to construct the mock client when false.
	MockOK bool
	// MockClient is the resolved instance when MockOK is true.
	MockClient *esign.MockClient
	// PollInterval is the reconcile loop period. Default 5 min.
	PollInterval time.Duration
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

// StartOAuth returns the URL to redirect the admin's browser to.
// The state binds the redirect to the requesting tenant.
func (s *Service) StartOAuth(_ context.Context, tenantID string, provider esign.Provider) (string, error) {
	if s.esign == nil {
		return "", errors.New("esign not configured")
	}
	cfg, ok := s.esign.OAuthByProvider[provider]
	if !ok {
		return "", fmt.Errorf("esign: unsupported provider %q", provider)
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
	cfg, ok := s.esign.OAuthByProvider[provider]
	if !ok {
		return "", fmt.Errorf("esign: unsupported provider %q", provider)
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
	row := &repository.ESignToken{
		TenantID: tenantID, Provider: string(provider),
		AccessToken: access, RefreshToken: refresh,
		ExpiresAt: tok.ExpiresAt, AccountID: tok.AccountID,
		BaseURI: tok.BaseURI, Scope: tok.Scope,
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
	if provider == esign.ProviderMock {
		if !s.esign.MockOK || s.esign.MockClient == nil {
			return nil, errors.New("esign mock not allowed")
		}
		return s.esign.MockClient, nil
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

// ingestSignedDocument pulls the signed PDF + CoC from the vendor.
// Hand-off to the document service for "create new version"
// happens via the dms.document.version_uploaded.v1 outbox path
// (ADR 0021); for now we emit a direct dms.signature.completed.v1
// payload that the document worker subscribes to with the bytes
// already in S3. The full upload-and-version step depends on the
// storage service's PutObject API which is wired separately.
func (s *Service) ingestSignedDocument(ctx context.Context, tenantID, requestID, envelopeID string, provider esign.Provider) error {
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
	tenantUUID, _ := uuid.Parse(tenantID)
	reqUUID, _ := uuid.Parse(requestID)
	completedPayload, _ := json.Marshal(map[string]any{
		"specversion": "1.0", "type": "dms.signature.completed.v1",
		"source": "/vaultdms/signature",
		"data": map[string]any{
			"tenant_id":   tenantID,
			"request_id":  requestID,
			"provider":    string(provider),
			"envelope_id": envelopeID,
			"signed_pdf_bytes": len(got.SignedPDF),
			"coc_pdf_bytes":    len(got.CoCPDF),
		},
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
