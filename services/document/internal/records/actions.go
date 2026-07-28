package records

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/model"
)

// recordImmutableOrNotFound disambiguates a 0-row mutation on records: a
// disposed/transferred record is immutable (its mandatory metadata + vital
// flag must not change post-disposition), which is a Conflict; a genuinely
// absent record is NotFound.
func recordImmutableOrNotFound(ctx context.Context, tx pgx.Tx, tenantID, recordID uuid.UUID) error {
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM records WHERE tenant_id=$1 AND id=$2)`,
		tenantID, recordID).Scan(&exists); err != nil {
		return vdmserr.FromPgError(err)
	}
	if exists {
		return vdmserr.Conflict("record is disposed/transferred; it is immutable")
	}
	return vdmserr.NotFound("record not found")
}

// SetVital toggles the vital-records designation (continuity-of-operations
// program). Emits dms.record.vital_set.v1 for event completeness.
func (s *Service) SetVital(ctx context.Context, tenantID uuid.UUID, recordID uuid.UUID, vital bool) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx,
			`UPDATE records SET vital_record=$3, updated_at=now()
			  WHERE tenant_id=$1 AND id=$2 AND disposition_state NOT IN ('disposed','transferred')`,
			tenantID, recordID, vital)
		if err != nil {
			return vdmserr.FromPgError(err)
		}
		if ct.RowsAffected() == 0 {
			// A disposed/transferred record is immutable; distinguish that from
			// a genuinely missing one.
			return recordImmutableOrNotFound(ctx, tx, tenantID, recordID)
		}
		evt, err := model.NewOutboxEvent(tenantID, "dms.record.vital_set.v1", "record", recordID, map[string]any{
			"record_id": recordID.String(), "vital_record": vital,
		})
		if err != nil {
			return err
		}
		return insertOutbox(ctx, tx, evt)
	})
}

// Freeze halts disposition independent of legal hold. A frozen record cannot
// be disposed until Unfreeze. Emits dms.record.frozen.v1.
func (s *Service) Freeze(ctx context.Context, tenantID, frozenBy, recordID uuid.UUID, reason string) error {
	if reason == "" {
		return vdmserr.Validation("reason", "required")
	}
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var state string
		if err := tx.QueryRow(ctx,
			`SELECT disposition_state FROM records WHERE tenant_id=$1 AND id=$2`,
			tenantID, recordID).Scan(&state); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return vdmserr.NotFound("record not found")
			}
			return vdmserr.FromPgError(err)
		}
		if state == "disposed" || state == "transferred" {
			return vdmserr.Conflict("record already disposed; cannot freeze")
		}
		if _, err := tx.Exec(ctx, `
			UPDATE records SET frozen=true, frozen_by=$3, frozen_at=now(), freeze_reason=$4, updated_at=now()
			 WHERE tenant_id=$1 AND id=$2`,
			tenantID, recordID, frozenBy, reason); err != nil {
			return vdmserr.FromPgError(err)
		}
		evt, err := model.NewOutboxEvent(tenantID, "dms.record.frozen.v1", "record", recordID, map[string]any{
			"record_id": recordID.String(), "frozen_by": frozenBy.String(), "reason": reason,
		})
		if err != nil {
			return err
		}
		return insertOutbox(ctx, tx, evt)
	})
}

// Unfreeze lifts a records freeze. Emits dms.record.unfrozen.v1.
func (s *Service) Unfreeze(ctx context.Context, tenantID, actor, recordID uuid.UUID) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, `
			UPDATE records SET frozen=false, frozen_by=NULL, frozen_at=NULL, freeze_reason=NULL, updated_at=now()
			 WHERE tenant_id=$1 AND id=$2 AND frozen=true`,
			tenantID, recordID)
		if err != nil {
			return vdmserr.FromPgError(err)
		}
		if ct.RowsAffected() == 0 {
			return vdmserr.NotFound("frozen record not found")
		}
		evt, err := model.NewOutboxEvent(tenantID, "dms.record.unfrozen.v1", "record", recordID, map[string]any{
			"record_id": recordID.String(), "unfrozen_by": actor.String(),
		})
		if err != nil {
			return err
		}
		return insertOutbox(ctx, tx, evt)
	})
}

// SetMetadata replaces the record's metadata map (mandatory-metadata fields
// like declaring agent, originating org, security classification). Emits
// dms.record.metadata_set.v1.
func (s *Service) SetMetadata(ctx context.Context, tenantID, recordID uuid.UUID, metadata map[string]any) (*Record, error) {
	if metadata == nil {
		metadata = map[string]any{}
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return nil, vdmserr.Validation("metadata", "not serializable")
	}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx,
			`UPDATE records SET metadata=$3::jsonb, updated_at=now()
			  WHERE tenant_id=$1 AND id=$2 AND disposition_state NOT IN ('disposed','transferred')`,
			tenantID, recordID, raw)
		if e != nil {
			return vdmserr.FromPgError(e)
		}
		if ct.RowsAffected() == 0 {
			return recordImmutableOrNotFound(ctx, tx, tenantID, recordID)
		}
		evt, e := model.NewOutboxEvent(tenantID, "dms.record.metadata_set.v1", "record", recordID, map[string]any{
			"record_id": recordID.String(), "keys": mapKeys(metadata),
		})
		if e != nil {
			return e
		}
		return insertOutbox(ctx, tx, evt)
	})
	if err != nil {
		return nil, err
	}
	// Return the record by id (reuse the document join via a small lookup).
	return s.getByID(ctx, tenantID, recordID)
}

// ---- accession / transfer export ----------------------------------------

// ManifestEntry is one record line in a transfer/accession manifest.
type ManifestEntry struct {
	RecordID          string         `json:"record_id"`
	DocumentID        string         `json:"document_id"`
	DocumentTitle     string         `json:"document_title"`
	CategoryID        string         `json:"category_id"`
	DispositionAction string         `json:"disposition_action"`
	DispositionState  string         `json:"disposition_state"`
	VitalRecord       bool           `json:"vital_record"`
	DeclaredAt        time.Time      `json:"declared_at"`
	CutoffDate        *time.Time     `json:"cutoff_date,omitempty"`
	DisposedAt        *time.Time     `json:"disposed_at,omitempty"`
	Metadata          map[string]any `json:"metadata,omitempty"`
}

// TransferManifest is the accession export — the structured handover a
// receiving archive ingests (DoD 5015.2 transfer / SF-258 analogue).
type TransferManifest struct {
	TenantID    string          `json:"tenant_id"`
	GeneratedAt time.Time       `json:"generated_at"`
	Count       int             `json:"count"`
	Entries     []ManifestEntry `json:"entries"`
}

// AccessionExport builds the transfer manifest. includeTransferredOnly limits
// it to records already disposed via the transfer action; otherwise it covers
// every record whose schedule's disposition action is transfer (the candidate
// transfer set). Emits dms.record.transfer_exported.v1 so the export itself is
// in the audit trail.
func (s *Service) AccessionExport(ctx context.Context, tenantID, actor uuid.UUID, includeTransferredOnly bool) (*TransferManifest, error) {
	man := &TransferManifest{TenantID: tenantID.String(), GeneratedAt: time.Now().UTC(), Entries: []ManifestEntry{}}
	where := "r.disposition_action = 'transfer'"
	if includeTransferredOnly {
		where = "r.disposition_state = 'transferred'"
	}
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT r.id, r.document_id, COALESCE(d.title,''), r.category_id,
			       COALESCE(r.disposition_action,''), r.disposition_state, r.vital_record,
			       r.declared_at, r.cutoff_date, r.disposed_at, r.metadata
			  FROM records r
			  LEFT JOIN documents d ON d.tenant_id=r.tenant_id AND d.id=r.document_id
			 WHERE r.tenant_id=$1 AND `+where+`
			 ORDER BY r.declared_at ASC`, tenantID)
		if err != nil {
			return vdmserr.FromPgError(err)
		}
		defer rows.Close()
		for rows.Next() {
			var e ManifestEntry
			var meta []byte
			if err := rows.Scan(&e.RecordID, &e.DocumentID, &e.DocumentTitle, &e.CategoryID,
				&e.DispositionAction, &e.DispositionState, &e.VitalRecord,
				&e.DeclaredAt, &e.CutoffDate, &e.DisposedAt, &meta); err != nil {
				return err
			}
			e.Metadata = decodeJSONMap(meta)
			man.Entries = append(man.Entries, e)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		man.Count = len(man.Entries)
		evt, err := model.NewOutboxEvent(tenantID, "dms.record.transfer_exported.v1", "record", tenantID, map[string]any{
			"exported_by": actor.String(), "count": man.Count,
		})
		if err != nil {
			return err
		}
		return insertOutbox(ctx, tx, evt)
	})
	if err != nil {
		return nil, err
	}
	return man, nil
}

