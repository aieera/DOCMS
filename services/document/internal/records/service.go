package records

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/model"
)

var (
	validTriggers = map[string]bool{
		"declaration": true, "creation": true, "event": true,
		"superseded": true, "fixed_date": true,
	}
	validActions = map[string]bool{
		"destroy": true, "transfer": true, "permanent": true, "review": true,
	}
	validNodeTypes = map[string]bool{"category": true, "series": true}
)

// Service is the transaction-scoped records-management business layer.
type Service struct {
	pool *pgxpool.Pool
}

// New constructs a records Service bound to the given pool.
func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// ============================ Retention schedules ============================

// CreateSchedule adds a retention schedule (trigger + period + action).
func (s *Service) CreateSchedule(ctx context.Context, tenantID uuid.UUID, in ScheduleInput) (*RetentionSchedule, error) {
	if in.Name == "" {
		return nil, vdmserr.Validation("name", "required")
	}
	if in.TriggerEvent == "" {
		in.TriggerEvent = "declaration"
	}
	if in.DispositionAction == "" {
		in.DispositionAction = "review"
	}
	if !validTriggers[in.TriggerEvent] {
		return nil, vdmserr.Validation("trigger_event", "invalid")
	}
	if !validActions[in.DispositionAction] {
		return nil, vdmserr.Validation("disposition_action", "invalid")
	}
	if in.RetentionPeriodDays < 0 {
		return nil, vdmserr.Validation("retention_period_days", "must be >= 0")
	}
	out := &RetentionSchedule{TenantID: tenantID}
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO retention_schedules
			    (tenant_id, name, description, trigger_event, retention_period_days, disposition_action)
			VALUES ($1,$2,NULLIF($3,''),$4,$5,$6)
			RETURNING id, name, COALESCE(description,''), trigger_event,
			          retention_period_days, disposition_action, created_at, updated_at`,
			tenantID, in.Name, in.Description, in.TriggerEvent, in.RetentionPeriodDays, in.DispositionAction,
		).Scan(&out.ID, &out.Name, &out.Description, &out.TriggerEvent,
			&out.RetentionPeriodDays, &out.DispositionAction, &out.CreatedAt, &out.UpdatedAt)
	})
	if err != nil {
		return nil, vdmserr.FromPgError(err)
	}
	return out, nil
}

// ListSchedules returns all schedules for the tenant (newest first).
func (s *Service) ListSchedules(ctx context.Context, tenantID uuid.UUID) ([]*RetentionSchedule, error) {
	out := []*RetentionSchedule{}
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, name, COALESCE(description,''), trigger_event,
			       retention_period_days, disposition_action, created_at, updated_at
			  FROM retention_schedules WHERE tenant_id = $1
			 ORDER BY created_at DESC`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r := &RetentionSchedule{TenantID: tenantID}
			if err := rows.Scan(&r.ID, &r.Name, &r.Description, &r.TriggerEvent,
				&r.RetentionPeriodDays, &r.DispositionAction, &r.CreatedAt, &r.UpdatedAt); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// UpdateSchedule patches a schedule. Existing declarations keep their
// snapshotted cutoff/action — schedule edits only affect future declarations.
func (s *Service) UpdateSchedule(ctx context.Context, tenantID, id uuid.UUID, in ScheduleInput) (*RetentionSchedule, error) {
	if in.TriggerEvent != "" && !validTriggers[in.TriggerEvent] {
		return nil, vdmserr.Validation("trigger_event", "invalid")
	}
	if in.DispositionAction != "" && !validActions[in.DispositionAction] {
		return nil, vdmserr.Validation("disposition_action", "invalid")
	}
	out := &RetentionSchedule{TenantID: tenantID}
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			UPDATE retention_schedules SET
			    name = COALESCE(NULLIF($3,''), name),
			    description = COALESCE(NULLIF($4,''), description),
			    trigger_event = COALESCE(NULLIF($5,''), trigger_event),
			    retention_period_days = CASE WHEN $6 < 0 THEN retention_period_days ELSE $6 END,
			    disposition_action = COALESCE(NULLIF($7,''), disposition_action),
			    updated_at = now()
			 WHERE tenant_id = $1 AND id = $2
			RETURNING id, name, COALESCE(description,''), trigger_event,
			          retention_period_days, disposition_action, created_at, updated_at`,
			tenantID, id, in.Name, in.Description, in.TriggerEvent, in.RetentionPeriodDays, in.DispositionAction,
		).Scan(&out.ID, &out.Name, &out.Description, &out.TriggerEvent,
			&out.RetentionPeriodDays, &out.DispositionAction, &out.CreatedAt, &out.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return vdmserr.NotFound("schedule not found")
		}
		return err
	})
	if err != nil {
		return nil, vdmserr.FromPgError(err)
	}
	return out, nil
}

// DeleteSchedule removes a schedule. Fails if a category still references it
// (FK) — detach it from the file plan first.
func (s *Service) DeleteSchedule(ctx context.Context, tenantID, id uuid.UUID) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, `DELETE FROM retention_schedules WHERE tenant_id=$1 AND id=$2`, tenantID, id)
		if err != nil {
			return vdmserr.FromPgError(err)
		}
		if ct.RowsAffected() == 0 {
			return vdmserr.NotFound("schedule not found")
		}
		return nil
	})
}

// ============================ File-plan categories ===========================

// CreateCategory adds a node to the file-plan tree.
func (s *Service) CreateCategory(ctx context.Context, tenantID uuid.UUID, in CategoryInput) (*RecordCategory, error) {
	if in.Name == "" {
		return nil, vdmserr.Validation("name", "required")
	}
	if in.NodeType == "" {
		in.NodeType = "category"
	}
	if !validNodeTypes[in.NodeType] {
		return nil, vdmserr.Validation("node_type", "must be category|series")
	}
	out := &RecordCategory{TenantID: tenantID}
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO record_categories
			    (tenant_id, parent_id, name, code, node_type, description, retention_schedule_id)
			VALUES ($1,$2,$3,NULLIF($4,''),$5,NULLIF($6,''),$7)
			RETURNING id, parent_id, name, COALESCE(code,''), node_type,
			          COALESCE(description,''), retention_schedule_id, created_at, updated_at`,
			tenantID, in.ParentID, in.Name, in.Code, in.NodeType, in.Description, in.RetentionScheduleID,
		).Scan(&out.ID, &out.ParentID, &out.Name, &out.Code, &out.NodeType,
			&out.Description, &out.RetentionScheduleID, &out.CreatedAt, &out.UpdatedAt)
	})
	if err != nil {
		return nil, vdmserr.FromPgError(err)
	}
	return out, nil
}

