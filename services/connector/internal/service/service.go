// Package service orchestrates connector operations: webhook management,
// NATS event fanout to webhooks, and connector CRUD.
package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/services/connector/internal/ingest"
	"github.com/aieera/sedoc/services/connector/internal/model"
	"github.com/aieera/sedoc/services/connector/internal/repository"
	"github.com/aieera/sedoc/services/connector/internal/webhook"
)

// Service is the connector facade.
type Service struct {
	repo   *repository.Repository
	log    zerolog.Logger
	kicker func()
	// Per-tenant native-connector wiring (Google, Salesforce, M365, …).
	// SetConnectorDeps populates these from main.go after construction.
	connSealingKey []byte
	connHMAC       []byte
	connRedirect   string
	// ingest runs the server-side document ingest flow (Drive import,
	// and later Salesforce/M365 pulls). Nil disables import actions —
	// SetIngestClient wires it from main.go when storage is reachable.
	ingest *ingest.Client
	// allowPrivateWebhooks relaxes the outbound-webhook URL guard for
	// on-prem deployments (cfg.WebhookAllowPrivateTargets) — permits
	// http/private-IP receivers. Default false keeps the https + public-IP
	// SSRF guard.
	allowPrivateWebhooks bool
}

// SetIngestClient wires the server-side ingest path used by Drive
// import. Optional; nil leaves import endpoints returning a 503.
func (s *Service) SetIngestClient(c *ingest.Client) { s.ingest = c }

// SetWorkerKicker wires the delivery worker's Kick so test-send /
// redeliver can wake the worker immediately instead of waiting up
// to 5 s for the next poll tick. Optional — nil kicker is fine, the
// poll loop still drains pending rows.
func (s *Service) SetWorkerKicker(kick func()) { s.kicker = kick }

func (s *Service) kick() {
	if s.kicker != nil {
		s.kicker()
	}
}

// Config is DI.
type Config struct {
	Repo   *repository.Repository
	Logger zerolog.Logger
	// AllowPrivateWebhookTargets relaxes the webhook URL SSRF guard for
	// self-hosted/on-prem deployments. Sourced from
	// cfg.WebhookAllowPrivateTargets (env SEDOC_WEBHOOK_ALLOW_PRIVATE).
	AllowPrivateWebhookTargets bool
}

// New creates a Service.
func New(cfg Config) *Service {
	return &Service{repo: cfg.Repo, log: cfg.Logger, allowPrivateWebhooks: cfg.AllowPrivateWebhookTargets}
}

// ---- Webhook CRUD ---------------------------------------------------------

