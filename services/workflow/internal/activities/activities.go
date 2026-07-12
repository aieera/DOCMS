// Package activities implements Temporal activity functions.
package activities

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/expr-lang/expr"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/database"
)

// Activities holds deps injected from main.
type Activities struct {
	Pool   *pgxpool.Pool
	Outbox *database.OutboxRepository
	// Redis is used by DSR token verification (Wave 11.4). Optional —
	// nil is accepted; activities that depend on Redis return a typed
	// error in that case.
	Redis *redis.Client
	// ServiceURLs maps service name → base URL for cross-service
	// activities (Wave 12.4). Keys: "search", "qdrant", "connector",
	// "signature". Empty map or missing key → activity soft-no-ops + logs.
	ServiceURLs map[string]string
	// InternalKey is SEDOC_INTERNAL_API_KEY, presented as
	// X-Internal-Service-Key on service-to-service activity calls that hit
	// an internal endpoint (e.g. the signature seal-ceremony seal, ADR 0025).
	InternalKey string
	// JS is the JetStream context for emitting CloudEvents from
	// activities. ADR 0085 saved-search alert workflow uses this
	// to publish dms.notify.saved_search_match.v1. nil is accepted;
	// EmitSavedSearchMatch returns a typed error when JS isn't wired.
	JS  nats.JetStreamContext
	Log zerolog.Logger
}

// CreateTask inserts a pending task row.
func (a *Activities) CreateTask(ctx context.Context, tenantID, instanceID, documentID, stepName, assigneeID string) error {
	id, _ := uuid.NewV7()
	return a.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO workflow_tasks (id, tenant_id, instance_id, document_id, step_name, assignee_id, status, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, 'pending', $7)
		`, id, tenantID, instanceID, documentID, stepName, assigneeID, time.Now().UTC())
		return err
	})
}

// taskStatusFor maps a raw workflow outcome (what approval/review/signature
// send: "approve"/"approved"/"reject"/"sign"/"decline"/"cancelled"/…) onto
// the closed set the workflow_tasks.status CHECK constraint allows
// (pending, in_progress, completed, rejected, delegated, escalated,
// skipped). Writing the raw outcome as status violated the CHECK; the raw
// value is preserved separately in the `outcome` column. The mapping is
// total: any unrecognized outcome falls through to 'completed' (a terminal
// state) with the raw value still recorded, because failing the activity
// here would re-wedge the very task-completion path this fixes.
func taskStatusFor(outcome string) string {
	switch outcome {
	case "approve", "approved", "sign", "signed", "complete", "completed":
		return "completed"
	case "reject", "rejected", "decline", "declined", "deny", "denied":
		return "rejected"
	case "escalate", "escalated":
		return "escalated"
	case "delegate", "delegated":
		return "delegated"
	case "skip", "skipped", "cancel", "cancelled", "canceled", "recall", "recalled":
		return "skipped"
	case "pending", "in_progress":
		return outcome
	default:
		return "completed"
	}
}

// CompleteTask marks the instance's pending task completed, mapping the raw
// outcome to a CHECK-legal status and preserving the raw outcome verbatim.
//
// The task is targeted by an explicit-id subquery rather than a bare
// `UPDATE … ORDER BY … LIMIT` (which Postgres rejects as a syntax error —
// the original defect). The caller has only (instance, step), not a task id
// — CreateTask doesn't return one — so we pick the instance's oldest
// pending task. FOR UPDATE SKIP LOCKED is load-bearing, not decoration:
// review and signature run reviewers/signers in PARALLEL, so multiple
// pending tasks can exist for one instance and several CompleteTask
// activities can run at once; SKIP LOCKED makes each claim a DISTINCT row,
// so N decisions complete N tasks. (Per-assignee targeting — matching the
// decision to its exact reviewer's row rather than the oldest — needs the
// activity to carry the assignee/task id and is tracked as a follow-up;
// stepIndex is retained in the signature for that work.)
func (a *Activities) CompleteTask(ctx context.Context, tenantID, instanceID string, stepIndex int, outcome, notes string) error {
	status := taskStatusFor(outcome)
	return a.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE workflow_tasks
			   SET status = $1, outcome = $2, notes = $3, completed_at = $4
			 WHERE tenant_id = $5 AND id = (
			     SELECT id FROM workflow_tasks
			      WHERE tenant_id = $5 AND instance_id = $6 AND status = 'pending'
			      ORDER BY created_at ASC
			      LIMIT 1
			      FOR UPDATE SKIP LOCKED
			 )
		`, status, outcome, notes, time.Now().UTC(), tenantID, instanceID)
		return err
	})
}

