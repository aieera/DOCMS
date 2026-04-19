package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/document/internal/model"
)

// ---- Outbox ---------------------------------------------------------------

type outboxRepo struct{}

func (r *outboxRepo) Insert(ctx context.Context, tx pgx.Tx, e *model.OutboxEvent) error {
	// Schema no longer carries a `subject` column — the publisher derives
	// NATS subject from `event_type`. e.Subject is still present on the
	// in-memory event for logging convenience but is not persisted.
	_, err := tx.Exec(ctx, `
		INSERT INTO outbox (
			id, tenant_id, event_type, aggregate_type, aggregate_id,
			payload, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, e.ID, e.TenantID, e.EventType, e.AggregateType, e.AggregateID,
		e.Payload, e.CreatedAt)
	return mapPgError(err)
}

// ---- Legal hold -----------------------------------------------------------

type legalHoldRepo struct{}

func (r *legalHoldRepo) Place(ctx context.Context, tx pgx.Tx, h *model.LegalHoldRecord) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO legal_hold_documents (
			id, tenant_id, document_id, hold_name, matter_reference, reason,
			previous_lifecycle_state, placed_by, placed_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, h.ID, h.TenantID, h.DocumentID, h.HoldName, h.MatterReference, h.Reason,
		string(h.PreviousLifecycleState), h.PlacedBy, h.PlacedAt)
	return mapPgError(err)
}

func (r *legalHoldRepo) Release(ctx context.Context, tx pgx.Tx, tenantID, documentID, releasedBy uuid.UUID) (*model.LegalHoldRecord, error) {
	row := tx.QueryRow(ctx, `
		UPDATE legal_hold_documents
		SET released_at = now(), released_by = $3
		WHERE tenant_id = $1 AND document_id = $2 AND released_at IS NULL
		RETURNING id, tenant_id, document_id, hold_name,
		          COALESCE(matter_reference, ''), COALESCE(reason, ''),
		          previous_lifecycle_state, placed_by, placed_at, released_at, released_by
	`, tenantID, documentID, releasedBy)
	return scanHold(row)
}

func (r *legalHoldRepo) GetActiveByDocument(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID) (*model.LegalHoldRecord, error) {
	row := tx.QueryRow(ctx, `
		SELECT id, tenant_id, document_id, hold_name,
		       COALESCE(matter_reference, ''), COALESCE(reason, ''),
		       previous_lifecycle_state, placed_by, placed_at, released_at, released_by
		FROM legal_hold_documents
		WHERE tenant_id = $1 AND document_id = $2 AND released_at IS NULL
		ORDER BY placed_at DESC LIMIT 1
	`, tenantID, documentID)
	return scanHold(row)
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
