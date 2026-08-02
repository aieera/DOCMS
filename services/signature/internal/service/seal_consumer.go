// Seal consumer (ADR 0025 / Wave 9.2b increment 2).
//
// Subscribes to dms.signature.completed.v1 (emitted by the workflow service's
// SignatureWorkflow on all-signers-approved) and applies the organizational
// PAdES seal to the signed version via the server-seal pipeline. Durable push
// consumer on the SIGNATURE_EVENTS stream with manual ack.
package service

import (
	"context"
	"encoding/json"
	"time"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/events"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
)

// SealConsumer auto-triggers the server-seal when a workflow signature completes.
type SealConsumer struct {
	js   nats.JetStreamContext
	svc  *Service
	log  zerolog.Logger
	base context.Context // process-lifetime ctx from Start; per-message ctxs derive from it (C4)
}

const (
	// maxSealAttempts bounds redelivery so an unsealable message (bad payload,
	// deleted version) is terminated rather than retried forever.
	maxSealAttempts = 8
	// sealRetryBackoff is multiplied by the delivery count, capped at
	// maxSealRetryDelay: 15s, 30s, 45s … 2m.
	sealRetryBackoff  = 15 * time.Second
	maxSealRetryDelay = 2 * time.Minute
)

// deliveryCount reports how many times JetStream has delivered this message
// (1 on the first). Metadata is unavailable for a non-JetStream message, in
// which case we treat it as a first delivery.
func deliveryCount(msg *nats.Msg) int {
	md, err := msg.Metadata()
	if err != nil || md == nil {
		return 1
	}
	return int(md.NumDelivered)
}

// NewSealConsumer constructs the consumer. svc must have a configured sealer
// (AddSealer) or SealVersion returns "not configured" and the message is Nak'd.
func NewSealConsumer(js nats.JetStreamContext, svc *Service, log zerolog.Logger) *SealConsumer {
	return &SealConsumer{js: js, svc: svc, log: log}
}

// Start binds the durable subscription. Non-fatal for the caller: log + skip
// if it can't subscribe (e.g. the stream isn't provisioned in this env).
func (c *SealConsumer) Start(ctx context.Context) error {
	c.base = ctx
	_, err := c.js.Subscribe("dms.signature.completed.v1", c.handle,
		nats.Durable("signature-seal"),
		nats.ManualAck(),
		nats.AckWait(2*time.Minute), // a B-LT DSS sign + upload can take a while
	)
	return err
}

func (c *SealConsumer) handle(msg *nats.Msg) {
	var env events.CloudEvent
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		c.log.Warn().Err(err).Msg("seal consumer: bad envelope; dropping")
		_ = msg.Ack()
		return
	}
	var data struct {
		DocumentID  string `json:"document_id"`
		VersionID   string `json:"version_id"`
		RequestID   string `json:"request_id"`
		InitiatedBy string `json:"initiated_by"`
	}
	_ = json.Unmarshal(env.Data, &data)
	if env.TenantID == "" || data.DocumentID == "" || data.VersionID == "" {
		// A declined signature, or an older event without version_id — nothing
		// to seal. Ack so it doesn't redeliver forever.
		_ = msg.Ack()
		return
	}
	tenantUUID, err := uuid.Parse(env.TenantID)
	if err != nil {
		_ = msg.Ack()
		return
	}
	actor, _ := uuid.Parse(data.InitiatedBy) // uuid.Nil if absent; storage requires non-nil

	// System-seal identity: admin role so the storage OPA owner/admin rule
	// fires; the acting user is the workflow initiator.
	ctx := auth.WithUser(c.base, auth.UserInfo{
		TenantID: tenantUUID, ID: actor, Role: "admin",
	})
	// Fallback path: seal the ceremony if the Temporal workflow's SealCeremony
	// activity (the primary path, ADR 0025 Wave 9) hasn't already. The shared
	// ClaimSeal guard makes this exactly-once across both triggers + NATS
	// redelivery — alreadySealed=true means the workflow (or a prior delivery)
	// won the claim, so this is an idempotent no-op.
	res, alreadySealed, err := c.svc.SealCeremonyForRequest(ctx, env.TenantID, data.DocumentID, data.VersionID, data.InitiatedBy, data.RequestID)
	if err != nil {
		// Back the retry off instead of Nak'ing bare. A bare Nak redelivers
		// immediately, so a failure that cannot resolve itself (this one was
		// a missing initiated_by → "user_id: required") spun at ~28 msg/s
		// indefinitely, burning CPU across signature AND storage. Give up
		// after maxSealAttempts so a poison message can't hold the loop.
		attempt := deliveryCount(msg)
		if attempt >= maxSealAttempts {
			c.log.Error().Err(err).
				Str("document_id", data.DocumentID).Str("version_id", data.VersionID).
				Int("attempts", attempt).
				Msg("seal consumer: giving up after max attempts; document left unsealed")
			_ = msg.Term()
			return
		}
		delay := time.Duration(attempt) * sealRetryBackoff
		if delay > maxSealRetryDelay {
			delay = maxSealRetryDelay
		}
		c.log.Error().Err(err).
			Str("document_id", data.DocumentID).Str("version_id", data.VersionID).
			Int("attempt", attempt).Dur("retry_in", delay).
			Msg("seal consumer: seal failed; will redeliver")
		_ = msg.NakWithDelay(delay)
		return
	}
	if alreadySealed {
		c.log.Info().Str("request_id", data.RequestID).Msg("seal consumer: already sealed (workflow-owned); skipping")
		_ = msg.Ack()
		return
	}
	c.log.Info().
		Str("document_id", data.DocumentID).
		Str("new_version_id", res.NewVersionID).
		Str("level", res.Level).
		Msg("workflow signature sealed (consumer fallback)")
	_ = msg.Ack()
}
