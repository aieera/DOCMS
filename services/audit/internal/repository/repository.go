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

// withTenant opens a tenant-scoped transaction (SET LOCAL
// app.current_tenant) and runs fn inside it — the A.1.a template. The
// read/export/redact methods below use it so the FORCE-RLS audit_events
// table returns rows under the dms_app (NOBYPASSRLS) role; the raw-pool
// versions fail closed in prod (empty reads → the hash chain never
// links, issue #74). Writes (Insert/BatchInsert/checkpoints) were
// already wrapped by FIX-7.
func (r *Repository) withTenant(ctx context.Context, tenantID string, fn func(tx pgx.Tx) error) error {
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		return fmt.Errorf("tenant_id: %w", err)
	}
	return database.WithTenantTx(ctx, r.pool, tid, fn)
}

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
		return insertEvent(ctx, tx, e)
	})
}

// insertEvent writes one audit row inside tx (see Insert for the NULL
// handling of resource_id / actor_id and the derived actor_type).
func insertEvent(ctx context.Context, tx pgx.Tx, e *model.AuditEvent) error {
	var resourceID any
	if e.ResourceID != "" {
		resourceID = e.ResourceID
	}
	actorType := "system"
	if e.Actor != "" {
		actorType = "user"
	}
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

// AppendChained links e into its tenant's hash chain and inserts it, all in
// one tenant transaction (M-14):
//
//  1. pg_advisory_xact_lock on a per-tenant key serialises every appender
//     across every audit replica — the lock lives in Postgres, so two pods
//     can no longer both read the same head and fork the chain. It is
//     released by COMMIT/ROLLBACK, so a crashed appender cannot wedge it.
//  2. The head (hash + created_at) is read inside the same transaction,
//     after the lock, so it is the true predecessor.
//  3. created_at is clamped to strictly after the head's, so the
//     (created_at, id) order VerifyIntegrity walks always equals append
//     order, whatever the clock skew between replicas or the delay between
//     stamping an event and winning the lock.
//  4. details, ip_address and resource_id are replaced by the exact text
//     Postgres will hand back on read (jsonb reorders keys and adds
//     spaces; inet and uuid lower-case and compress). The hash must cover
//     what VerifyIntegrity re-reads, or every event with a non-trivial
//     payload fails verification — which is what happened while the hash
//     covered the raw envelope bytes.
//  5. hash(prev, e) computes the event hash over those final values.
func (r *Repository) AppendChained(ctx context.Context, e *model.AuditEvent, hash func(prev string, e *model.AuditEvent) string) error {
	return r.withTenant(ctx, e.TenantID, func(tx pgx.Tx) error {
		if len(e.Details) == 0 {
			e.Details = []byte("{}")
		}
		var details, ip, resourceID string
		if err := tx.QueryRow(ctx, `
			SELECT $1::jsonb::text,
			       COALESCE(host(NULLIF($2, '')::inet), ''),
			       COALESCE(NULLIF($3, '')::uuid::text, '')`,
			string(e.Details), e.IPAddress, e.ResourceID).Scan(&details, &ip, &resourceID); err != nil {
			return fmt.Errorf("canonicalise event fields: %w", err)
		}
		e.Details, e.IPAddress, e.ResourceID = []byte(details), ip, resourceID

		if _, err := tx.Exec(ctx,
			`SELECT pg_advisory_xact_lock(hashtextextended('audit_chain:' || $1::text, 0))`,
			e.TenantID); err != nil {
			return fmt.Errorf("chain lock: %w", err)
		}
		var (
			prev   string
			headAt time.Time
		)
		err := tx.QueryRow(ctx,
			`SELECT event_hash, created_at FROM audit_events WHERE tenant_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1`,
			e.TenantID).Scan(&prev, &headAt)
		switch {
		case err == pgx.ErrNoRows:
			prev = ""
		case err != nil:
			return fmt.Errorf("chain head: %w", err)
		default:
			if !e.CreatedAt.After(headAt) {
				e.CreatedAt = headAt.Add(time.Microsecond)
			}
		}
		e.PreviousHash = prev
		e.EventHash = hash(prev, e)
		return insertEvent(ctx, tx, e)
	})
}

// GetLastHash returns the most recent event_hash for a tenant.
func (r *Repository) GetLastHash(ctx context.Context, tenantID string) (string, error) {
	var hash string
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		serr := tx.QueryRow(ctx,
			`SELECT event_hash FROM audit_events WHERE tenant_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1`,
			tenantID).Scan(&hash)
		if serr == pgx.ErrNoRows {
			return nil // genuinely empty chain — ("", nil)
		}
		return serr
	})
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

	var events []*model.AuditEvent
	if err := r.withTenant(ctx, f.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e := &model.AuditEvent{}
			if err := rows.Scan(&e.ID, &e.TenantID, &e.EventHash, &e.PreviousHash, &e.Actor, &e.ActorName,
				&e.Action, &e.ResourceType, &e.ResourceID, &e.ResourceTitle, &e.Details, &e.IPAddress,
				&e.UserAgent, &e.SourceEvent, &e.CreatedAt); err != nil {
				return err
			}
			events = append(events, e)
		}
		return rows.Err()
	}); err != nil {
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
	var events []*model.AuditEvent
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, tenant_id, event_hash,
				COALESCE(previous_hash, ''), actor, actor_name, action,
				COALESCE(resource_type, ''), COALESCE(resource_id::text, ''),
				COALESCE(resource_title, ''), details,
				COALESCE(host(ip_address), ''), COALESCE(user_agent, ''),
				COALESCE(source_event, ''), created_at
			FROM audit_events WHERE tenant_id = $1 ORDER BY created_at ASC, id ASC`,
			tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e := &model.AuditEvent{}
			if err := rows.Scan(&e.ID, &e.TenantID, &e.EventHash, &e.PreviousHash, &e.Actor, &e.ActorName,
				&e.Action, &e.ResourceType, &e.ResourceID, &e.ResourceTitle, &e.Details, &e.IPAddress,
				&e.UserAgent, &e.SourceEvent, &e.CreatedAt); err != nil {
				return err
			}
			events = append(events, e)
		}
		return rows.Err()
	})
	return events, err
}

// ListBySubject returns all events where actor matches (for GDPR export).
func (r *Repository) ListBySubject(ctx context.Context, tenantID, subjectID string) ([]*model.AuditEvent, error) {
	var events []*model.AuditEvent
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, tenant_id, event_hash,
				COALESCE(previous_hash, ''), actor, actor_name, action,
				COALESCE(resource_type, ''), COALESCE(resource_id::text, ''),
				COALESCE(resource_title, ''), details,
				COALESCE(host(ip_address), ''), COALESCE(user_agent, ''),
				COALESCE(source_event, ''), created_at
			FROM audit_events WHERE tenant_id = $1 AND actor = $2 ORDER BY created_at ASC`,
			tenantID, subjectID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e := &model.AuditEvent{}
			if err := rows.Scan(&e.ID, &e.TenantID, &e.EventHash, &e.PreviousHash, &e.Actor, &e.ActorName,
				&e.Action, &e.ResourceType, &e.ResourceID, &e.ResourceTitle, &e.Details, &e.IPAddress,
				&e.UserAgent, &e.SourceEvent, &e.CreatedAt); err != nil {
				return err
			}
			events = append(events, e)
		}
		return rows.Err()
	})
	return events, err
}

