// Package repository persists audit events with hash-chain integrity.
package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/services/audit/internal/model"
)

// Repository manages the audit_events table.
type Repository struct{ pool *pgxpool.Pool }

// New constructs a Repository.
func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// Insert appends one event. Caller is responsible for computing event_hash.
//
// The schema's resource_id column is uuid + nullable, so events that
// don't carry a resource (login_success, mfa_reset) must pass NULL
// rather than the empty string the event envelope serializes as.
// Same for actor_id (uuid, nullable) once we start populating it.
// Without this guard every event from auth fails with "invalid input
// syntax for type uuid: \"\"" and the audit log stays empty — the
// exact symptom QA reported as BUG-20.
//
// actor_type is also NOT NULL with no default; the original event
// envelope doesn't carry it, so we derive: an actor_id present
// implies 'user', otherwise 'system'.
func (r *Repository) Insert(ctx context.Context, e *model.AuditEvent) error {
	var resourceID any
	if e.ResourceID != "" {
		resourceID = e.ResourceID
	}
	actorType := "system"
	if e.Actor != "" {
		actorType = "user"
	}
	// FIX-7 (audit C6) — audit_events has FORCE ROW LEVEL SECURITY
	// with an INSERT WITH CHECK keyed on app.current_tenant. The
	// previous direct pool.Exec did NOT set that GUC, so under the
	// dms_app role (NOBYPASSRLS in prod) every Insert silently
	// inserted ZERO rows — a SOC2-blocker for any consumer that
	// audited via this table. Wrapping in WithTenantTx applies the
	// GUC inside the same transaction.
	//
	// In dev the connection role is `vaultdms` (BYPASSRLS=t) so the
	// previous bug was invisible. The startup assertion in
	// pkg/database.AssertRLSPosture catches future role drift.
	tenantUUID, err := uuid.Parse(e.TenantID)
	if err != nil {
		return fmt.Errorf("audit insert: tenant_id not a uuid: %w", err)
	}
	return database.WithTenantTx(ctx, r.pool, tenantUUID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO audit_events (
				id, tenant_id, event_hash, previous_hash,
				actor, actor_id, actor_name, actor_type,
				action, resource_type, resource_id, resource_title,
				details, ip_address, user_agent, source_event, created_at
			) VALUES (
				$1, $2, $3, $4,
				$5, NULLIF($5, '')::uuid, $6, $7,
				$8, NULLIF($9, ''), $10, NULLIF($11, ''),
				$12, NULLIF($13, '')::inet, NULLIF($14, ''), NULLIF($15, ''), $16
			)
		`, e.ID, e.TenantID, e.EventHash, e.PreviousHash,
			e.Actor, e.ActorName, actorType,
			e.Action, e.ResourceType, resourceID, e.ResourceTitle,
			e.Details, e.IPAddress, e.UserAgent, e.SourceEvent, e.CreatedAt)
		return err
	})
}

// BatchInsert appends N events in a single transaction. All events
// MUST share the same tenant (the function asserts and errors out
// otherwise) because WithTenantTx sets a single app.current_tenant
// GUC for the tx and RLS would reject inserts that don't match.
//
// Hash chain: caller is responsible for pre-computing previous_hash /
// event_hash links — the repo just persists. Because IngestEvent
// holds a per-tenant Redis lock around the hash-chain computation,
// the caller can safely pre-compute a chained batch before calling
// here.
//
// Use this on the high-volume consumer path (NATS subscriber that
// pulls batches). Per-tx overhead drops by a factor of N: one
// connection acquire, one BEGIN, one SET LOCAL app.current_tenant,
// N INSERTs, one COMMIT — versus N of each in the per-row Insert.
func (r *Repository) BatchInsert(ctx context.Context, events []*model.AuditEvent) error {
	if len(events) == 0 {
		return nil
	}
	tenantStr := events[0].TenantID
	tenantUUID, err := uuid.Parse(tenantStr)
	if err != nil {
		return fmt.Errorf("audit batch insert: tenant_id[0] not a uuid: %w", err)
	}
	for i, e := range events {
		if e.TenantID != tenantStr {
			return fmt.Errorf("audit batch insert: mixed tenants (event[0]=%s, event[%d]=%s); split per-tenant before calling",
				tenantStr, i, e.TenantID)
		}
	}
	return database.WithTenantTx(ctx, r.pool, tenantUUID, func(tx pgx.Tx) error {
		batch := &pgx.Batch{}
		for _, e := range events {
			var resourceID any
			if e.ResourceID != "" {
				resourceID = e.ResourceID
			}
			actorType := "system"
			if e.Actor != "" {
				actorType = "user"
			}
			batch.Queue(`
				INSERT INTO audit_events (
					id, tenant_id, event_hash, previous_hash,
					actor, actor_id, actor_name, actor_type,
					action, resource_type, resource_id, resource_title,
					details, ip_address, user_agent, source_event, created_at
				) VALUES (
					$1, $2, $3, $4,
					$5, NULLIF($5, '')::uuid, $6, $7,
					$8, NULLIF($9, ''), $10, NULLIF($11, ''),
					$12, NULLIF($13, '')::inet, NULLIF($14, ''), NULLIF($15, ''), $16
				)`,
				e.ID, e.TenantID, e.EventHash, e.PreviousHash,
				e.Actor, e.ActorName, actorType,
				e.Action, e.ResourceType, resourceID, e.ResourceTitle,
				e.Details, e.IPAddress, e.UserAgent, e.SourceEvent, e.CreatedAt)
		}
		br := tx.SendBatch(ctx, batch)
		defer func() { _ = br.Close() }()
		for i := range events {
			if _, err := br.Exec(); err != nil {
				return fmt.Errorf("audit batch insert row %d: %w", i, err)
			}
		}
		return nil
	})
}

// GetLastHash returns the most recent event_hash for a tenant.
func (r *Repository) GetLastHash(ctx context.Context, tenantID string) (string, error) {
	var hash string
	err := r.pool.QueryRow(ctx,
		`SELECT event_hash FROM audit_events WHERE tenant_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1`,
		tenantID).Scan(&hash)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return hash, err
}

// List returns audit events matching the filter with cursor pagination.
func (r *Repository) List(ctx context.Context, f model.ListFilter) ([]*model.AuditEvent, string, error) {
	pageSize := f.PageSize
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 50
	}
	args := []any{f.TenantID}
	clauses := []string{"tenant_id = $1"}
	idx := 2

	if f.Actor != "" {
		clauses = append(clauses, fmt.Sprintf("actor = $%d", idx))
		args = append(args, f.Actor)
		idx++
	}
	if f.Action != "" {
		clauses = append(clauses, fmt.Sprintf("action = $%d", idx))
		args = append(args, f.Action)
		idx++
	}
	if f.ResourceType != "" {
		clauses = append(clauses, fmt.Sprintf("resource_type = $%d", idx))
		args = append(args, f.ResourceType)
		idx++
	}
	if f.ResourceID != "" {
		clauses = append(clauses, fmt.Sprintf("resource_id = $%d", idx))
		args = append(args, f.ResourceID)
		idx++
	}
	if f.DateFrom != nil {
		clauses = append(clauses, fmt.Sprintf("created_at >= $%d", idx))
		args = append(args, *f.DateFrom)
		idx++
	}
	if f.DateTo != nil {
		clauses = append(clauses, fmt.Sprintf("created_at <= $%d", idx))
		args = append(args, *f.DateTo)
		idx++
	}
	if f.PageToken != "" {
		cursorTime, cursorID := decodeCursor(f.PageToken)
		if cursorID != "" {
			clauses = append(clauses, fmt.Sprintf("(created_at, id) < ($%d, $%d)", idx, idx+1))
			args = append(args, cursorTime, cursorID)
			idx += 2
		}
	}
	where := strings.Join(clauses, " AND ")
	// Nullable columns (previous_hash, resource_type, resource_id, resource_title,
	// ip_address, user_agent, source_event) are coalesced to '' so we can scan
	// into plain `string` fields. ip_address is inet — host() drops the CIDR mask.
	query := fmt.Sprintf(`SELECT id, tenant_id, event_hash,
		COALESCE(previous_hash, ''), actor, actor_name, action,
		COALESCE(resource_type, ''), COALESCE(resource_id::text, ''),
		COALESCE(resource_title, ''), details,
		COALESCE(host(ip_address), ''), COALESCE(user_agent, ''),
		COALESCE(source_event, ''), created_at
		FROM audit_events WHERE %s ORDER BY created_at DESC, id DESC LIMIT %d`, where, pageSize+1)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	var events []*model.AuditEvent
	for rows.Next() {
		e := &model.AuditEvent{}
		if err := rows.Scan(&e.ID, &e.TenantID, &e.EventHash, &e.PreviousHash, &e.Actor, &e.ActorName,
			&e.Action, &e.ResourceType, &e.ResourceID, &e.ResourceTitle, &e.Details, &e.IPAddress,
			&e.UserAgent, &e.SourceEvent, &e.CreatedAt); err != nil {
			return nil, "", err
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	var nextToken string
	if len(events) > pageSize {
		events = events[:pageSize]
		last := events[pageSize-1]
		nextToken = encodeCursor(last.CreatedAt, last.ID)
	}
	return events, nextToken, nil
}

// ListAll streams all events for a tenant in order (for integrity verification).
func (r *Repository) ListAll(ctx context.Context, tenantID string) ([]*model.AuditEvent, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, tenant_id, event_hash,
			COALESCE(previous_hash, ''), actor, actor_name, action,
			COALESCE(resource_type, ''), COALESCE(resource_id::text, ''),
			COALESCE(resource_title, ''), details,
			COALESCE(host(ip_address), ''), COALESCE(user_agent, ''),
			COALESCE(source_event, ''), created_at
		FROM audit_events WHERE tenant_id = $1 ORDER BY created_at ASC, id ASC`,
		tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []*model.AuditEvent
	for rows.Next() {
		e := &model.AuditEvent{}
		if err := rows.Scan(&e.ID, &e.TenantID, &e.EventHash, &e.PreviousHash, &e.Actor, &e.ActorName,
			&e.Action, &e.ResourceType, &e.ResourceID, &e.ResourceTitle, &e.Details, &e.IPAddress,
			&e.UserAgent, &e.SourceEvent, &e.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// ListBySubject returns all events where actor matches (for GDPR export).
func (r *Repository) ListBySubject(ctx context.Context, tenantID, subjectID string) ([]*model.AuditEvent, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, tenant_id, event_hash,
			COALESCE(previous_hash, ''), actor, actor_name, action,
			COALESCE(resource_type, ''), COALESCE(resource_id::text, ''),
			COALESCE(resource_title, ''), details,
			COALESCE(host(ip_address), ''), COALESCE(user_agent, ''),
			COALESCE(source_event, ''), created_at
		FROM audit_events WHERE tenant_id = $1 AND actor = $2 ORDER BY created_at ASC`,
		tenantID, subjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []*model.AuditEvent
	for rows.Next() {
		e := &model.AuditEvent{}
		if err := rows.Scan(&e.ID, &e.TenantID, &e.EventHash, &e.PreviousHash, &e.Actor, &e.ActorName,
			&e.Action, &e.ResourceType, &e.ResourceID, &e.ResourceTitle, &e.Details, &e.IPAddress,
			&e.UserAgent, &e.SourceEvent, &e.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// AnonymizeSubject replaces actor/actor_name with "REDACTED" for GDPR Art.17.
func (r *Repository) AnonymizeSubject(ctx context.Context, tenantID, subjectID string) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE audit_events SET actor = 'REDACTED', actor_name = 'REDACTED', ip_address = '', user_agent = ''
		 WHERE tenant_id = $1 AND actor = $2`, tenantID, subjectID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func encodeCursor(t time.Time, id string) string {
	b, _ := json.Marshal([]string{t.Format(time.RFC3339Nano), id})
	return base64.StdEncoding.EncodeToString(b)
}

func decodeCursor(token string) (time.Time, string) {
	b, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return time.Time{}, ""
	}
	var parts []string
	if err := json.Unmarshal(b, &parts); err != nil || len(parts) != 2 {
		return time.Time{}, ""
	}
	t, _ := time.Parse(time.RFC3339Nano, parts[0])
	return t, parts[1]
}
