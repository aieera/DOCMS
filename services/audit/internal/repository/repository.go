// Package repository persists audit events with hash-chain integrity.
package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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
// links, issue #74). Writes go through AppendChained (tenant tx + the
// chain's advisory lock, M-14) and the checkpoint inserts.
func (r *Repository) withTenant(ctx context.Context, tenantID string, fn func(tx pgx.Tx) error) error {
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		return fmt.Errorf("tenant_id: %w", err)
	}
	return database.WithTenantTx(ctx, r.pool, tid, fn)
}

// insertEvent writes one audit row inside tx. resource_id and actor_id are
// nullable uuid columns, so empty strings go in as NULL (events without a
// resource, e.g. login_success, failed with 22P02 before — BUG-20);
// actor_type is NOT NULL with no default and is derived: an actor implies
// 'user', otherwise 'system'. Only appendInTx calls it, under the chain
// lock.
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
		return appendInTx(ctx, tx, e, hash)
	})
}

// appendInTx is AppendChained's body for a caller that already holds a
// tenant transaction. Re-taking the advisory lock inside a transaction
// that already holds it is a no-op.
func appendInTx(ctx context.Context, tx pgx.Tx, e *model.AuditEvent, hash func(prev string, e *model.AuditEvent) string) error {
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

	if err := lockChain(ctx, tx, e.TenantID); err != nil {
		return err
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
}

// lockChain takes the per-tenant chain lock for the rest of tx (M-14).
func lockChain(ctx context.Context, tx pgx.Tx, tenantID string) error {
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('audit_chain:' || $1::text, 0))`,
		tenantID); err != nil {
		return fmt.Errorf("chain lock: %w", err)
	}
	return nil
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

// ErrChainTampered aborts an erasure whose rewrite range contains an event
// that no longer matches its stored hash or link: re-chaining over it and
// signing a fresh checkpoint would certify the tampering.
var ErrChainTampered = errors.New("audit chain does not verify in the range an erasure would rewrite")

// ChainHashes are the two hash functions an erasure needs: Hash computes a
// new (v2) hash; Expected re-derives the stored hash with the algorithm
// the event was written with (legacy or v2), for verification.
type ChainHashes struct {
	Hash     func(prev string, e *model.AuditEvent) string
	Expected func(prev string, e *model.AuditEvent) string
}

// AnonymizeAndRechain erases a data subject from the tenant's trail and
// re-links the chain from the first redacted event (GDPR Art.17, H-12), in
// one tenant transaction that holds the same advisory lock as
// AppendChained, so no append can interleave.
//
// dms_app cannot UPDATE audit_events (WORM), so both writes go through the
// SECURITY DEFINER functions of audit migration 000006:
// audit_redact_subject returns the ids it redacted, and every event from
// the earliest of those onward gets a hash re-computed over the values as
// stored, written back with audit_set_event_hash. Events before the first
// redaction keep their hashes.
//
// Before anything is redacted, every event from the subject's first event
// to the head — the subject's own events included — must still link to its
// stored predecessor and match its stored hash; otherwise the transaction
// aborts with ErrChainTampered (nothing redacted, no marker). In the same transaction, marker(n)
// is appended to the chain so the erasure is recorded as an authorised
// act, not mistaken for tampering. Returns how many events were redacted
// (0 leaves the chain untouched and appends no marker).
func (r *Repository) AnonymizeAndRechain(ctx context.Context, tenantID, subjectID string, h ChainHashes, marker func(redacted int64) *model.AuditEvent) (int64, error) {
	var n int64
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		if err := lockChain(ctx, tx, tenantID); err != nil {
			return err
		}
		loadChain := func() ([]*model.AuditEvent, error) {
			return scanEvents(tx.Query(ctx,
				selectEvents+` WHERE tenant_id = $1 ORDER BY created_at ASC, id ASC`, tenantID))
		}

		// 1. Verify BEFORE redacting: every event from the subject's first
		//    event to the head — the subject's own included — must still
		//    link to its stored predecessor and match its stored hash.
		//    Checking after the redaction would skip the subject's events,
		//    letting tampered content in fields the redaction keeps
		//    (action, resource, details) be re-hashed and signed.
		events, err := loadChain()
		if err != nil {
			return fmt.Errorf("verify load: %w", err)
		}
		first := -1
		for i, e := range events {
			if e.Actor == subjectID {
				first = i
				break
			}
		}
		if first < 0 {
			return nil // the subject has no events: nothing to erase
		}
		prev := ""
		if first > 0 {
			prev = events[first-1].EventHash
		}
		for _, e := range events[first:] {
			if e.PreviousHash != prev {
				return fmt.Errorf("%w: event %s does not link to its predecessor", ErrChainTampered, e.ID)
			}
			if h.Expected(e.PreviousHash, e) != e.EventHash {
				return fmt.Errorf("%w: event %s does not match its stored hash", ErrChainTampered, e.ID)
			}
			prev = e.EventHash
		}

		// 2. Redact through the definer.
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

		// 3. Re-chain from the first redacted event over the stored values.
		events, err = loadChain()
		if err != nil {
			return fmt.Errorf("re-chain load: %w", err)
		}
		newPrev, rechaining := "", false
		for _, e := range events {
			if !rechaining && redacted[e.ID] {
				rechaining = true
			}
			if !rechaining {
				newPrev = e.EventHash
				continue
			}
			next := h.Hash(newPrev, e)
			if next != e.EventHash || newPrev != e.PreviousHash {
				if _, err := tx.Exec(ctx, `SELECT audit_set_event_hash($1::uuid, $2::uuid, $3, $4)`,
					tenantID, e.ID, newPrev, next); err != nil {
					return fmt.Errorf("re-chain event %s: %w", e.ID, err)
				}
			}
			newPrev = next
		}

		// 4. Record the erasure on the chain.
		if err := appendInTx(ctx, tx, marker(n), h.Hash); err != nil {
			return fmt.Errorf("append erasure marker: %w", err)
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
