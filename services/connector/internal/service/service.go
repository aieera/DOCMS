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

	"github.com/vaultdms/vaultdms/pkg/auth"
	"github.com/vaultdms/vaultdms/services/connector/internal/model"
	"github.com/vaultdms/vaultdms/services/connector/internal/repository"
	"github.com/vaultdms/vaultdms/services/connector/internal/webhook"
)

// Service is the connector facade.
type Service struct {
	repo *repository.Repository
	log  zerolog.Logger
}

// Config is DI.
type Config struct {
	Repo   *repository.Repository
	Logger zerolog.Logger
}

// New creates a Service.
func New(cfg Config) *Service {
	return &Service{repo: cfg.Repo, log: cfg.Logger}
}

// ---- Webhook CRUD ---------------------------------------------------------

// CreateWebhook validates the URL, generates an HMAC secret, and persists.
func (s *Service) CreateWebhook(ctx context.Context, tenantID, userID, url string, events []string) (*model.WebhookSubscription, error) {
	if err := webhook.ValidateURL(url); err != nil {
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
	envelope := map[string]any{
		"type":           "dms.webhook.test.v1",
		"subscription":   subID,
		"tenant_id":      tenantID,
		"data":           map[string]any{"message": "VaultDMS test delivery"},
		"correlation_id": newID(),
		"occurred_at":    time.Now().UTC().Format(time.RFC3339Nano),
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
		tenantID, _ := data["tenant_id"].(string)
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