// AnonymizeAndRechain erases a data subject from the tenant's trail and
// re-links the chain from the first redacted event (GDPR Art.17, H-12), in
// one tenant transaction that holds the same advisory lock as
// AppendChained, so no append can interleave.
//
// dms_app cannot UPDATE audit_events (WORM), so both writes go through the
// SECURITY DEFINER functions of audit migration 000006:
// audit_redact_subject returns the ids it redacted, and every event from
// the earliest of those onward gets a hash re-computed by hash(prev, e)
// over the values as stored, written back with audit_set_event_hash.
// Events before the first redaction keep their hashes. Returns how many
// events were redacted (0 leaves the chain untouched).
func (r *Repository) AnonymizeAndRechain(ctx context.Context, tenantID, subjectID string, hash func(prev string, e *model.AuditEvent) string) (int64, error) {
	var n int64
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`SELECT pg_advisory_xact_lock(hashtextextended('audit_chain:' || $1::text, 0))`,
			tenantID); err != nil {
			return fmt.Errorf("chain lock: %w", err)
		}
		rows, err := tx.Query(ctx, `SELECT r::text FROM audit_redact_subject($1::uuid, $2) AS r`, tenantID, subjectID)
		if err != nil {
			return fmt.Errorf("redact: %w", err)
		}
		redacted := map[string]bool{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			redacted[id] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("redact: %w", err)
		}
		n = int64(len(redacted))
		if n == 0 {
			return nil
		}

		events, err := scanEvents(tx.Query(ctx,
			selectEvents+` WHERE tenant_id = $1 ORDER BY created_at ASC, id ASC`, tenantID))
		if err != nil {
			return fmt.Errorf("re-chain load: %w", err)
		}
		prev, rechaining := "", false
		for _, e := range events {
			if !rechaining && redacted[e.ID] {
				rechaining = true
			}
			if !rechaining {
				prev = e.EventHash
				continue
			}
			h := hash(prev, e)
			if h != e.EventHash || prev != e.PreviousHash {
				if _, err := tx.Exec(ctx, `SELECT audit_set_event_hash($1::uuid, $2::uuid, $3, $4)`,
					tenantID, e.ID, prev, h); err != nil {
					return fmt.Errorf("re-chain event %s: %w", e.ID, err)
				}
			}
			prev = h
		}
		return nil
	})
	return n, err
}

