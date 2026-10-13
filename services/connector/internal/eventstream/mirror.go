package eventstream

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/aieera/sedoc/services/connector/internal/eventpolicy"
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
//   - Envelope without a UUID tenant (tenantid / tenant_id) → Ack (we never
//     want to retry these, they would loop forever).
//   - Stream provision failure → Nak so the upstream consumer retries with
//     backoff (typically transient — JetStream API hiccup, disk full).
//   - Publish failure → Nak.
func (s *Service) StartMirror(parent context.Context) error {
	if parent == nil {
		parent = context.Background()
	}
	subjects := mirrorSubjects()
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

// mirrorSubjects is the set of upstream families mirrored into per-tenant
// streams: exactly the externally subscribable families (DEC-16). Auth,
// session, API-key, user, audit, billing and every other internal family
// are never consumed, so they cannot reach a tenant's event stream (and
// from there the polling endpoint or the SSE tail).
func mirrorSubjects() []string {
	return eventpolicy.Families()
}

func (s *Service) handleUpstream(ctx context.Context, msg *nats.Msg) {
	if !eventpolicy.Subscribable(msg.Subject) {
		_ = msg.Ack()
		return
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(msg.Data, &envelope); err != nil {
		_ = msg.Ack()
		return
	}
	// The envelope type must pass too, so a mislabelled internal event on
	// an allowed subject is not forwarded.
	if t, ok := envelope["type"]; ok {
		var typ string
		if json.Unmarshal(t, &typ) == nil && typ != "" && !eventpolicy.Subscribable(typ) {
			_ = msg.Ack()
			return
		}
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

// pluckTenant returns the tenant carried on the envelope, canonicalised.
// pkg/events CloudEvents put it at the top level as `tenantid` (N-2: the
// mirror used to read only `tenant_id` and dropped nearly every event);
// older hand-built payloads use top-level or data.tenant_id. The value
// becomes a NATS subject token, so it must parse as a UUID — a dot or
// wildcard would otherwise address another tenant's subtree.
func pluckTenant(envelope map[string]json.RawMessage) string {
	str := func(raw json.RawMessage) string {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return ""
		}
		return s
	}
	var candidate string
	if t, ok := envelope["tenantid"]; ok {
		candidate = str(t)
	}
	if candidate == "" {
		if t, ok := envelope["tenant_id"]; ok {
			candidate = str(t)
		}
	}
	if candidate == "" {
		if d, ok := envelope["data"]; ok {
			var inner map[string]json.RawMessage
			if json.Unmarshal(d, &inner) == nil {
				if t, ok := inner["tenant_id"]; ok {
					candidate = str(t)
				}
			}
		}
	}
	id, err := uuid.Parse(candidate)
	if err != nil || id == uuid.Nil {
		return ""
	}
	return id.String()
}

// servable reports whether a message read from a per-tenant stream
// (tenant.<id>.events.<domain>.<action>.v1) may be handed to a tenant
// client. The mirror only writes subscribable families now, but events
// mirrored before the DEC-16 allow-list stay retained for DefaultRetention,
// so the read paths re-check the upstream family.
func servable(tenantSubject string) bool {
	if !strings.HasPrefix(tenantSubject, "tenant.") {
		return false
	}
	_, rest, ok := strings.Cut(strings.TrimPrefix(tenantSubject, "tenant."), ".events.")
	if !ok {
		return false
	}
	return eventpolicy.Subscribable("dms." + rest)
}
