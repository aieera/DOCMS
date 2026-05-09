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
	"github.com/redis/go-redis/v9"
	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/pkg/database"
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
	// activities (Wave 12.4). Keys: "search", "qdrant", "connector".
	// Empty map or missing key → activity soft-no-ops + logs.
	ServiceURLs map[string]string
	// JS is the JetStream context for emitting CloudEvents from
	// activities. ADR 0085 saved-search alert workflow uses this
	// to publish dms.notify.saved_search_match.v1. nil is accepted;
	// EmitSavedSearchMatch returns a typed error when JS isn't wired.
	JS nats.JetStreamContext
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

// CompleteTask marks a task as completed with outcome.
func (a *Activities) CompleteTask(ctx context.Context, tenantID, instanceID string, stepIndex int, status, notes string) error {
	return a.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE workflow_tasks SET status = $1, notes = $2, completed_at = $3
			WHERE tenant_id = $4 AND instance_id = $5 AND status = 'pending'
			ORDER BY created_at ASC LIMIT 1
		`, status, notes, time.Now().UTC(), tenantID, instanceID)
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
