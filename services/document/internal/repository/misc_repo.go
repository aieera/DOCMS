package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/pkg/auth"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/model"
)

// ---- Outbox ---------------------------------------------------------------

type outboxRepo struct{}

// Insert writes an outbox row inside the caller's tx, stamping the
// authenticated caller's user_id / email and the request's client IP
// (read from ctx) onto the row. Those three fields propagate through
// the outbox publisher's CloudEvents envelope into audit_events so
// audit log rows have actor + IP without every emitter needing to
// thread them through its payload manually. Background-job callers
// without a request ctx end up with NULL — correct behaviour, since
// there is no human actor.
func (r *outboxRepo) Insert(ctx context.Context, tx pgx.Tx, e *model.OutboxEvent) error {
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
	// Schema no longer carries a `subject` column — the publisher derives
	// NATS subject from `event_type`. e.Subject is still present on the
	// in-memory event for logging convenience but is not persisted.
	_, err := tx.Exec(ctx, `
		INSERT INTO outbox (
			id, tenant_id, event_type, aggregate_type, aggregate_id,
			payload, created_at, actor_id, actor_name, ip_address
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, e.ID, e.TenantID, e.EventType, e.AggregateType, e.AggregateID,
		e.Payload, e.CreatedAt, actorID, actorName, ipAddr)
	return mapPgError(err)
}

// ---- Legal hold -----------------------------------------------------------
//
// The in-band lifecycle hold (UpdateLifecycle apply_hold / release_hold)
// lives in the same tables the compliance API writes: a legal_holds row
// tagged origin='lifecycle' plus its legal_hold_documents binding, whose
// previous_lifecycle_state is the state release_hold restores. Enforcement
// (DocumentHoldState) sees both origins alike.

type legalHoldRepo struct{}

// Place records an in-band legal hold on one document.
func (r *legalHoldRepo) Place(ctx context.Context, tx pgx.Tx, h *model.LegalHoldRecord) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO legal_holds (
			tenant_id, id, name, description, matter_reference,
			applied_by, applied_at, is_active, origin
		) VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), $6, $7, true, 'lifecycle')
	`, h.TenantID, h.ID, h.HoldName, h.Reason, h.MatterReference, h.PlacedBy, h.PlacedAt); err != nil {
		return mapPgError(err)
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO legal_hold_documents (
			tenant_id, hold_id, document_id, applied_at, previous_lifecycle_state
		) VALUES ($1, $2, $3, $4, $5)
	`, h.TenantID, h.ID, h.DocumentID, h.PlacedAt, string(h.PreviousLifecycleState))
	return mapPgError(err)
}

// holdRecordColumns projects a legal_holds ⋈ legal_hold_documents row onto
// scanHold's column order.
const holdRecordColumns = `
	lh.id, lh.tenant_id, lhd.document_id, lh.name,
	COALESCE(lh.matter_reference, ''), COALESCE(lh.description, ''),
	lhd.previous_lifecycle_state,
	COALESCE(lh.applied_by, '00000000-0000-0000-0000-000000000000'::uuid),
	lh.applied_at, lh.released_at, lh.released_by`

// Release ends the document's lifecycle-origin hold and returns the
// binding (its previous_lifecycle_state is what release_hold restores).
// Only THIS document leaves the hold: when the hold row still binds other
// documents (the compliance API refuses to attach documents to a
// lifecycle hold, but a hold row must never be released for documents the
// caller did not name), just this document's binding is removed and the
// hold stays in force for the rest; otherwise the hold row is deactivated.
// An already-inactive lifecycle hold is still returned so the document can
// leave the legal_hold state. Compliance-API holds are never touched.
// ErrNotFound when the document has no lifecycle hold.
func (r *legalHoldRepo) Release(ctx context.Context, tx pgx.Tx, tenantID, documentID, releasedBy uuid.UUID) (*model.LegalHoldRecord, error) {
	var (
		holdID   uuid.UUID
		active   bool
		bindings int
	)
	if err := tx.QueryRow(ctx, `
		SELECT lh.id, lh.is_active,
		       (SELECT count(*) FROM legal_hold_documents b
		         WHERE b.tenant_id = lh.tenant_id AND b.hold_id = lh.id)
		  FROM legal_holds lh
		  JOIN legal_hold_documents lhd
		    ON lhd.tenant_id = lh.tenant_id AND lhd.hold_id = lh.id
		 WHERE lh.tenant_id = $1 AND lhd.document_id = $2 AND lh.origin = 'lifecycle'
		 ORDER BY lh.is_active DESC, lh.applied_at DESC
		 LIMIT 1
		   FOR UPDATE OF lh
	`, tenantID, documentID).Scan(&holdID, &active, &bindings); err != nil {
		return nil, mapPgError(err)
	}
	if active && bindings > 1 {
		row := tx.QueryRow(ctx, `
			DELETE FROM legal_hold_documents lhd
			 USING legal_holds lh
			 WHERE lhd.tenant_id = $1 AND lhd.hold_id = $2 AND lhd.document_id = $3
			   AND lh.tenant_id = lhd.tenant_id AND lh.id = lhd.hold_id
			RETURNING`+holdRecordColumns, tenantID, holdID, documentID)
		return scanHold(row)
	}
	row := tx.QueryRow(ctx, `
		UPDATE legal_holds lh
		   SET is_active   = false,
		       released_by = COALESCE(lh.released_by, $3),
		       released_at = COALESCE(lh.released_at, now())
		  FROM legal_hold_documents lhd
		 WHERE lh.tenant_id = $1 AND lh.id = $2
		   AND lhd.tenant_id = lh.tenant_id AND lhd.hold_id = lh.id AND lhd.document_id = $4
		RETURNING`+holdRecordColumns, tenantID, holdID, releasedBy, documentID)
	return scanHold(row)
}

// GetActiveByDocument returns the document's active lifecycle-origin hold.
func (r *legalHoldRepo) GetActiveByDocument(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID) (*model.LegalHoldRecord, error) {
	row := tx.QueryRow(ctx, `
		SELECT`+holdRecordColumns+`
		  FROM legal_holds lh
		  JOIN legal_hold_documents lhd
		    ON lhd.tenant_id = lh.tenant_id AND lhd.hold_id = lh.id
		 WHERE lh.tenant_id = $1 AND lhd.document_id = $2
		   AND lh.origin = 'lifecycle' AND lh.is_active
		 ORDER BY lh.applied_at DESC LIMIT 1
	`, tenantID, documentID)
	return scanHold(row)
}

// DocumentHoldState reads, inside the caller's tx, the document's
// lifecycle_state and whether any ACTIVE legal hold — lifecycle or
// compliance-API origin — binds it. ErrNotFound when the document is
// absent. Package-level (not on LegalHoldRepository) because the interface
// lives in repository.go.
func DocumentHoldState(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID) (model.LifecycleState, bool, error) {
	var (
		state string
		bound bool
	)
	err := tx.QueryRow(ctx, `
		SELECT d.lifecycle_state,
		       EXISTS (
		           SELECT 1
		             FROM legal_hold_documents lhd
		             JOIN legal_holds lh ON lh.tenant_id = lhd.tenant_id AND lh.id = lhd.hold_id
		            WHERE lhd.tenant_id = d.tenant_id AND lhd.document_id = d.id
		              AND lh.is_active
		       )
		  FROM documents d
		 WHERE d.tenant_id = $1 AND d.id = $2
	`, tenantID, documentID).Scan(&state, &bound)
	if err != nil {
		return "", false, mapPgError(err)
	}
	return model.LifecycleState(state), bound, nil
}

func scanHold(r rowScanner) (*model.LegalHoldRecord, error) {
	var (
		h          model.LegalHoldRecord
		prevState  string
		releasedAt *time.Time
		releasedBy *uuid.UUID
	)
	if err := r.Scan(
		&h.ID, &h.TenantID, &h.DocumentID, &h.HoldName, &h.MatterReference, &h.Reason,
		&prevState, &h.PlacedBy, &h.PlacedAt, &releasedAt, &releasedBy,
	); err != nil {
		return nil, mapPgError(err)
	}
	h.PreviousLifecycleState = model.LifecycleState(prevState)
	h.ReleasedAt = releasedAt
	h.ReleasedBy = releasedBy
	return &h, nil
}

// ---- Tenant metadata schema -----------------------------------------------

type metadataSchemaRepo struct{}

func (r *metadataSchemaRepo) Get(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]byte, error) {
	var b []byte
	err := tx.QueryRow(ctx, `
		SELECT json_schema FROM tenant_metadata_schemas WHERE tenant_id = $1
	`, tenantID).Scan(&b)
	if err != nil {
		if vdmserr.KindOf(mapPgError(err)) == vdmserr.KindNotFound {
			return []byte("{}"), nil
		}
		return nil, mapPgError(err)
	}
	return b, nil
}

func (r *metadataSchemaRepo) Upsert(ctx context.Context, tx pgx.Tx, tenantID, updatedBy uuid.UUID, jsonSchema []byte) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO tenant_metadata_schemas (tenant_id, json_schema, updated_by, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (tenant_id) DO UPDATE
		SET json_schema = EXCLUDED.json_schema,
		    updated_by  = EXCLUDED.updated_by,
		    updated_at  = EXCLUDED.updated_at
	`, tenantID, jsonSchema, updatedBy)
	return mapPgError(err)
}