// ListCategories returns the flat node set; the frontend assembles the tree
// from parent_id (cheaper + simpler than a recursive CTE for plan-sized data).
func (s *Service) ListCategories(ctx context.Context, tenantID uuid.UUID) ([]*RecordCategory, error) {
	out := []*RecordCategory{}
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, parent_id, name, COALESCE(code,''), node_type,
			       COALESCE(description,''), retention_schedule_id, created_at, updated_at
			  FROM record_categories WHERE tenant_id = $1
			 ORDER BY name ASC`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c := &RecordCategory{TenantID: tenantID}
			if err := rows.Scan(&c.ID, &c.ParentID, &c.Name, &c.Code, &c.NodeType,
				&c.Description, &c.RetentionScheduleID, &c.CreatedAt, &c.UpdatedAt); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// UpdateCategory patches a node (name/code/description/schedule). parent_id is
// not re-parented here to keep the tree edit simple and cycle-free.
func (s *Service) UpdateCategory(ctx context.Context, tenantID, id uuid.UUID, in CategoryInput) (*RecordCategory, error) {
	if in.NodeType != "" && !validNodeTypes[in.NodeType] {
		return nil, vdmserr.Validation("node_type", "must be category|series")
	}
	out := &RecordCategory{TenantID: tenantID}
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			UPDATE record_categories SET
			    name = COALESCE(NULLIF($3,''), name),
			    code = COALESCE(NULLIF($4,''), code),
			    node_type = COALESCE(NULLIF($5,''), node_type),
			    description = COALESCE(NULLIF($6,''), description),
			    retention_schedule_id = $7,
			    updated_at = now()
			 WHERE tenant_id = $1 AND id = $2
			RETURNING id, parent_id, name, COALESCE(code,''), node_type,
			          COALESCE(description,''), retention_schedule_id, created_at, updated_at`,
			tenantID, id, in.Name, in.Code, in.NodeType, in.Description, in.RetentionScheduleID,
		).Scan(&out.ID, &out.ParentID, &out.Name, &out.Code, &out.NodeType,
			&out.Description, &out.RetentionScheduleID, &out.CreatedAt, &out.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return vdmserr.NotFound("category not found")
		}
		return err
	})
	if err != nil {
		return nil, vdmserr.FromPgError(err)
	}
	return out, nil
}