// getByID is the by-record-id read used after metadata writes.
func (s *Service) getByID(ctx context.Context, tenantID, recordID uuid.UUID) (*Record, error) {
	r := &Record{TenantID: tenantID}
	var meta []byte
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT id, document_id, category_id, retention_schedule_id,
			       COALESCE(disposition_action,''), disposition_state,
			       declared_by, declared_at, cutoff_date, disposed_by, disposed_at,
			       disposition_event_id, vital_record, frozen, frozen_by, frozen_at,
			       COALESCE(freeze_reason,''), metadata, created_at, updated_at
			  FROM records WHERE tenant_id=$1 AND id=$2`,
			tenantID, recordID,
		).Scan(&r.ID, &r.DocumentID, &r.CategoryID, &r.RetentionScheduleID,
			&r.DispositionAction, &r.DispositionState, &r.DeclaredBy, &r.DeclaredAt,
			&r.CutoffDate, &r.DisposedBy, &r.DisposedAt, &r.DispositionEventID,
			&r.VitalRecord, &r.Frozen, &r.FrozenBy, &r.FrozenAt, &r.FreezeReason,
			&meta, &r.CreatedAt, &r.UpdatedAt)
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, vdmserr.NotFound("record not found")
		}
		return nil, vdmserr.FromPgError(err)
	}
	r.Metadata = decodeJSONMap(meta)
	return r, nil
}

func mapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
