package service

// event_service.go owns the outbox + hash-chain seams. Every
// state-change helper elsewhere in the package calls into
// appendEvent(WithPrev) so the chain stays linear and the outbox row
// is emitted in the same tx as the DB write.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/vaultdms/vaultdms/pkg/database"
	"github.com/vaultdms/vaultdms/services/acknowledgement/internal/model"
)

// Outbox event subjects. Versioned (v1) per the Wave-5 subject rules.
const (
	EventCampaignCreated = "dms.acknowledgement.campaign.created.v1"
	EventCampaignClosed  = "dms.acknowledgement.campaign.closed.v1"
	EventAcknowledged    = "dms.acknowledgement.acknowledged.v1"
	EventReminded        = "dms.acknowledgement.reminded.v1"
	EventEscalated       = "dms.acknowledgement.escalated.v1"

	// Notification fan-out subjects consumed by
	// services/notification. Shape must satisfy
	// notification/internal/model.DeliveryPayload (tenant_id,
	// user_ids, type, title, body, resource_type, resource_id).
	//
	// See ADR 0032 for why these sit under dms.notify.* rather than
	// the dms.<aggregate>.<event>.v1 shape.
	NotifyCampaignCreated = "dms.notify.acknowledgement.campaign.created.v1"
	NotifyReminded        = "dms.notify.acknowledgement.reminded.v1"
	NotifyEscalated       = "dms.notify.acknowledgement.escalated.v1"
)

// emitNotify inserts a `dms.notify.*` outbox row whose payload
// matches notification-service's DeliveryPayload shape. Kept local
// so the ack service doesn't take a hard dep on the notification
// module's types (which would create a workspace import cycle if
// the two ever share a helper). Resource fields are set so the
// inbox entry deep-links back to the campaign.
//
// Safe to call with users==nil — the helper no-ops so callers don't
// have to guard every site.
func (s *Service) emitNotify(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, campaignID uuid.UUID,
	users []uuid.UUID,
	subject, notifyType, title, body string,
) error {
	if len(users) == 0 {
		return nil
	}
	uids := make([]string, 0, len(users))
	for _, u := range users {
		uids = append(uids, u.String())
	}
	payload := map[string]any{
		"tenant_id":     tenantID.String(),
		"user_ids":      uids,
		"type":          notifyType,
		"title":         title,
		"body":          body,
		"resource_type": "acknowledgement_campaign",
		"resource_id":   campaignID.String(),
	}
	body2, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	evt := database.NewOutboxEvent(tenantID, subject, "acknowledgement", campaignID, body2)
	return s.outbox.Insert(ctx, tx, evt)
}

// appendEvent is the "first event for this campaign" path — no prev.
func (s *Service) appendEvent(ctx context.Context, tx pgx.Tx, tenantID, campaignID uuid.UUID, assignmentID *uuid.UUID, actorID *uuid.UUID, eventType string, payload map[string]any) error {
	return s.appendEventWithPrev(ctx, tx, tenantID, campaignID, assignmentID, actorID, eventType, payload, nil)
}

func (s *Service) appendEventWithPrev(ctx context.Context, tx pgx.Tx, tenantID, campaignID uuid.UUID, assignmentID, actorID *uuid.UUID, eventType string, payload map[string]any, prev []byte) error {
	body, err := canonicalJSON(payload)
	if err != nil {
		return err
	}
	selfHash := eventSelfHash(prev, body)
	e := &model.Event{
		TenantID:     tenantID,
		ID:           uuid.New(),
		CampaignID:   campaignID,
		AssignmentID: assignmentID,
		ActorUserID:  actorID,
		EventType:    eventType,
		Payload:      body,
		PrevHash:     prev,
		SelfHash:     selfHash,
		CreatedAt:    s.clock(),
	}
	if err := s.repo.AppendEvent(ctx, tx, e); err != nil {
		return err
	}
	// Outbox emission: handled separately so downstream (audit,
	// notifications) sees the event even if it's only interested in
	// the subject, not the chain.
	evt := database.NewOutboxEvent(tenantID, eventType, "acknowledgement", campaignID, body)
	return s.outbox.Insert(ctx, tx, evt)
}

// eventSelfHash computes SHA-256(prev || payload). Deterministic.
func eventSelfHash(prev, payload []byte) []byte {
	h := sha256.New()
	if len(prev) > 0 {
		h.Write(prev)
	}
	h.Write(payload)
	return h.Sum(nil)
}

// canonicalJSON serialises a payload with sorted keys so the
// hash-chain entry is byte-stable across replicas and replays. We
// do NOT rely on Go's incidental map-key ordering — a refactor to
// struct-shaped payloads would silently break chain verification.
// The implementation emits `{"k1":v1,"k2":v2,...}` with a top-level
// key sort; nested maps/structs are marshaled with the default
// encoder (any further canonicalisation can land when a concrete
// consumer needs it — today every payload is a flat map).
func canonicalJSON(payload map[string]any) ([]byte, error) {
	keys := make([]string, 0, len(payload))
	for k := range payload {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := json.Marshal(payload[k])
		if err != nil {
			return nil, err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}
