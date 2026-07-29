// Task 4 (2026-07-28 task-service design) — outbox event + notification
// emission. Every emitter here writes into the `outbox` table inside the
// caller's pgx.Tx (never publishes to NATS directly — see CLAUDE.md's
// transactional-outbox note); the OutboxPublisher started in
// cmd/server/main.go forwards published-false rows to NATS out of band.
package service

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/pkg/database"
)

// taskAggregateType is the outbox aggregate_type for every event this
// service emits, domain or notify.
const taskAggregateType = "task"

// newTaskEvent marshals payload and builds an OutboxEvent ready for
// Repos.Outbox.Insert. database.NewOutboxEvent takes json.RawMessage (a
// single return value, not (v, err) — verified against
// pkg/database/outbox.go:54), so the marshal happens here.
func newTaskEvent(tenantID uuid.UUID, eventType string, taskID uuid.UUID, payload map[string]any) (*database.OutboxEvent, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return database.NewOutboxEvent(tenantID, eventType, taskAggregateType, taskID, raw), nil
}

// emitTaskEvent builds and inserts one domain event (dms.task.*.v1) in
// tx. Every call site in this package must be inside the same
// withTenantTx as the row write the event describes, so a crash between
// them can't leave a write unaccompanied by its event.
func (s *TaskService) emitTaskEvent(ctx context.Context, tx pgx.Tx, tenantID, taskID uuid.UUID, eventType string, payload map[string]any) error {
	evt, err := newTaskEvent(tenantID, eventType, taskID, payload)
	if err != nil {
		return err
	}
	return s.Repos.Outbox.Insert(ctx, tx, evt)
}

// emitNotify builds and inserts one dms.notify.task.<suffix>.v1 event
// for userIDs. Payload shape ports the document service's
// emitTaskAssigned (tasks.go:397-419) verbatim for the keys the
// notification consumer's DeliveryPayload requires: tenant_id, user_ids,
// type, title, body, resource_type, resource_id. No-ops on an empty
// recipient list so call sites don't need their own guard.
func (s *TaskService) emitNotify(ctx context.Context, tx pgx.Tx, tenantID, taskID uuid.UUID, notifType, title, body string, userIDs []uuid.UUID) error {
	if len(userIDs) == 0 {
		return nil
	}
	ids := make([]string, len(userIDs))
	for i, id := range userIDs {
		ids[i] = id.String()
	}
	return s.emitTaskEvent(ctx, tx, tenantID, taskID, "dms.notify.task."+notifType+".v1", map[string]any{
		"tenant_id":     tenantID.String(),
		"user_ids":      ids,
		"type":          "task." + notifType,
		"title":         title,
		"body":          body,
		"resource_type": "task",
		"resource_id":   taskID.String(),
	})
}
