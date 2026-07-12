package eventstream

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nats-io/nats.go"
)

// StartMirror subscribes to every dms.{domain}.>.v1 subject the platform
// emits and re-publishes each message onto
// `tenant.{tenant_id}.events.{domain}.{action}.v1`. Per-tenant streams
// are created on demand the first time we see a tenant's event.
//
// Idempotency: NATS uses Nats-Msg-Id for dedupe; we copy the upstream
// message ID forward so a redelivered upstream copy doesn't create a
// duplicate downstream copy.
//
// Failure modes:
//   - Envelope without tenant_id → log + Ack (we never want to retry these,
//     they would loop forever).
//   - Stream provision failure → Nak so the upstream consumer retries with
//     backoff (typically transient — JetStream API hiccup, disk full).
//   - Publish failure → Nak.
func (s *Service) StartMirror(parent context.Context) error {
	if parent == nil {
		parent = context.Background()
	}
	// Keep this subject list in sync with the connector service's
	// existing fanout (services/connector/internal/service/service.go
	// → StartEventFanout). If a new domain ships, both subscribe lists
	// need the new prefix.
	subjects := []string{
		"dms.document.>", "dms.version.>", "dms.workspace.>",
		"dms.user.>", "dms.session.>", "dms.apikey.>", "dms.auth.>",
		"dms.policy.>", "dms.permission.>",
		"dms.billing.>", "dms.subscription.>", "dms.usage.>",
		"dms.audit.>",
		"dms.workflow.>", "dms.task.>",
		"dms.sharelink.>", "dms.folder.>",
		"dms.signature.>", "dms.hold.>",
		"dms.dsr.>",
	}
	handler := func(msg *nats.Msg) {
		s.handleUpstream(parent, msg)
	}
	for _, subj := range subjects {
		durable := "evstream-mirror-" + strings.ReplaceAll(strings.TrimSuffix(subj, ".>"), ".", "_")
		if _, err := s.js.Subscribe(subj, handler,
			nats.Durable(durable), nats.ManualAck(), nats.MaxDeliver(5),
		); err != nil {
			// A subject with no JetStream coverage is fine — it just
			// means the platform hasn't started emitting that family
			// yet. Log + continue rather than failing the whole boot.
			s.log.Warn().Err(err).Str("subject", subj).Msg("eventstream mirror skipped (no stream)")
			continue
		}
		s.log.Info().Str("subject", subj).Str("durable", durable).Msg("eventstream mirror subscribed")
	}
	return nil
}

func (s *Service) handleUpstream(ctx context.Context, msg *nats.Msg) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(msg.Data, &envelope); err != nil {
		_ = msg.Ack()
		return
	}
	tenantID := pluckTenant(envelope)
	if tenantID == "" {
		_ = msg.Ack()
		return
	}
	if err := s.ensureStream(ctx, tenantID); err != nil {
		s.log.Error().Err(err).Str("tenant_id", tenantID).Msg("mirror ensureStream")
		_ = msg.Nak()
		return
	}
	// Rewrite subject: dms.document.created.v1 → tenant.<id>.events.document.created.v1.
	rewritten := "tenant." + tenantID + ".events." + strings.TrimPrefix(msg.Subject, "dms.")
	publish := &nats.Msg{
		Subject: rewritten,
		Data:    msg.Data,
		Header:  nats.Header{},
	}
	// Forward the upstream message ID for downstream dedupe. If the
	// upstream didn't set one, fall back to the JetStream sequence.
	if id := msg.Header.Get(nats.MsgIdHdr); id != "" {
		publish.Header.Set(nats.MsgIdHdr, id)
	} else if meta, err := msg.Metadata(); err == nil {
		publish.Header.Set(nats.MsgIdHdr, fmt.Sprintf("%s-%d", msg.Subject, meta.Sequence.Stream))
	}
	if _, err := s.js.PublishMsg(publish); err != nil {
		s.log.Error().Err(err).Str("tenant_id", tenantID).Str("subject", rewritten).Msg("mirror publish")
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
}

// pluckTenant returns the tenant_id carried on the envelope. We accept
// it at either the top level or nested under `data`, because the
// platform publishes both shapes depending on the service vintage.
func pluckTenant(envelope map[string]json.RawMessage) string {
	if t, ok := envelope["tenant_id"]; ok {
		var s string
		if json.Unmarshal(t, &s) == nil && s != "" {
			return s
		}
	}
	if d, ok := envelope["data"]; ok {
		var inner map[string]json.RawMessage
		if json.Unmarshal(d, &inner) == nil {
			if t, ok := inner["tenant_id"]; ok {
				var s string
				if json.Unmarshal(t, &s) == nil && s != "" {
					return s
				}
			}
		}
	}
	return ""
}
