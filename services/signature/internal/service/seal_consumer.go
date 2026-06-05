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

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/events"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
)

// SealConsumer auto-triggers the server-seal when a workflow signature completes.
type SealConsumer struct {
	js  nats.JetStreamContext
	svc *Service
	log zerolog.Logger
}

// NewSealConsumer constructs the consumer. svc must have a configured sealer
// (AddSealer) or SealVersion returns "not configured" and the message is Nak'd.
func NewSealConsumer(js nats.JetStreamContext, svc *Service, log zerolog.Logger) *SealConsumer {
	return &SealConsumer{js: js, svc: svc, log: log}
}

// Start binds the durable subscription. Non-fatal for the caller: log + skip
// if it can't subscribe (e.g. the stream isn't provisioned in this env).
func (c *SealConsumer) Start() error {
	_, err := c.js.Subscribe("dms.signature.completed.v1", c.handle,
		nats.Durable("signature-seal"),
		nats.ManualAck(),
		nats.AckWait(2*60*1e9), // 2m — a B-LT DSS sign + upload can take a while
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
	ctx := auth.WithUser(context.Background(), auth.UserInfo{
		TenantID: tenantUUID, ID: actor, Role: "admin",
	})
	res, err := c.svc.SealVersion(ctx, env.TenantID, data.DocumentID, data.VersionID,
		data.InitiatedBy, "SeDoc Workflow Seal", "Sealed on workflow signature completion")
	if err != nil {
		c.log.Error().Err(err).
			Str("document_id", data.DocumentID).Str("version_id", data.VersionID).
			Msg("seal consumer: seal failed; will redeliver")
		_ = msg.Nak()
		return
	}
	c.log.Info().
		Str("document_id", data.DocumentID).
		Str("new_version_id", res.NewVersionID).
		Str("level", res.Level).
		Msg("workflow signature sealed")
	_ = msg.Ack()
}