// selectEvents is the column list every chain read scans (see scanEvents).
// Nullable columns are coalesced to ” and ip_address goes through host(),
// exactly the form the chain hash is computed over.
const selectEvents = `SELECT id, tenant_id, event_hash,
	COALESCE(previous_hash, ''), actor, actor_name, action,
	COALESCE(resource_type, ''), COALESCE(resource_id::text, ''),
	COALESCE(resource_title, ''), details,
	COALESCE(host(ip_address), ''), COALESCE(user_agent, ''),
	COALESCE(source_event, ''), created_at
	FROM audit_events`

func scanEvents(rows pgx.Rows, err error) ([]*model.AuditEvent, error) {
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

// HeadAndCount returns the tenant's current event count and the
// event_hash of the most recent event, read in one snapshot so the pair
// is consistent. count == 0 means there are no events to checkpoint.
// Runs inside WithTenantTx so the FORCE-RLS audit_events read sees rows
// under the dms_app (NOBYPASSRLS) role.
func (r *Repository) HeadAndCount(ctx context.Context, tenantID string) (int64, string, error) {
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return 0, "", fmt.Errorf("head+count: tenant_id not a uuid: %w", err)
	}
	var (
		count int64
		head  string
	)
	err = database.WithTenantTx(ctx, r.pool, tenantUUID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT count(*),
			       COALESCE((SELECT event_hash FROM audit_events
			                  WHERE tenant_id = $1
			               ORDER BY created_at DESC, id DESC LIMIT 1), '')
			FROM audit_events WHERE tenant_id = $1
		`, tenantID).Scan(&count, &head)
	})
	return count, head, err
}

// InsertCheckpoint appends a signed checkpoint (append-only, tenant-RLS).
func (r *Repository) InsertCheckpoint(ctx context.Context, cp *model.AuditCheckpoint) error {
	tenantUUID, err := uuid.Parse(cp.TenantID)
	if err != nil {
		return fmt.Errorf("insert checkpoint: tenant_id not a uuid: %w", err)
	}
	return database.WithTenantTx(ctx, r.pool, tenantUUID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO audit_checkpoints (tenant_id, head_hash, event_count, algo, key_id, signature)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id, created_at
		`, cp.TenantID, cp.HeadHash, cp.EventCount, cp.Algo, cp.KeyID, cp.Signature).
			Scan(&cp.ID, &cp.CreatedAt)
	})
}

// LatestCheckpoint returns the most recent checkpoint for the tenant, or
// (nil, nil) when none exists.
func (r *Repository) LatestCheckpoint(ctx context.Context, tenantID string) (*model.AuditCheckpoint, error) {
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return nil, fmt.Errorf("latest checkpoint: tenant_id not a uuid: %w", err)
	}
	var cp *model.AuditCheckpoint
	err = database.WithTenantTx(ctx, r.pool, tenantUUID, func(tx pgx.Tx) error {
		c := &model.AuditCheckpoint{}
		serr := tx.QueryRow(ctx, `
			SELECT tenant_id, id, created_at, head_hash, event_count, algo, key_id, signature
			FROM audit_checkpoints WHERE tenant_id = $1
			ORDER BY created_at DESC, id DESC LIMIT 1
		`, tenantID).Scan(&c.TenantID, &c.ID, &c.CreatedAt, &c.HeadHash, &c.EventCount, &c.Algo, &c.KeyID, &c.Signature)
		if serr == pgx.ErrNoRows {
			return nil
		}
		if serr != nil {
			return serr
		}
		cp = c
		return nil
	})
	return cp, err
}

// ListCheckpoints returns the tenant's checkpoints, newest first.
func (r *Repository) ListCheckpoints(ctx context.Context, tenantID string, limit int) ([]*model.AuditCheckpoint, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return nil, fmt.Errorf("list checkpoints: tenant_id not a uuid: %w", err)
	}
	var out []*model.AuditCheckpoint
	err = database.WithTenantTx(ctx, r.pool, tenantUUID, func(tx pgx.Tx) error {
		rows, qerr := tx.Query(ctx, `
			SELECT tenant_id, id, created_at, head_hash, event_count, algo, key_id, signature
			FROM audit_checkpoints WHERE tenant_id = $1
			ORDER BY created_at DESC, id DESC LIMIT $2
		`, tenantID, limit)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()
		for rows.Next() {
			c := &model.AuditCheckpoint{}
			if serr := rows.Scan(&c.TenantID, &c.ID, &c.CreatedAt, &c.HeadHash, &c.EventCount, &c.Algo, &c.KeyID, &c.Signature); serr != nil {
				return serr
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
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
