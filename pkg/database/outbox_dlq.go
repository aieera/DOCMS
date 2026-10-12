package database

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ReplayDLQOptions selects the dead-lettered rows ReplayDLQ moves back.
type ReplayDLQOptions struct {
	// TenantID limits the replay to one tenant. uuid.Nil means every
	// tenant in the organizations registry.
	TenantID uuid.UUID
	// Limit caps the number of rows replayed in this call (oldest
	// dead-lettered first). <= 0 means 100.
	Limit int
	// DryRun lists the rows that would be replayed without changing them.
	DryRun bool
}

// ReplayedEvent describes one outbox_dlq row ReplayDLQ moved (or, on a
// dry run, would move) back into outbox.
type ReplayedEvent struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	EventType string
	Attempts  int
	LastError string
}

// ReplayDLQ moves dead-lettered rows from outbox_dlq back into outbox with
// attempts reset to 0, no recorded error and no deferral, so the publisher
// retries them on its next poll with a full retry budget. The event id is
// kept, so JetStream deduplication (Nats-Msg-Id) and consumer idempotency
// still apply. Each tenant's rows move in one transaction under that
// tenant's RLS context.
func ReplayDLQ(ctx context.Context, pool *pgxpool.Pool, opts ReplayDLQOptions) ([]ReplayedEvent, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 100
	}
	tenants := []uuid.UUID{opts.TenantID}
	if opts.TenantID == uuid.Nil {
		var err error
		if tenants, err = ListTenantIDs(ctx, pool); err != nil {
			return nil, err
		}
	}

	var out []ReplayedEvent
	for _, tenantID := range tenants {
		if len(out) >= limit {
			break
		}
		var got []ReplayedEvent
		err := WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
			var err error
			got, err = replayTenantDLQ(ctx, tx, tenantID, limit-len(out), opts.DryRun)
			return err
		})
		if err != nil {
			// This tenant's transaction rolled back; earlier tenants committed.
			return out, fmt.Errorf("replay dlq for tenant %s: %w", tenantID, err)
		}
		out = append(out, got...)
	}
	return out, nil
}

func replayTenantDLQ(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, limit int, dryRun bool) ([]ReplayedEvent, error) {
	const pick = `
		SELECT id FROM outbox_dlq
		 WHERE tenant_id = $1
		 ORDER BY dead_lettered_at, id
		 LIMIT $2`
	q := `
		SELECT id, tenant_id, event_type, attempts, COALESCE(last_error, '')
		  FROM outbox_dlq
		 WHERE id IN (` + pick + `)
		 ORDER BY dead_lettered_at, id`
	if !dryRun {
		q = `
		WITH moved AS (
			DELETE FROM outbox_dlq
			 WHERE id IN (` + pick + ` FOR UPDATE SKIP LOCKED)
			RETURNING id, tenant_id, event_type, aggregate_type, aggregate_id, payload,
			          created_at, actor_id, actor_name, ip_address, user_agent,
			          attempts, last_error, dead_lettered_at
		), requeued AS (
			INSERT INTO outbox (id, tenant_id, event_type, aggregate_type, aggregate_id, payload,
			                    published, created_at, actor_id, actor_name, ip_address, user_agent,
			                    attempts, last_error, next_attempt_at)
			SELECT id, tenant_id, event_type, aggregate_type, aggregate_id, payload,
			       false, created_at, actor_id, actor_name, ip_address, user_agent,
			       0, NULL, NULL
			  FROM moved
		)
		SELECT id, tenant_id, event_type, attempts, COALESCE(last_error, '')
		  FROM moved
		 ORDER BY dead_lettered_at, id`
	}
	rows, err := tx.Query(ctx, q, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReplayedEvent
	for rows.Next() {
		var e ReplayedEvent
		if err := rows.Scan(&e.ID, &e.TenantID, &e.EventType, &e.Attempts, &e.LastError); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