// DelegateTask reassigns a pending task.
func (a *Activities) DelegateTask(ctx context.Context, tenantID, instanceID string, stepIndex int, newAssignee string) error {
	return a.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE workflow_tasks SET assignee_id = $1, status = 'delegated'
			WHERE tenant_id = $2 AND instance_id = $3 AND status = 'pending'
		`, newAssignee, tenantID, instanceID)
		return err
	})
}

// NotifyAssignee enqueues a notification for the assigned reviewer via the
// outbox table. The outbox publisher delivers the event to NATS, so if this
// activity runs but the broker is briefly down, the event is still durably
// persisted and delivered once NATS is reachable.
func (a *Activities) NotifyAssignee(ctx context.Context, tenantID, assigneeID, documentID, taskName string) error {
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return fmt.Errorf("tenant_id: %w", err)
	}
	aggID := parseUUIDOrNew(documentID)

	payload, _ := json.Marshal(map[string]string{
		"tenant_id":   tenantID,
		"user_id":     assigneeID,
		"document_id": documentID,
		"task_name":   taskName,
		"type":        "workflow.step_assigned",
	})
	evt := database.NewOutboxEvent(tenantUUID, "dms.notify.workflow_assigned.v1", "workflow_task", aggID, payload)
	return database.WithTenantTx(ctx, a.Pool, tenantUUID, func(tx pgx.Tx) error {
		return a.Outbox.Insert(ctx, tx, evt)
	})
}

// SetDocumentLifecycle changes the document's lifecycle state.
func (a *Activities) SetDocumentLifecycle(ctx context.Context, tenantID, documentID, state string) error {
	return a.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE documents SET lifecycle_state = $1, updated_at = $2
			WHERE tenant_id = $3 AND id = $4
		`, state, time.Now().UTC(), tenantID, documentID)
		return err
	})
}

// PublishEvent enqueues a CloudEvents-shaped envelope for the given subject
// through the outbox. Callers pass an arbitrary subject (e.g.
// "dms.workflow.completed.v1"); the outbox publisher will deliver it to NATS.
func (a *Activities) PublishEvent(ctx context.Context, tenantID, subject string, data map[string]string) error {
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return fmt.Errorf("tenant_id: %w", err)
	}
	aggID := parseUUIDOrNew(data["instance_id"])

	payload, _ := json.Marshal(map[string]any{
		"specversion": "1.0", "type": subject,
		"source": "/vaultdms/workflow", "data": data,
	})
	evt := database.NewOutboxEvent(tenantUUID, subject, "workflow_instance", aggID, payload)
	return database.WithTenantTx(ctx, a.Pool, tenantUUID, func(tx pgx.Tx) error {
		return a.Outbox.Insert(ctx, tx, evt)
	})
}

// EvaluateCondition runs an expr-lang expression against document metadata.
func (a *Activities) EvaluateCondition(ctx context.Context, tenantID, documentID, expression string) (bool, error) {
	var metadata json.RawMessage
	if err := a.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT COALESCE(custom_metadata, '{}') FROM documents WHERE tenant_id = $1 AND id = $2`,
			tenantID, documentID).Scan(&metadata)
	}); err != nil {
		return false, fmt.Errorf("fetch metadata: %w", err)
	}
	var env map[string]any
	if err := json.Unmarshal(metadata, &env); err != nil {
		return false, fmt.Errorf("parse metadata: %w", err)
	}
	program, err := expr.Compile(expression, expr.Env(env))
	if err != nil {
		return false, fmt.Errorf("compile expr: %w", err)
	}
	result, err := expr.Run(program, env)
	if err != nil {
		return false, fmt.Errorf("eval expr: %w", err)
	}
	b, ok := result.(bool)
	if !ok {
		return false, fmt.Errorf("expr result not bool: %T", result)
	}
	return b, nil
}

// parseUUIDOrNew returns the parsed UUID if s is a valid uuid, otherwise a
// fresh v7. Used for outbox aggregate_id slots where upstream callers may
// pass non-UUID identifiers (e.g. Temporal workflow ids).
func parseUUIDOrNew(s string) uuid.UUID {
	if id, err := uuid.Parse(s); err == nil {
		return id
	}
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.New()
	}
	return id
}
