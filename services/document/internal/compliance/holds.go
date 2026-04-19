// Package compliance — legal hold API surface (Wave 8 Prompt 8.2).
//
// Tables used:
//
//	legal_holds
//	    id, tenant_id, name, description, matter_reference,
//	    applied_by, applied_at, released_by, released_at, is_active
//
//	legal_hold_documents
//	    tenant_id, hold_id, document_id, applied_at,
//	    previous_lifecycle_state
//
// Events emitted (via outbox, same txn as the write):
//
//	dms.hold.applied.v1     on POST   /compliance/holds
//	dms.hold.released.v1    on POST   /compliance/holds/{id}/release
//	dms.hold.updated.v1     on PATCH  /compliance/holds/{id}
//
// Immutability: once a hold is created, hold.id, tenant_id, and
// matter_reference are never mutable. PATCH only touches name,
// description, and the attached-documents set.
//
// Enforcement: DocumentService.DeleteDocument pre-checks
// AnyActiveHoldFor(documentID) and returns ErrLegalHold (→ 423
// Locked) when the binding table has an active row.
//
// NOTE: this file is the real Wave 8.2 implementation. The small
// helpers in retention.go (CreateLegalHold / ReleaseLegalHold) were
// written against a schema that never existed (`legal_hold_items` +
// `reason` + `status` columns). They remain unreferenced by the
// server boot and are scheduled for removal in the Wave 11 workflow
// consolidation.
package compliance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vaultdms/vaultdms/pkg/database"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/document/internal/model"
)

// Hold is the wire model for a legal hold. Matter reference is
// returned as-is; operators may use it to link to their e-discovery
// platform's matter ID.
type Hold struct {
	ID              uuid.UUID  `json:"id"`
	TenantID        uuid.UUID  `json:"tenant_id"`
	Name            string     `json:"name"`
	Description     string     `json:"description,omitempty"`
	MatterReference string     `json:"matter_reference,omitempty"`
	AppliedBy       *uuid.UUID `json:"applied_by,omitempty"`
	AppliedAt       time.Time  `json:"applied_at"`
	ReleasedBy      *uuid.UUID `json:"released_by,omitempty"`
	ReleasedAt      *time.Time `json:"released_at,omitempty"`
	IsActive        bool       `json:"is_active"`
	DocumentIDs     []string   `json:"document_ids,omitempty"`
}

// CreateInput is the POST /compliance/holds body.
type CreateInput struct {
	Name            string      `json:"name"`
	Description     string      `json:"description"`
	MatterReference string      `json:"matter_reference"`
	DocumentIDs     []uuid.UUID `json:"document_ids"`
}

// UpdateInput is the PATCH /compliance/holds/{id} body. Only name,
// description, and the attached-documents set may change.
type UpdateInput struct {
	Name        *string      `json:"name,omitempty"`
	Description *string      `json:"description,omitempty"`
	AddDocs     []uuid.UUID  `json:"add_document_ids,omitempty"`
	RemoveDocs  []uuid.UUID  `json:"remove_document_ids,omitempty"`
}

// ReleaseInput is the POST /compliance/holds/{id}/release body.
type ReleaseInput struct {
	Reason   string    `json:"reason"`
	Approver uuid.UUID `json:"approver_id"`
}

// ListFilter scopes GET /compliance/holds.
type ListFilter struct {
	Status     string    // "", "active", "released"
	DocumentID uuid.UUID // zero = all
	Custodian  uuid.UUID // applied_by filter; zero = all
}

// HoldsService is the transaction-scoped business layer.
type HoldsService struct {
	pool *pgxpool.Pool
}

// NewHoldsService constructs a HoldsService bound to the given pool.
func NewHoldsService(pool *pgxpool.Pool) *HoldsService {
	return &HoldsService{pool: pool}
}

