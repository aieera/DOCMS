// Package service orchestrates connector operations: webhook management,
// NATS event fanout to webhooks, and connector CRUD.
package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/services/connector/internal/eventpolicy"
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

// ErrEventNotSubscribable rejects a webhook subscription to an event family
// that must never leave the platform (DEC-16: auth, session, API-key, user,
// audit, ...).
var ErrEventNotSubscribable = errors.New("event is not externally subscribable")

// CreateWebhook validates the events and URL, generates an HMAC secret, and
// persists. Every requested event must be on the eventpolicy allow-list; one
// bad entry rejects the whole request (checked before the URL probe so a
// rejected subscription never triggers an outbound request).
func (s *Service) CreateWebhook(ctx context.Context, tenantID, userID, url string, events []string) (*model.WebhookSubscription, error) {
	for _, ev := range events {
		if !eventpolicy.Subscribable(ev) {
			return nil, fmt.Errorf("%w: %q", ErrEventNotSubscribable, ev)
		}
	}
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

// GetDeliveryLog returns delivery history for a webhook. Rows queued before
// the allow-list existed may hold internal events (a password-reset event
// carried the plaintext token); they are never shown.
func (s *Service) GetDeliveryLog(ctx context.Context, tenantID, subID string, limit int) ([]*model.WebhookDelivery, error) {
	rows, err := s.repo.ListDeliveries(ctx, tenantID, subID, limit)
	if err != nil {
		return nil, err
	}
	out := rows[:0]
	for _, d := range rows {
		if eventpolicy.Deliverable(d.EventType) {
			out = append(out, d)
		}
	}
	return out, nil
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
	if !eventpolicy.Deliverable(src.EventType) {
		// Pre-allow-list row (e.g. a password-reset event): never resend.
		return nil, nil
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
	// one stream, so subscribe per family. Only the externally subscribable
	// families (eventpolicy, DEC-16) are consumed at all: dms.auth.*,
	// dms.session.*, dms.apikey.*, dms.user.* and every other internal
	// family never reach this process.
	subjects := eventpolicy.Families()
	handler := func(msg *nats.Msg) {
		ctx, cancel := context.WithTimeout(parent, 30*time.Second)
		defer cancel()
		if corrID := msg.Header.Get("correlation-id"); corrID != "" {
			ctx = auth.SetCorrelationID(ctx, corrID)
		}
		if s.fanoutMessage(ctx, s.repo, msg.Subject, msg.Data) {
			_ = msg.Ack()
		} else {
			_ = msg.Nak()
		}
	}
	for _, subj := range subjects {
		durable := "connector-" + strings.ReplaceAll(strings.TrimSuffix(subj, ".>"), ".", "_")
		if _, err := js.Subscribe(subj, handler,
			nats.Durable(durable), nats.ManualAck(), nats.MaxDeliver(3),
		); err != nil {
			// A family no stream covers yet (e.g. dms.tag.> before any
			// service emits it) has nothing to fan out; anything else is a
			// real broker problem.
			if errors.Is(err, nats.ErrNoMatchingStream) {
				s.log.Warn().Str("subject", subj).Msg("connector fanout: no stream covers subject; skipped")
				continue
			}
			return fmt.Errorf("subscribe %s: %w", subj, err)
		}
		s.log.Info().Str("subject", subj).Str("durable", durable).Msg("connector subscribed")
	}
	return nil
}

// fanoutStore is the slice of the repository the fan-out needs; an
// interface so the drop-before-lookup policy is unit-testable.
type fanoutStore interface {
	GetActiveWebhooksForEvent(ctx context.Context, tenantID, eventType string) ([]*model.WebhookSubscription, error)
	InsertDelivery(ctx context.Context, d *model.WebhookDelivery) error
}

// fanoutMessage turns one upstream event into a delivery row per matching
// subscription. It returns false only when the message should be redelivered
// (subscription lookup failed); every other outcome — including dropping an
// unparseable or tenant-less event — is final.
func (s *Service) fanoutMessage(ctx context.Context, store fanoutStore, subject string, raw []byte) bool {
	// DEC-16: drop anything outside the allow-list before any lookup. Both
	// the NATS subject and the envelope type must pass, so neither a legacy
	// subscription to an auth event nor a mislabelled envelope can route an
	// internal event to an external receiver.
	if !eventpolicy.Subscribable(subject) {
		return true
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return true
	}
	eventType, _ := envelope["type"].(string)
	if eventType == "" {
		eventType = subject
	}
	if !eventpolicy.Subscribable(eventType) {
		return true
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
		return true
	}
	subs, err := store.GetActiveWebhooksForEvent(ctx, tenantID, eventType)
	if err != nil {
		s.log.Error().Err(err).Str("event", eventType).Msg("lookup webhooks")
		return false
	}
	for _, sub := range subs {
		delivery := &model.WebhookDelivery{
			ID:             newID(),
			SubscriptionID: sub.ID,
			TenantID:       tenantID,
			EventType:      eventType,
			Payload:        raw,
			Attempts:       0,
			CreatedAt:      time.Now().UTC(),
		}
		if err := store.InsertDelivery(ctx, delivery); err != nil {
			s.log.Error().Err(err).Str("sub", sub.ID).Msg("insert delivery")
		}
	}
	return true
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