// DeleteCategory removes a node and its subtree (FK ON DELETE CASCADE). Fails
// if any record is declared against a node in the subtree (FK from records).
func (s *Service) DeleteCategory(ctx context.Context, tenantID, id uuid.UUID) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, `DELETE FROM record_categories WHERE tenant_id=$1 AND id=$2`, tenantID, id)
		if err != nil {
			return vdmserr.FromPgError(err)
		}
		if ct.RowsAffected() == 0 {
			return vdmserr.NotFound("category not found")
		}
		return nil
	})
}

// ============================ Declare / Dispose ==============================

// Declare files a document against a plan node, freezing it (immutable until
// disposition). The schedule is resolved from the category and snapshotted;
// the cutoff is computed now for declaration-triggered schedules. Emits
// dms.record.declared.v1 in the same transaction.
func (s *Service) Declare(ctx context.Context, tenantID, declaredBy uuid.UUID, in DeclareInput) (*Record, error) {
	if in.DocumentID == uuid.Nil {
		return nil, vdmserr.Validation("document_id", "required")
	}
	if in.CategoryID == uuid.Nil {
		return nil, vdmserr.Validation("category_id", "required")
	}
	rec := &Record{TenantID: tenantID, DocumentID: in.DocumentID, CategoryID: in.CategoryID, DeclaredBy: &declaredBy}
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		// Document must exist and not be deleted/disposed.
		var lifecycle string
		if err := tx.QueryRow(ctx, `
			SELECT lifecycle_state FROM documents
			 WHERE tenant_id=$1 AND id=$2 AND deleted_at IS NULL`,
			tenantID, in.DocumentID,
		).Scan(&lifecycle); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return vdmserr.NotFound("document not found")
			}
			return vdmserr.FromPgError(err)
		}
		if lifecycle == string(model.StateDisposed) {
			return vdmserr.Conflict("document is disposed")
		}

		// Resolve the schedule from the category (nullable).
		var (
			schedID *uuid.UUID
			trigger *string
			periodD *int
			action  *string
		)
		if err := tx.QueryRow(ctx, `
			SELECT rs.id, rs.trigger_event, rs.retention_period_days, rs.disposition_action
			  FROM record_categories c
			  LEFT JOIN retention_schedules rs
			    ON rs.tenant_id = c.tenant_id AND rs.id = c.retention_schedule_id
			 WHERE c.tenant_id=$1 AND c.id=$2`,
			tenantID, in.CategoryID,
		).Scan(&schedID, &trigger, &periodD, &action); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return vdmserr.NotFound("category not found")
			}
			return vdmserr.FromPgError(err)
		}

		rec.DeclaredAt = time.Now().UTC()
		rec.RetentionScheduleID = schedID
		if action != nil {
			rec.DispositionAction = *action
		}
		// Declaration-triggered schedules get a concrete cutoff now; other
		// triggers (superseded / fixed_date / event) resolve theirs later.
		if schedID != nil && trigger != nil && *trigger == "declaration" && periodD != nil {
			c := rec.DeclaredAt.AddDate(0, 0, *periodD)
			rec.CutoffDate = &c
		}

		err := tx.QueryRow(ctx, `
			INSERT INTO records
			    (tenant_id, document_id, category_id, retention_schedule_id,
			     disposition_action, disposition_state, declared_by, declared_at, cutoff_date)
			VALUES ($1,$2,$3,$4,NULLIF($5,''),'declared',$6,$7,$8)
			RETURNING id, disposition_state, created_at, updated_at`,
			tenantID, in.DocumentID, in.CategoryID, rec.RetentionScheduleID,
			rec.DispositionAction, declaredBy, rec.DeclaredAt, rec.CutoffDate,
		).Scan(&rec.ID, &rec.DispositionState, &rec.CreatedAt, &rec.UpdatedAt)
		if err != nil {
			// UNIQUE(tenant_id, document_id) → already a record.
			return vdmserr.FromPgError(err)
		}

		evt, err := model.NewOutboxEvent(tenantID, "dms.record.declared.v1", "record", rec.ID, map[string]any{
			"record_id":   rec.ID.String(),
			"document_id": in.DocumentID.String(),
			"category_id": in.CategoryID.String(),
			"declared_by": declaredBy.String(),
			"cutoff_date": cutoffStr(rec.CutoffDate),
			"disposition": rec.DispositionAction,
		})
		if err != nil {
			return err
		}
		return insertOutbox(ctx, tx, evt)
	})
	if err != nil {
		return nil, err
	}
	return rec, nil
}