// CreateWebhook validates the URL, generates an HMAC secret, and persists.
func (s *Service) CreateWebhook(ctx context.Context, tenantID, userID, url string, events []string) (*model.WebhookSubscription, error) {
	if err := webhook.ValidateURL(url, s.allowPrivateWebhooks); err != nil {
		return nil, err
	}
	wh := &model.WebhookSubscription{
		ID:        newID(),
		TenantID:  tenantID,
		URL:       url,
		Secret:    generateSecret(),
		Events:    events,
		Active:    true,
		CreatedBy: userID,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.repo.CreateWebhook(ctx, wh); err != nil {
		return nil, err
	}
	return wh, nil
}

// ListWebhooks returns all webhooks for a tenant.
func (s *Service) ListWebhooks(ctx context.Context, tenantID string) ([]*model.WebhookSubscription, error) {
	return s.repo.ListWebhooks(ctx, tenantID)
}

// DeleteWebhook removes a webhook subscription.
func (s *Service) DeleteWebhook(ctx context.Context, tenantID, id string) error {
	return s.repo.DeleteWebhook(ctx, tenantID, id)
}

// GetDeliveryLog returns delivery history for a webhook.
func (s *Service) GetDeliveryLog(ctx context.Context, tenantID, subID string, limit int) ([]*model.WebhookDelivery, error) {
	return s.repo.ListDeliveries(ctx, tenantID, subID, limit)
}

// RotateSecret mints a new HMAC secret for the webhook and returns
// the rotated subscription (including the new secret — caller must
// surface it to the operator exactly once, like an API key).
func (s *Service) RotateSecret(ctx context.Context, tenantID, id string) (*model.WebhookSubscription, error) {
	newSecret := generateSecret()
	if err := s.repo.RotateSecret(ctx, tenantID, id, newSecret); err != nil {
		return nil, err
	}
	wh, err := s.repo.GetWebhook(ctx, tenantID, id)
	if err != nil || wh == nil {
		return nil, err
	}
	wh.Secret = newSecret
	return wh, nil
}

// SendTestEvent queues a synthetic `dms.webhook.test.v1` delivery
// against an existing subscription so the operator can verify their
// receiver wiring (signature math, network reachability, response
// handling) without waiting for a real domain event. Reuses the
// regular delivery worker so the test payload goes through the same
// HMAC + retry + DLQ pipeline as production traffic.
func (s *Service) SendTestEvent(ctx context.Context, tenantID, subID string) (*model.WebhookDelivery, error) {
	sub, err := s.repo.GetWebhook(ctx, tenantID, subID)
	if err != nil || sub == nil {
		return nil, err
	}
	// Proper CloudEvent envelope — same field names as real domain events
	// (pkg/events.CloudEvent) so subscribers can dedup on `id` and read
	// `tenantid`. The previous ad-hoc shape (no `id`; `tenant_id` instead of
	// `tenantid`) was rejected by CloudEvent-style receivers (e.g. the ERP
	// inbound webhook, which keys idempotency on envelope.id).
	envelope := map[string]any{
		"specversion":   "1.0",
		"id":            newID(),
		"source":        "/vaultdms/connector",
		"type":          "dms.webhook.test.v1",
		"subject":       subID,
		"time":          time.Now().UTC().Format(time.RFC3339Nano),
		"tenantid":      tenantID,
		"correlationid": newID(),
		"data":          map[string]any{"message": "SeDoc test delivery", "subscription_id": subID},
	}
	payload, _ := json.Marshal(envelope)
	d := &model.WebhookDelivery{
		ID:             newID(),
		SubscriptionID: subID,
		TenantID:       tenantID,
		EventType:      "dms.webhook.test.v1",
		Payload:        payload,
		Attempts:       0,
		CreatedAt:      time.Now().UTC(),
	}
	if err := s.repo.InsertDelivery(ctx, d); err != nil {
		return nil, err
	}
	s.kick()
	return d, nil
}

// RedeliverDelivery clones a past delivery into a fresh pending row
// so the outbox dispatcher picks it up on its next tick. The original
// row is preserved for audit.
func (s *Service) RedeliverDelivery(ctx context.Context, tenantID, deliveryID string) (*model.WebhookDelivery, error) {
	src, err := s.repo.GetDelivery(ctx, tenantID, deliveryID)
	if err != nil || src == nil {
		return nil, err
	}
	clone := &model.WebhookDelivery{
		ID:             newID(),
		SubscriptionID: src.SubscriptionID,
		TenantID:       src.TenantID,
		EventType:      src.EventType,
		Payload:        src.Payload,
		Attempts:       0,
		CreatedAt:      time.Now().UTC(),
	}
	if err := s.repo.InsertDelivery(ctx, clone); err != nil {
		return nil, err
	}
	s.kick()
	return clone, nil
}

// ---- Event fanout to webhooks ---------------------------------------------

// StartEventFanout subscribes to all domain events and creates webhook
// deliveries for matching subscriptions. `parent` is the service
// lifecycle ctx so SIGTERM cascades into in-flight handlers
// (Wave 6 Prompt 6.4).
func (s *Service) StartEventFanout(parent context.Context, js nats.JetStreamContext) error {
	if parent == nil {
		parent = context.Background()
	}
	// JetStream requires each subscribe subject to be covered by exactly
	// one stream. `dms.>` spans many streams, so subscribe per-subject.
	// Keep in sync with pkg/events.DefaultStreams.
	subjects := []string{
		"dms.document.>", "dms.version.>", "dms.workspace.>",
		"dms.user.>", "dms.session.>", "dms.apikey.>", "dms.auth.>",
		"dms.policy.>", "dms.permission.>",
		"dms.billing.>", "dms.subscription.>", "dms.usage.>",
		"dms.audit.>",
		"dms.search.>",
		"dms.workflow.>", "dms.task.>",
		"dms.ocr.>", "dms.classify.>", "dms.embed.>", "dms.ner.>",
		"dms.notify.>",
		// dms.signature.> (SIGNATURE_EVENTS stream) — needed so
		// dms.signature.completed.v1 fans out to subscribers (e.g. the ERP
		// inbound webhook that marks an invoice/quote signed).
		"dms.signature.>",
		"dms.sharelink.>", "dms.folder.>", "dms.intelligence.>", "dms.rotation.>",
	}
	handler := func(msg *nats.Msg) {
		ctx, cancel := context.WithTimeout(parent, 30*time.Second)
		defer cancel()
		if corrID := msg.Header.Get("correlation-id"); corrID != "" {
			ctx = auth.SetCorrelationID(ctx, corrID)
		}
		var envelope map[string]any
		if err := json.Unmarshal(msg.Data, &envelope); err != nil {
			_ = msg.Ack()
			return
		}
		eventType, _ := envelope["type"].(string)
		if eventType == "" {
			eventType = msg.Subject
		}
		data, _ := envelope["data"].(map[string]any)
		if data == nil {
			data = envelope
		}
		// Tenant is carried at the CloudEvent top level (`tenantid`); only
		// some hand-built payloads also duplicate it into data.tenant_id.
		// Prefer the envelope — reading only data.tenant_id silently dropped
		// state_changed / deleted / version events (same class as the search
		// "hoist tenantid" fix).
		tenantID, _ := envelope["tenantid"].(string)
		if tenantID == "" {
			tenantID, _ = data["tenant_id"].(string)
		}
		if tenantID == "" {
			_ = msg.Ack()
			return
		}
		subs, err := s.repo.GetActiveWebhooksForEvent(ctx, tenantID, eventType)
		if err != nil {
			s.log.Error().Err(err).Str("event", eventType).Msg("lookup webhooks")
			_ = msg.Nak()
			return
		}
		for _, sub := range subs {
			delivery := &model.WebhookDelivery{
				ID:             newID(),
				SubscriptionID: sub.ID,
				TenantID:       tenantID,
				EventType:      eventType,
				Payload:        msg.Data,
				Attempts:       0,
				CreatedAt:      time.Now().UTC(),
			}
			if err := s.repo.InsertDelivery(ctx, delivery); err != nil {
				s.log.Error().Err(err).Str("sub", sub.ID).Msg("insert delivery")
			}
		}
		_ = msg.Ack()
	}
	for _, subj := range subjects {
		durable := "connector-" + strings.ReplaceAll(strings.TrimSuffix(subj, ".>"), ".", "_")
		if _, err := js.Subscribe(subj, handler,
			nats.Durable(durable), nats.ManualAck(), nats.MaxDeliver(3),
		); err != nil {
			return fmt.Errorf("subscribe %s: %w", subj, err)
		}
		s.log.Info().Str("subject", subj).Str("durable", durable).Msg("connector subscribed")
	}
	return nil
}

// ---- Connector CRUD -------------------------------------------------------

// ListConnectors returns all configured connectors for a tenant.
func (s *Service) ListConnectors(ctx context.Context, tenantID string) ([]*model.ConnectorConfig, error) {
	return s.repo.ListConnectors(ctx, tenantID)
}

// GetConnector returns a specific connector config.
func (s *Service) GetConnector(ctx context.Context, tenantID, provider string) (*model.ConnectorConfig, error) {
	return s.repo.GetConnector(ctx, tenantID, provider)
}

// UpsertConnector saves or updates a connector config.
func (s *Service) UpsertConnector(ctx context.Context, cc *model.ConnectorConfig) error {
	if cc.ID == "" {
		cc.ID = newID()
	}
	if cc.CreatedAt.IsZero() {
		cc.CreatedAt = time.Now().UTC()
	}
	return s.repo.UpsertConnector(ctx, cc)
}

// ---- helpers --------------------------------------------------------------

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func generateSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return "whsec_" + hex.EncodeToString(b)
}
