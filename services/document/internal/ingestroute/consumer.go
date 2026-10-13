package ingestroute

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
// manual ack. Workflow-id ingest-<item_id> + AllowDuplicateFailedOnly make
// the start idempotent under NATS at-least-once redelivery while letting a
// failed routing run be retried.
type Consumer struct {
	js  nats.JetStreamContext
	tc  workflowStarter
	log zerolog.Logger
}

// workflowStarter is the slice of the Temporal client the consumer uses.
type workflowStarter interface {
	ExecuteWorkflow(ctx context.Context, options temporalclient.StartWorkflowOptions, workflow interface{}, args ...interface{}) (temporalclient.WorkflowRun, error)
}

// acker is the slice of *nats.Msg the consumer settles messages with.
type acker interface {
	Ack(opts ...nats.AckOpt) error
	Nak(opts ...nats.AckOpt) error
	Term(opts ...nats.AckOpt) error
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

func (c *Consumer) handle(msg *nats.Msg) { c.process(msg, msg.Data) }

func (c *Consumer) process(msg acker, raw []byte) {
	in, err := parseProcessed(raw)
	if err != nil {
		// Malformed / inconsistent event (D-24): nothing can ever route it.
		// Term (not Ack) so JetStream stops redelivering AND records a
		// terminated advisory instead of silently counting it as handled.
		c.log.Warn().Err(err).Msg("ingestion-route: malformed processed event; terminating")
		_ = msg.Term()
		return
	}

	opts := temporalclient.StartWorkflowOptions{
		ID:        "ingest-" + in.ItemID,
		TaskQueue: TaskQueue,
		// A running or completed routing workflow dedupes the redelivery;
		// one that failed (e.g. its retries ran out) may be started again
		// so the item is not stranded (D-24).
		WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY,
	}
	_, err = c.tc.ExecuteWorkflow(context.Background(), opts, IngestAndRoute, in)
	if err != nil {
		var already *serviceerror.WorkflowExecutionAlreadyStarted
		if errors.As(err, &already) {
			// This item already has a routing workflow (running or finished) —
			// idempotent on ingestion_item.id.
			_ = msg.Ack()
			return
		}
		c.log.Error().Err(err).Str("item", in.ItemID).
			Msg("ingestion-route: start workflow failed; will redeliver")
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
}

// parseProcessed validates a dms.ingestion.processed.v1 envelope: the tenant
// (data.tenant_id, else the envelope's) and the item id must be non-nil
// UUIDs, and a tenant named in both places must agree. The workflow input
// carries the canonical UUID strings.
func parseProcessed(raw []byte) (IngestInput, error) {
	var env events.CloudEvent
	if err := json.Unmarshal(raw, &env); err != nil {
		return IngestInput{}, fmt.Errorf("envelope: %w", err)
	}
	var data struct {
		IngestionItemID string `json:"ingestion_item_id"`
		TenantID        string `json:"tenant_id"`
	}
	if len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, &data); err != nil {
			return IngestInput{}, fmt.Errorf("data: %w", err)
		}
	}
	tenant, err := parseNonNilUUID(data.TenantID)
	if strings.TrimSpace(data.TenantID) == "" {
		tenant, err = parseNonNilUUID(env.TenantID)
	}
	if err != nil {
		return IngestInput{}, fmt.Errorf("tenant_id: %w", err)
	}
	if strings.TrimSpace(env.TenantID) != "" {
		envTenant, eerr := parseNonNilUUID(env.TenantID)
		if eerr != nil || envTenant != tenant {
			return IngestInput{}, errors.New("tenant_id: envelope and payload disagree")
		}
	}
	item, err := parseNonNilUUID(data.IngestionItemID)
	if err != nil {
		return IngestInput{}, fmt.Errorf("ingestion_item_id: %w", err)
	}
	return IngestInput{TenantID: tenant.String(), ItemID: item.String()}, nil
}

func parseNonNilUUID(s string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(s))
	if err != nil {
		return uuid.Nil, err
	}
	if id == uuid.Nil {
		return uuid.Nil, errors.New("nil uuid")
	}
	return id, nil
}