// Dispose certifies the disposition of a declared record: marks it disposed
// (or transferred), transitions the document to `disposed`, and emits
// dms.record.disposed.v1 — which the audit service appends to the tamper-
// evident hash chain. The emitted event id is stored as the disposition
// certificate handle (disposition_event_id).
func (s *Service) Dispose(ctx context.Context, tenantID, disposedBy, recordID uuid.UUID, in DisposeInput) (*Record, error) {
	if !in.Certify {
		return nil, vdmserr.Validation("certify", "disposition must be certified")
	}
	out := &Record{TenantID: tenantID}
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var (
			docID  uuid.UUID
			state  string
			action string
			frozen bool
		)
		if err := tx.QueryRow(ctx, `
			SELECT document_id, disposition_state, COALESCE(disposition_action,'review'), frozen
			  FROM records WHERE tenant_id=$1 AND id=$2`,
			tenantID, recordID,
		).Scan(&docID, &state, &action, &frozen); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return vdmserr.NotFound("record not found")
			}
			return vdmserr.FromPgError(err)
		}
		if state == "disposed" || state == "transferred" {
			return vdmserr.Conflict("record already disposed")
		}
		// A records freeze halts disposition independent of legal hold.
		if frozen {
			return vdmserr.Conflict("record is frozen; unfreeze before disposition")
		}

		finalState := "disposed"
		if action == "transfer" {
			finalState = "transferred"
		}
		if action == "permanent" {
			return vdmserr.Conflict("permanent records cannot be disposed")
		}

		// The disposition certificate handle is the outbox event id.
		evt, err := model.NewOutboxEvent(tenantID, "dms.record.disposed.v1", "record", recordID, map[string]any{
			"record_id":          recordID.String(),
			"document_id":        docID.String(),
			"disposed_by":        disposedBy.String(),
			"disposition_action": action,
			"final_state":        finalState,
			"reason":             in.Reason,
		})
		if err != nil {
			return err
		}

		now := time.Now().UTC()
		err = tx.QueryRow(ctx, `
			UPDATE records SET
			    disposition_state = $3,
			    disposed_by = $4,
			    disposed_at = $5,
			    disposition_event_id = $6,
			    updated_at = now()
			 WHERE tenant_id=$1 AND id=$2
			RETURNING id, document_id, category_id, retention_schedule_id,
			          COALESCE(disposition_action,''), disposition_state,
			          declared_by, declared_at, cutoff_date, disposed_by, disposed_at,
			          disposition_event_id, created_at, updated_at`,
			tenantID, recordID, finalState, disposedBy, now, evt.ID,
		).Scan(&out.ID, &out.DocumentID, &out.CategoryID, &out.RetentionScheduleID,
			&out.DispositionAction, &out.DispositionState, &out.DeclaredBy, &out.DeclaredAt,
			&out.CutoffDate, &out.DisposedBy, &out.DisposedAt, &out.DispositionEventID,
			&out.CreatedAt, &out.UpdatedAt)
		if err != nil {
			return vdmserr.FromPgError(err)
		}
		out.TenantID = tenantID

		// Transition the document to its terminal lifecycle state. This is
		// the authorized disposition path, so it sets `disposed` directly
		// (the records domain owns this transition).
		if _, err := tx.Exec(ctx, `
			UPDATE documents SET lifecycle_state='disposed', updated_at=now()
			 WHERE tenant_id=$1 AND id=$2 AND deleted_at IS NULL`,
			tenantID, docID,
		); err != nil {
			return vdmserr.FromPgError(err)
		}
		return insertOutbox(ctx, tx, evt)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ============================ Queue / sweep / gate ===========================

