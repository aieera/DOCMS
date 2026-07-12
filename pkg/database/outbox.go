package database

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/tracing"
)

// OutboxEvent is the minimal shape of an `outbox` row. The fields are
// deliberately primitive (uuid.UUID, string, json.RawMessage, time.Time) so
// that this package can be imported by every service without pulling in any
// service-specific domain types.
//
// The caller is responsible for marshaling its own domain payload to
// json.RawMessage BEFORE calling Insert; this package never sees the struct.
type OutboxEvent struct {
	ID            uuid.UUID
	TenantID      uuid.UUID
	EventType     string // e.g. "dms.document.created.v1"
	AggregateType string // e.g. "document" | "workflow" | "signature"
	AggregateID   uuid.UUID
	Payload       json.RawMessage
	CreatedAt     time.Time
	// Request-context audit fields. Populated by the outbox Insert
	// from auth.GetUserID / GetUserName / GetClientIP on ctx; empty
	// when the event was produced by a background goroutine without
	// a request ctx. The outbox publisher copies these into the
	// CloudEvents envelope so the audit consumer can stamp
	// audit_events.actor_id / actor_name / ip_address.
	ActorID   *uuid.UUID
	ActorName string
	IPAddress string
	// TraceContext is the W3C trace context (traceparent/tracestate) of
	// the request that produced this event, captured at Insert time. The
	// publisher injects it onto the NATS message so a consumer's span is
	// a child of the producing request's span — this is how a distributed
	// trace crosses the async outbox→NATS boundary. Empty for events
	// produced without an active span.
	TraceContext map[string]string
}

// NewOutboxEvent builds an event with a fresh UUIDv7 id and the current UTC
// time. UUIDv7 (time-ordered) keeps the `outbox` BTree leaf pages dense and
// guarantees created_at ordering matches insert order at the sub-millisecond
// level, which matters for the publisher's FOR UPDATE SKIP LOCKED loop.
func NewOutboxEvent(
	tenantID uuid.UUID,
	eventType, aggregateType string,
	aggregateID uuid.UUID,
	payload json.RawMessage,
) *OutboxEvent {
	id, err := uuid.NewV7()
	if err != nil {
		// Extremely unlikely (NewV7 only errors if the crypto/rand source
		// fails); fall back to v4 rather than panic.
		id = uuid.New()
	}
	return &OutboxEvent{
		ID:            id,
		TenantID:      tenantID,
		EventType:     eventType,
		AggregateType: aggregateType,
		AggregateID:   aggregateID,
		Payload:       payload,
		CreatedAt:     time.Now().UTC(),
	}
}

// OutboxRepository is a zero-state helper that inserts outbox rows inside
// the caller's transaction. A single repository instance is safe to share
// across goroutines; it holds no state.
type OutboxRepository struct{}

// NewOutboxRepository constructs a repository. Kept for API symmetry with
// the domain repositories each service defines.
func NewOutboxRepository() *OutboxRepository { return &OutboxRepository{} }

// Insert writes event into the `outbox` table inside the caller-supplied
// transaction. Callers MUST pass the same pgx.Tx they used for the business
// write — that is the entire point of the transactional-outbox pattern.
//
// The `published` flag is pinned to false here; the OutboxPublisher flips it
// once the event has been delivered to NATS.
func (r *OutboxRepository) Insert(ctx context.Context, tx pgx.Tx, event *OutboxEvent) error {
	if event == nil {
		return fmt.Errorf("outbox: nil event")
	}
	if event.ID == uuid.Nil || event.TenantID == uuid.Nil || event.AggregateID == uuid.Nil {
		return fmt.Errorf("outbox: id, tenant_id, aggregate_id are required")
	}
	if event.EventType == "" || event.AggregateType == "" {
		return fmt.Errorf("outbox: event_type and aggregate_type are required")
	}
	if len(event.Payload) == 0 {
		return fmt.Errorf("outbox: payload must be non-empty JSON")
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	// Stamp the actor/IP from ctx so audit_events rows inherit them
	// via the outbox publisher's CloudEvents envelope. Callers without
	// a request ctx (background goroutines) get NULLs — correct.
	var (
		actorID   any
		actorName any
		ipAddr    any
	)
	if uid, err := auth.GetUserID(ctx); err == nil && uid != uuid.Nil {
		actorID = uid
	}
	if name := auth.GetUserName(ctx); name != "" {
		actorName = name
	}
	if ip := auth.GetClientIP(ctx); ip != "" {
		ipAddr = ip
	}
	// Capture the current trace context so the publisher can restore it
	// on the NATS message. Stored as JSONB (nullable — no active span → NULL).
	var traceArg any
	if event.TraceContext == nil {
		event.TraceContext = tracing.InjectToMap(ctx)
	}
	if len(event.TraceContext) > 0 {
		if b, mErr := json.Marshal(event.TraceContext); mErr == nil {
			traceArg = b
		}
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO outbox (id, tenant_id, event_type, aggregate_type, aggregate_id,
		                    payload, published, created_at,
		                    actor_id, actor_name, ip_address, trace_context)
		VALUES ($1, $2, $3, $4, $5, $6, false, $7, $8, $9, $10, $11)
	`,
		event.ID, event.TenantID, event.EventType, event.AggregateType,
		event.AggregateID, event.Payload, event.CreatedAt,
		actorID, actorName, ipAddr, traceArg,
	)
	if err != nil {
		return fmt.Errorf("outbox insert: %w", err)
	}
	return nil
}
