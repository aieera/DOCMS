// dms.notification.send.v1 consumer (Wave 0.2).
//
// The Python intelligence tasks (compliance_scan, ocr_quality) write
// dms.notification.send.v1 into the shared outbox with a ROLE-targeted
// payload — {tenant_id, subject, body, target_roles[], category} — not
// the user-id-targeted DeliveryPayload the dms.notify.> consumer expects.
// PR #69 bound dms.notification.> to a stream so the row no longer wedges
// the drain, but nothing DELIVERED it. This consumer resolves the target
// roles to concrete users and fans out through the normal Deliver path,
// so a compliance scan actually produces in-app notifications.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/services/notification/internal/model"
)

// notificationSendPayload is the role-targeted event shape emitted by the
// intelligence tasks (services/intelligence/app/tasks/*.py).
type notificationSendPayload struct {
	TenantID    string   `json:"tenant_id"`
	Subject     string   `json:"subject"`
	Body        string   `json:"body"`
	TargetRoles []string `json:"target_roles"`
	Category    string   `json:"category"`
	ResourceID  string   `json:"document_id"`
}

// StartNotificationSendConsumer subscribes to dms.notification.send.v1
// and delivers each event to every user holding one of its target roles.
// Separate durable from the dms.notify.> consumer so the two fan-out
// shapes don't share a cursor.
func (s *Service) StartNotificationSendConsumer(parent context.Context, js nats.JetStreamContext) error {
	if parent == nil {
		parent = context.Background()
	}
	_, err := js.Subscribe("dms.notification.send.v1", func(msg *nats.Msg) {
		p, ok := parseNotificationSend(msg.Data)
		if !ok {
			s.log.Warn().Str("subject", msg.Subject).Msg("notification.send event missing fields")
			_ = msg.Term()
			return
		}
		ctx, cancel := context.WithTimeout(parent, 15*time.Second)
		defer cancel()
		if corrID := msg.Header.Get("correlation-id"); corrID != "" {
			ctx = auth.SetCorrelationID(ctx, corrID)
		}

		users, err := s.repo.UsersForRoles(ctx, p.TenantID, p.TargetRoles)
		if err != nil {
			s.log.Error().Err(err).Msg("notification.send: resolve roles")
			_ = msg.Nak()
			return
		}
		if len(users) == 0 {
			// No recipient holds the target roles — nothing to deliver,
			// but the event was handled. Ack so it doesn't redeliver.
			_ = msg.Ack()
			return
		}
		typ := p.Category
		if typ == "" {
			typ = "notification"
		}
		if err := s.Deliver(ctx, model.DeliveryPayload{
			TenantID:     p.TenantID,
			UserIDs:      users,
			Type:         typ,
			Title:        p.Subject,
			Body:         p.Body,
			ResourceType: "document",
			ResourceID:   p.ResourceID,
		}); err != nil {
			s.log.Error().Err(err).Msg("notification.send: deliver")
			_ = msg.Nak()
			return
		}
		_ = msg.Ack()
	}, nats.Durable("notification-send"), nats.ManualAck(), nats.MaxDeliver(5))
	if err != nil {
		return fmt.Errorf("subscribe dms.notification.send.v1: %w", err)
	}
	s.log.Info().Msg("notification consumer subscribed to dms.notification.send.v1")
	return nil
}

// parseNotificationSend extracts the role-targeted payload from either a
// bare JSON body or a CloudEvents envelope (the outbox publisher wraps
// every emit in `data`). Returns false when the required fields are
// missing.
func parseNotificationSend(data []byte) (notificationSendPayload, bool) {
	var p notificationSendPayload
	_ = json.Unmarshal(data, &p)
	if p.TenantID == "" || len(p.TargetRoles) == 0 {
		var env struct {
			Data notificationSendPayload `json:"data"`
		}
		if json.Unmarshal(data, &env) == nil {
			p = env.Data
		}
	}
	if p.TenantID == "" || len(p.TargetRoles) == 0 {
		return p, false
	}
	return p, true
}
