// Package classification denormalises the intelligence compliance scan
// (dms.compliance.completed.v1, ADR 0054) onto the documents row so the read
// path can gate access on sensitivity without a cross-table join (§8). It maps
// the scan's overall_risk + PHI/PII counts to a security_classification level
// and the has_phi/has_pii flags. A human ('manual'/'records') classification is
// authoritative and is never lowered by the scan (see SetDocumentSensitivity).
package classification

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/events"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/repository"
)

// Consumer is a durable push consumer on COMPLIANCE_EVENTS that keeps the
// documents row's sensitivity metadata in sync with the latest scan.
type Consumer struct {
	js    nats.JetStreamContext
	pool  *pgxpool.Pool
	repos *repository.Repositories
	log   zerolog.Logger
}

// NewConsumer constructs the consumer.
func NewConsumer(js nats.JetStreamContext, pool *pgxpool.Pool, repos *repository.Repositories, log zerolog.Logger) *Consumer {
	return &Consumer{js: js, pool: pool, repos: repos, log: log}
}

// Start binds the durable subscription. Non-fatal for the caller: log + skip if
// it can't subscribe (e.g. the stream isn't provisioned in this env).
func (c *Consumer) Start() error {
	_, err := c.js.Subscribe("dms.compliance.completed.v1", c.handle,
		nats.Durable("document-classification-denorm"),
		nats.ManualAck(),
		nats.AckWait(2*time.Minute),
	)
	return err
}

type compliancePayload struct {
	TenantID    string `json:"tenant_id"`
	DocumentID  string `json:"document_id"`
	OverallRisk string `json:"overall_risk"`
	PIICount    int    `json:"pii_count"`
	PHICount    int    `json:"phi_count"`
}

func (c *Consumer) handle(msg *nats.Msg) {
	var env events.CloudEvent
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		c.log.Warn().Err(err).Msg("classification-denorm: bad envelope; dropping")
		_ = msg.Ack()
		return
	}
	var data compliancePayload
	_ = json.Unmarshal(env.Data, &data)
	tenantRaw := data.TenantID
	if tenantRaw == "" {
		tenantRaw = env.TenantID
	}
	tenantID, terr := uuid.Parse(tenantRaw)
	docID, derr := uuid.Parse(data.DocumentID)
	if terr != nil || derr != nil {
		// Malformed / older event — nothing to denormalise. Ack so it doesn't
		// redeliver forever.
		_ = msg.Ack()
		return
	}

	hasPHI := data.PHICount > 0
	hasPII := data.PIICount > 0
	class := model.RiskToClassification(data.OverallRisk, hasPHI)

	// NATS consumer: re-establish the tenant context before any DB work so RLS
	// applies (CLAUDE.md). No inbound request context exists for a NATS
	// message, so a fresh context is correct here.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := database.WithTenantTx(ctx, c.pool, tenantID, func(tx pgx.Tx) error {
		return c.repos.Classification.SetDocumentSensitivity(ctx, tx, tenantID, docID, class, hasPHI, hasPII)
	})
	if err != nil {
		c.log.Error().Err(err).Str("document", docID.String()).
			Msg("classification-denorm: update failed; will redeliver")
		_ = msg.Nak()
		return
	}
	c.log.Debug().Str("document", docID.String()).Str("classification", class).
		Bool("phi", hasPHI).Msg("classification-denorm: sensitivity updated")
	_ = msg.Ack()
}
