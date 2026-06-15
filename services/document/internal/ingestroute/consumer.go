package ingestroute

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
	enums "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	temporalclient "go.temporal.io/sdk/client"

	"github.com/aieera/sedoc/pkg/events"
)

// Consumer subscribes to dms.ingestion.processed.v1 (emitted by the intelligence
// worker once OCR + key extraction finish) and starts the IngestAndRoute
// workflow for the staged item. Durable push consumer on INGESTION_EVENTS with
// manual ack. Workflow-id ingest-<item_id> + RejectDuplicate make the start
// idempotent under NATS at-least-once redelivery.
type Consumer struct {
	js  nats.JetStreamContext
	tc  temporalclient.Client
	log zerolog.Logger
}

// NewConsumer constructs the consumer. tc must be a live Temporal client.
func NewConsumer(js nats.JetStreamContext, tc temporalclient.Client, log zerolog.Logger) *Consumer {
	return &Consumer{js: js, tc: tc, log: log}
}

// Start binds the durable subscription. Non-fatal for the caller: log + skip if
// it can't subscribe (e.g. the stream isn't provisioned in this env).
func (c *Consumer) Start() error {
	_, err := c.js.Subscribe("dms.ingestion.processed.v1", c.handle,
		nats.Durable("document-ingest-route"),
		nats.ManualAck(),
		nats.AckWait(2*time.Minute),
	)
	return err
}

func (c *Consumer) handle(msg *nats.Msg) {
	var env events.CloudEvent
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		c.log.Warn().Err(err).Msg("ingestion-route: bad envelope; dropping")
		_ = msg.Ack()
		return
	}
	var data struct {
		IngestionItemID string `json:"ingestion_item_id"`
		TenantID        string `json:"tenant_id"`
	}
	_ = json.Unmarshal(env.Data, &data)
	tenantID := data.TenantID
	if tenantID == "" {
		tenantID = env.TenantID
	}
	if tenantID == "" || data.IngestionItemID == "" {
		// Malformed / older event — nothing to route. Ack so it doesn't
		// redeliver forever.
		_ = msg.Ack()
		return
	}
	if _, err := uuid.Parse(data.IngestionItemID); err != nil {
		_ = msg.Ack()
		return
	}

	opts := temporalclient.StartWorkflowOptions{
		ID:                    "ingest-" + data.IngestionItemID,
		TaskQueue:             TaskQueue,
		WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
	}
	_, err := c.tc.ExecuteWorkflow(context.Background(), opts, IngestAndRoute,
		IngestInput{TenantID: tenantID, ItemID: data.IngestionItemID})
	if err != nil {
		var already *serviceerror.WorkflowExecutionAlreadyStarted
		if errors.As(err, &already) {
			// This item already has a routing workflow (running or finished) —
			// idempotent on ingestion_item.id.
			_ = msg.Ack()
			return
		}
		c.log.Error().Err(err).Str("item", data.IngestionItemID).
			Msg("ingestion-route: start workflow failed; will redeliver")
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
}