// DispositionQueue returns records eligible for disposition review: those at
// cutoff_pending (the janitor flagged them past cutoff), and optionally the
// still-active declared ones. Joined with the document title for the UI.
func (s *Service) DispositionQueue(ctx context.Context, tenantID uuid.UUID, includeDeclared bool) ([]*Record, error) {
	states := "('cutoff_pending')"
	if includeDeclared {
		states = "('cutoff_pending','declared')"
	}
	out := []*Record{}
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT r.id, r.document_id, r.category_id, r.retention_schedule_id,
			       COALESCE(r.disposition_action,''), r.disposition_state,
			       r.declared_by, r.declared_at, r.cutoff_date, r.disposed_by, r.disposed_at,
			       r.disposition_event_id, r.created_at, r.updated_at,
			       COALESCE(d.title,'')
			  FROM records r
			  LEFT JOIN documents d ON d.tenant_id=r.tenant_id AND d.id=r.document_id
			 WHERE r.tenant_id=$1 AND r.disposition_state IN `+states+`
			 ORDER BY r.cutoff_date ASC NULLS LAST, r.declared_at ASC`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r := &Record{TenantID: tenantID}
			if err := rows.Scan(&r.ID, &r.DocumentID, &r.CategoryID, &r.RetentionScheduleID,
				&r.DispositionAction, &r.DispositionState, &r.DeclaredBy, &r.DeclaredAt,
				&r.CutoffDate, &r.DisposedBy, &r.DisposedAt, &r.DispositionEventID,
				&r.CreatedAt, &r.UpdatedAt, &r.DocumentTitle); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// GetByDocument returns the record for a document, or nil if not declared.
func (s *Service) GetByDocument(ctx context.Context, tenantID, documentID uuid.UUID) (*Record, error) {
	var r *Record
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		row := &Record{TenantID: tenantID}
		var meta []byte
		err := tx.QueryRow(ctx, `
			SELECT id, document_id, category_id, retention_schedule_id,
			       COALESCE(disposition_action,''), disposition_state,
			       declared_by, declared_at, cutoff_date, disposed_by, disposed_at,
			       disposition_event_id, vital_record, frozen, frozen_by, frozen_at,
			       COALESCE(freeze_reason,''), metadata, created_at, updated_at
			  FROM records WHERE tenant_id=$1 AND document_id=$2`,
			tenantID, documentID,
		).Scan(&row.ID, &row.DocumentID, &row.CategoryID, &row.RetentionScheduleID,
			&row.DispositionAction, &row.DispositionState, &row.DeclaredBy, &row.DeclaredAt,
			&row.CutoffDate, &row.DisposedBy, &row.DisposedAt, &row.DispositionEventID,
			&row.VitalRecord, &row.Frozen, &row.FrozenBy, &row.FrozenAt, &row.FreezeReason,
			&meta, &row.CreatedAt, &row.UpdatedAt)
		if err == nil {
			row.Metadata = decodeJSONMap(meta)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // not declared → r stays nil
		}
		if err != nil {
			return err
		}
		r = row
		return nil
	})
	return r, err
}

// IsDeclaredRecord is the immutability gate used by DocumentService on
// mutating paths. Returns true while a record is declared|cutoff_pending
// (i.e. not yet disposed) — mirrors HoldsService.AnyActiveHoldFor.
func (s *Service) IsDeclaredRecord(ctx context.Context, tenantID, documentID uuid.UUID) (bool, error) {
	var exists bool
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT EXISTS (
			    SELECT 1 FROM records
			     WHERE tenant_id=$1 AND document_id=$2
			       AND disposition_state IN ('declared','cutoff_pending')
			)`, tenantID, documentID,
		).Scan(&exists)
	})
	return exists, err
}

// ProposeDispositionsAtCutoff is the janitor sweep: it flips declared records
// whose cutoff has passed to cutoff_pending so they surface in the disposition
// review queue ("the janitor proposes disposition"). Returns how many it moved.
func (s *Service) ProposeDispositionsAtCutoff(ctx context.Context, tenantID uuid.UUID) (int, error) {
	var moved int
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, `
			UPDATE records SET disposition_state='cutoff_pending', updated_at=now()
			 WHERE tenant_id=$1 AND disposition_state='declared'
			   AND cutoff_date IS NOT NULL AND cutoff_date <= now()`, tenantID)
		if err != nil {
			return vdmserr.FromPgError(err)
		}
		moved = int(ct.RowsAffected())
		return nil
	})
	return moved, err
}

// ============================ helpers ========================================

func cutoffStr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// decodeJSONMap unmarshals a jsonb column into a map, tolerating null/empty.
func decodeJSONMap(b []byte) map[string]any {
	if len(b) == 0 {
		return map[string]any{}
	}
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	return m
}

// insertOutbox writes an OutboxEvent inside the same txn (same column shape as
// repository/misc_repo.go). Inlined to avoid a records→repository import.
func insertOutbox(ctx context.Context, tx pgx.Tx, evt *model.OutboxEvent) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO outbox (id, tenant_id, event_type, aggregate_type, aggregate_id, payload, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		evt.ID, evt.TenantID, evt.EventType, evt.AggregateType, evt.AggregateID,
		evt.Payload, evt.CreatedAt,
	)
	return err
}