// Create applies a new hold to the listed documents. All inserts are
// done in a single tenant-scoped transaction with the outbox event,
// so a partial apply never leaks to the event bus.
func (s *HoldsService) Create(ctx context.Context, tenantID, appliedBy uuid.UUID, in CreateInput) (*Hold, error) {
	if in.Name == "" {
		return nil, vdmserr.Validation("name", "required")
	}
	if len(in.DocumentIDs) == 0 {
		return nil, vdmserr.Validation("document_ids", "at least one document required")
	}

	h := &Hold{
		Name:            in.Name,
		Description:     in.Description,
		MatterReference: in.MatterReference,
		AppliedBy:       &appliedBy,
		IsActive:        true,
		TenantID:        tenantID,
		DocumentIDs:     make([]string, 0, len(in.DocumentIDs)),
	}

	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			INSERT INTO legal_holds (tenant_id, name, description, matter_reference, applied_by, is_active)
			VALUES ($1,$2,$3,NULLIF($4,''),$5,true)
			RETURNING id, applied_at`,
			tenantID, in.Name, in.Description, in.MatterReference, appliedBy,
		).Scan(&h.ID, &h.AppliedAt)
		if err != nil {
			return vdmserr.FromPgError(err)
		}

		for _, docID := range in.DocumentIDs {
			var lifecycle string
			err := tx.QueryRow(ctx, `
				SELECT lifecycle_state FROM documents
				 WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`,
				tenantID, docID,
			).Scan(&lifecycle)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return vdmserr.NotFound(fmt.Sprintf("document %s not found", docID))
				}
				return vdmserr.FromPgError(err)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO legal_hold_documents (tenant_id, hold_id, document_id, previous_lifecycle_state)
				VALUES ($1,$2,$3,$4)
				ON CONFLICT DO NOTHING`,
				tenantID, h.ID, docID, lifecycle,
			); err != nil {
				return vdmserr.FromPgError(err)
			}
			h.DocumentIDs = append(h.DocumentIDs, docID.String())
		}

		evt, err := model.NewOutboxEvent(tenantID, "dms.hold.applied.v1", "legal_hold", h.ID, map[string]any{
			"hold_id":          h.ID.String(),
			"name":             h.Name,
			"matter_reference": h.MatterReference,
			"applied_by":       appliedBy.String(),
			"document_ids":     h.DocumentIDs,
		})
		if err != nil {
			return err
		}
		return insertOutbox(ctx, tx, evt)
	})
	if err != nil {
		return nil, err
	}
	return h, nil
}

// Release marks a hold inactive and stamps released_by / released_at.
// The binding rows stay in legal_hold_documents for audit — only the
// active flag controls enforcement.
func (s *HoldsService) Release(ctx context.Context, tenantID, holdID, releasedBy uuid.UUID, in ReleaseInput) (*Hold, error) {
	if in.Reason == "" {
		return nil, vdmserr.Validation("reason", "required")
	}
	if in.Approver == uuid.Nil {
		return nil, vdmserr.Validation("approver_id", "required")
	}

	var out Hold
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			UPDATE legal_holds
			   SET is_active = false,
			       released_by = $1,
			       released_at = $2
			 WHERE tenant_id = $3 AND id = $4 AND is_active = true
			 RETURNING id, tenant_id, name, COALESCE(description, ''), COALESCE(matter_reference, ''),
			           applied_by, applied_at, released_by, released_at, is_active`,
			releasedBy, time.Now().UTC(), tenantID, holdID,
		).Scan(&out.ID, &out.TenantID, &out.Name, &out.Description, &out.MatterReference,
			&out.AppliedBy, &out.AppliedAt, &out.ReleasedBy, &out.ReleasedAt, &out.IsActive)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return vdmserr.NotFound("active hold not found")
			}
			return vdmserr.FromPgError(err)
		}

		evt, err := model.NewOutboxEvent(tenantID, "dms.hold.released.v1", "legal_hold", holdID, map[string]any{
			"hold_id":     holdID.String(),
			"released_by": releasedBy.String(),
			"approver_id": in.Approver.String(),
			"reason":      in.Reason,
		})
		if err != nil {
			return err
		}
		return insertOutbox(ctx, tx, evt)
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Get returns one hold plus its document bindings.
func (s *HoldsService) Get(ctx context.Context, tenantID, holdID uuid.UUID) (*Hold, error) {
	var h Hold
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT id, tenant_id, name, COALESCE(description,''), COALESCE(matter_reference,''),
			       applied_by, applied_at, released_by, released_at, is_active
			  FROM legal_holds
			 WHERE tenant_id = $1 AND id = $2`,
			tenantID, holdID,
		).Scan(&h.ID, &h.TenantID, &h.Name, &h.Description, &h.MatterReference,
			&h.AppliedBy, &h.AppliedAt, &h.ReleasedBy, &h.ReleasedAt, &h.IsActive)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return vdmserr.NotFound("hold not found")
			}
			return vdmserr.FromPgError(err)
		}
		rows, err := tx.Query(ctx, `
			SELECT document_id::text FROM legal_hold_documents
			 WHERE tenant_id = $1 AND hold_id = $2`,
			tenantID, holdID,
		)
		if err != nil {
			return vdmserr.FromPgError(err)
		}
		defer rows.Close()
		for rows.Next() {
			var d string
			if err := rows.Scan(&d); err != nil {
				return err
			}
			h.DocumentIDs = append(h.DocumentIDs, d)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return &h, nil
}

