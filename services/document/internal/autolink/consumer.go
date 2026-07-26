package autolink

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/events"
)

// maxTargetsPerRule bounds fan-out for pathological metadata (e.g. a
// number value shared by hundreds of documents). Capped hits are logged
// — never silently truncated.
const maxTargetsPerRule = 25

// Store is the persistence surface the consumer needs. Interface so the
// linking policy is testable without Postgres.
type Store interface {
	DocumentMetadata(ctx context.Context, tenantID, docID uuid.UUID) (map[string]any, error)
	FindTargets(ctx context.Context, tenantID uuid.UUID, r Rule, self uuid.UUID) ([]uuid.UUID, error)
	InsertEdge(ctx context.Context, tenantID, src, dst uuid.UUID, confidence float64, meta map[string]any) error
}

// Consumer is a durable push consumer on document lifecycle events that
// materialises metadata-derived relationships as contract-graph edges.
type Consumer struct {
	js    nats.JetStreamContext
	store Store
	log   zerolog.Logger
}

// NewConsumer constructs the consumer with the Postgres-backed store.
func NewConsumer(js nats.JetStreamContext, store Store, log zerolog.Logger) *Consumer {
	return &Consumer{js: js, store: store, log: log}
}

// Start binds the durable subscriptions. Non-fatal contract for the
// caller (log + skip when the stream isn't provisioned), mirroring the
// classification consumer.
func (c *Consumer) Start() error {
	if _, err := c.js.Subscribe("dms.document.created.v1", c.handle,
		nats.Durable("document-autolink-created"),
		nats.ManualAck(),
		nats.AckWait(2*time.Minute),
	); err != nil {
		return err
	}
	_, err := c.js.Subscribe("dms.document.updated.v1", c.handle,
		nats.Durable("document-autolink-updated"),
		nats.ManualAck(),
		nats.AckWait(2*time.Minute),
	)
	return err
}

type docEventPayload struct {
	DocumentID string `json:"document_id"`
	TenantID   string `json:"tenant_id"`
}

func (c *Consumer) handle(msg *nats.Msg) {
	var env events.CloudEvent
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		c.log.Warn().Err(err).Msg("autolink: bad envelope; dropping")
		_ = msg.Ack()
		return
	}
	var data docEventPayload
	_ = json.Unmarshal(env.Data, &data)
	tenantRaw := data.TenantID
	if tenantRaw == "" {
		tenantRaw = env.TenantID
	}
	tenantID, terr := uuid.Parse(tenantRaw)
	docID, derr := uuid.Parse(data.DocumentID)
	if terr != nil || derr != nil {
		// Malformed / older event — nothing to link. Ack so it doesn't
		// redeliver forever.
		_ = msg.Ack()
		return
	}

	// NATS consumer: tenant context is re-established from the envelope
	// inside the store (WithTenantTx) before any DB work — RLS applies.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.process(ctx, tenantID, docID); err != nil {
		c.log.Error().Err(err).Str("document", docID.String()).
			Msg("autolink: linking failed; will redeliver")
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
}

// process derives rules from the document's metadata and UPSERTs one
// `references` edge per matched document. Idempotent: the unique
// (tenant, src, dst, type) index absorbs redelivery, and number-match
// edges are canonicalised (smaller UUID = src) so the pair yields the
// same row no matter which side's event fires first.
func (c *Consumer) process(ctx context.Context, tenantID, docID uuid.UUID) error {
	meta, err := c.store.DocumentMetadata(ctx, tenantID, docID)
	if err != nil {
		return err
	}
	rules := DeriveRules(meta)
	if len(rules) == 0 {
		return nil
	}

	type pair struct{ src, dst uuid.UUID }
	seen := map[pair]bool{}

	for _, r := range rules {
		targets, err := c.store.FindTargets(ctx, tenantID, r, docID)
		if err != nil {
			return err
		}
		if len(targets) > maxTargetsPerRule {
			c.log.Warn().Str("document", docID.String()).Str("key", r.Key).
				Int("matches", len(targets)).Int("cap", maxTargetsPerRule).
				Msg("autolink: rule fan-out capped")
			targets = targets[:maxTargetsPerRule]
		}
		for _, target := range targets {
			if target == docID {
				continue
			}
			src, dst := docID, target
			if !r.Outbound {
				src, dst = target, docID
			}
			if r.Kind == "number" {
				// Direction is arbitrary for shared numbers — canonicalise
				// so A→B and B→A collapse to one edge.
				if bytes.Compare(dst[:], src[:]) < 0 {
					src, dst = dst, src
				}
			}
			p := pair{src, dst}
			if seen[p] {
				continue
			}
			seen[p] = true
			err := c.store.InsertEdge(ctx, tenantID, src, dst, r.Confidence, map[string]any{
				"rule":          r.Kind,
				"matched_key":   r.Key,
				"matched_value": r.Value,
			})
			if err != nil {
				return err
			}
		}
	}
	return nil
}