// List returns holds for the tenant filtered by status / document /
// custodian. Returns [] (not nil) when empty.
func (s *HoldsService) List(ctx context.Context, tenantID uuid.UUID, f ListFilter) ([]*Hold, error) {
	q := `
		SELECT DISTINCT lh.id, lh.tenant_id, lh.name, COALESCE(lh.description,''), COALESCE(lh.matter_reference,''),
		       lh.applied_by, lh.applied_at, lh.released_by, lh.released_at, lh.is_active
		  FROM legal_holds lh
		  LEFT JOIN legal_hold_documents lhd
		    ON lhd.tenant_id = lh.tenant_id AND lhd.hold_id = lh.id
		 WHERE lh.tenant_id = $1`
	args := []any{tenantID}
	if f.Status == "active" {
		q += " AND lh.is_active = true"
	} else if f.Status == "released" {
		q += " AND lh.is_active = false"
	}
	if f.DocumentID != uuid.Nil {
		q += fmt.Sprintf(" AND lhd.document_id = $%d", len(args)+1)
		args = append(args, f.DocumentID)
	}
	if f.Custodian != uuid.Nil {
		q += fmt.Sprintf(" AND lh.applied_by = $%d", len(args)+1)
		args = append(args, f.Custodian)
	}
	q += " ORDER BY lh.applied_at DESC"

	out := []*Hold{}
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return vdmserr.FromPgError(err)
		}
		defer rows.Close()
		for rows.Next() {
			var h Hold
			if err := rows.Scan(&h.ID, &h.TenantID, &h.Name, &h.Description, &h.MatterReference,
				&h.AppliedBy, &h.AppliedAt, &h.ReleasedBy, &h.ReleasedAt, &h.IsActive); err != nil {
				return err
			}
			out = append(out, &h)
		}
		return rows.Err()
	})
	return out, err
}

// Update mutates name / description and adjusts the attached-documents
// set. Refuses to mutate a released hold.
func (s *HoldsService) Update(ctx context.Context, tenantID, holdID, actor uuid.UUID, in UpdateInput) (*Hold, error) {
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var isActive bool
		if err := tx.QueryRow(ctx,
			`SELECT is_active FROM legal_holds WHERE tenant_id=$1 AND id=$2`,
			tenantID, holdID,
		).Scan(&isActive); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return vdmserr.NotFound("hold not found")
			}
			return vdmserr.FromPgError(err)
		}
		if !isActive {
			return vdmserr.Conflict("hold has been released; cannot update")
		}

		if in.Name != nil || in.Description != nil {
			if _, err := tx.Exec(ctx, `
				UPDATE legal_holds
				   SET name = COALESCE($1, name),
				       description = COALESCE($2, description)
				 WHERE tenant_id = $3 AND id = $4`,
				in.Name, in.Description, tenantID, holdID,
			); err != nil {
				return vdmserr.FromPgError(err)
			}
		}

		for _, d := range in.AddDocs {
			var lifecycle string
			if err := tx.QueryRow(ctx, `
				SELECT lifecycle_state FROM documents
				 WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`,
				tenantID, d,
			).Scan(&lifecycle); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return vdmserr.NotFound(fmt.Sprintf("document %s not found", d))
				}
				return vdmserr.FromPgError(err)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO legal_hold_documents (tenant_id, hold_id, document_id, previous_lifecycle_state)
				VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
				tenantID, holdID, d, lifecycle,
			); err != nil {
				return vdmserr.FromPgError(err)
			}
		}
		for _, d := range in.RemoveDocs {
			if _, err := tx.Exec(ctx, `
				DELETE FROM legal_hold_documents
				 WHERE tenant_id = $1 AND hold_id = $2 AND document_id = $3`,
				tenantID, holdID, d,
			); err != nil {
				return vdmserr.FromPgError(err)
			}
		}

		evt, err := model.NewOutboxEvent(tenantID, "dms.hold.updated.v1", "legal_hold", holdID, map[string]any{
			"hold_id":     holdID.String(),
			"updated_by":  actor.String(),
			"added":       uuidsToStrings(in.AddDocs),
			"removed":     uuidsToStrings(in.RemoveDocs),
		})
		if err != nil {
			return err
		}
		return insertOutbox(ctx, tx, evt)
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, tenantID, holdID)
}

// AnyActiveHoldFor is the enforcement hook used by the document-
// delete path. Returns true when any row in legal_hold_documents
// joined to legal_holds.is_active=true references documentID.
func (s *HoldsService) AnyActiveHoldFor(ctx context.Context, tenantID, documentID uuid.UUID) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1
		      FROM legal_hold_documents lhd
		      JOIN legal_holds lh ON lh.tenant_id = lhd.tenant_id AND lh.id = lhd.hold_id
		     WHERE lhd.tenant_id = $1
		       AND lhd.document_id = $2
		       AND lh.is_active = true
		)`, tenantID, documentID,
	).Scan(&exists)
	return exists, err
}

// insertOutbox writes an OutboxEvent using the same column shape
// services/document/internal/repository/misc_repo.go uses. Inlined
// here because importing the repository package from compliance
// would create an import cycle (repository → compliance via the
// future delete-gate wiring).
func insertOutbox(ctx context.Context, tx pgx.Tx, evt *model.OutboxEvent) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO outbox (id, tenant_id, event_type, aggregate_type, aggregate_id, payload, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		evt.ID, evt.TenantID, evt.EventType, evt.AggregateType, evt.AggregateID,
		evt.Payload, evt.CreatedAt,
	)
	return err
}

func uuidsToStrings(ids []uuid.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}
