// Package repository persists workflow definitions, instances, and tasks.
//
// Wave 11.1: every read/write now routes through a tenant-scoped
// transaction so Postgres RLS (`USING tenant_id = current_setting('app.current_tenant')`)
// fires. Pre-Wave-11 queries ran against the raw pool and relied
// only on explicit WHERE clauses; defense-in-depth fix.
package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vaultdms/vaultdms/pkg/database"
	"github.com/vaultdms/vaultdms/services/workflow/internal/model"
)

// Repository manages workflow tables.
type Repository struct{ pool *pgxpool.Pool }

// New constructs a Repository.
func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func newID() string { id, _ := uuid.NewV7(); return id.String() }

// runTenant parses tenantID + runs fn inside a tenant-scoped tx.
// Identical pattern to activities.runTenant; factored separately so
// the two packages stay import-cycle-free.
func (r *Repository) runTenant(ctx context.Context, tenantID string, fn func(tx pgx.Tx) error) error {
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		return fmt.Errorf("tenant_id: %w", err)
	}
	return database.WithTenantTx(ctx, r.pool, tid, fn)
}

// ---- Definitions ----------------------------------------------------------

// definitionEnvelope is what we store inside the workflow_definitions
// `definition` JSONB column. The column holds the whole workflow
// spec — today that's just a steps array, tomorrow it's likely
// branches / variables / SLA defaults — so we wrap rather than store
// a bare list. unmarshalDefinition tolerates a bare `[...]` array
// for back-compat with rows written before this PR (none exist yet
// in dev, but cheap to support).
type definitionEnvelope struct {
	Steps []model.Step `json:"steps"`
}

func marshalDefinition(steps []model.Step) ([]byte, error) {
	return json.Marshal(definitionEnvelope{Steps: steps})
}

func unmarshalDefinition(raw []byte) []model.Step {
	if len(raw) == 0 {
		return nil
	}
	var env definitionEnvelope
	if err := json.Unmarshal(raw, &env); err == nil && len(env.Steps) > 0 {
		return env.Steps
	}
	// Back-compat: row was written as a bare array.
	var bare []model.Step
	_ = json.Unmarshal(raw, &bare)
	return bare
}

// CreateDefinition persists a workflow definition.
func (r *Repository) CreateDefinition(ctx context.Context, d *model.WorkflowDefinition) error {
	defJSON, err := marshalDefinition(d.Steps)
	if err != nil {
		return err
	}
	return r.runTenant(ctx, d.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO workflow_definitions (id, tenant_id, name, description, definition, created_by, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5::jsonb,$6,$7,$8)
		`, d.ID, d.TenantID, d.Name, d.Description, defJSON, d.CreatedBy, d.CreatedAt, d.UpdatedAt)
		return err
	})
}

// ListDefinitions returns all definitions for a tenant.
func (r *Repository) ListDefinitions(ctx context.Context, tenantID string) ([]*model.WorkflowDefinition, error) {
	var out []*model.WorkflowDefinition
	err := r.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, tenant_id, name, COALESCE(description,''), definition, COALESCE(created_by::text,''), created_at, updated_at
			FROM workflow_definitions WHERE tenant_id = $1 ORDER BY created_at DESC`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d := &model.WorkflowDefinition{}
			var defJSON []byte
			if err := rows.Scan(&d.ID, &d.TenantID, &d.Name, &d.Description, &defJSON, &d.CreatedBy, &d.CreatedAt, &d.UpdatedAt); err != nil {
				return err
			}
			d.Steps = unmarshalDefinition(defJSON)
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// GetDefinition returns one definition.
func (r *Repository) GetDefinition(ctx context.Context, tenantID, id string) (*model.WorkflowDefinition, error) {
	d := &model.WorkflowDefinition{}
	var defJSON []byte
	var found bool
	err := r.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT id, tenant_id, name, COALESCE(description,''), definition, COALESCE(created_by::text,''), created_at, updated_at
			FROM workflow_definitions WHERE tenant_id = $1 AND id = $2`, tenantID, id).
			Scan(&d.ID, &d.TenantID, &d.Name, &d.Description, &defJSON, &d.CreatedBy, &d.CreatedAt, &d.UpdatedAt)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return nil
	})
	if err != nil || !found {
		return nil, err
	}
	d.Steps = unmarshalDefinition(defJSON)
	return d, nil
}

// ---- Instances ------------------------------------------------------------

// CreateInstance persists a new workflow instance.
//
// Column-name reconciliation: the Go model uses domain-friendly
// names (InitiatedBy / CurrentStep / TemporalRunID / CreatedAt) but
// the DB columns are started_by / current_step_id / temporal_workflow_id
// / started_at. The previous queries referenced the Go names verbatim
// and failed at runtime with UndefinedColumn. We map at the SQL
// boundary so the service-layer types stay stable.
//
// CurrentStep is an INT in the Go model but current_step_id is TEXT
// in the DB. We marshal via strconv so a fresh instance writes "0",
// and unmarshal back to int on read; non-numeric stored values
// (future step-id strings) fall through with CurrentStep=0.
func (r *Repository) CreateInstance(ctx context.Context, inst *model.WorkflowInstance) error {
	return r.runTenant(ctx, inst.TenantID, func(tx pgx.Tx) error {
		var startedBy any
		if inst.InitiatedBy != "" {
			startedBy = inst.InitiatedBy
		}
		var temporal any
		if inst.TemporalRunID != "" {
			temporal = inst.TemporalRunID
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO workflow_instances (id, tenant_id, definition_id, document_id, started_by, status, current_step_id, temporal_workflow_id, started_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		`, inst.ID, inst.TenantID, inst.DefinitionID, inst.DocumentID, startedBy,
			inst.Status, strconv.Itoa(inst.CurrentStep), temporal, inst.CreatedAt)
		return err
	})
}

// GetInstance returns one instance.
func (r *Repository) GetInstance(ctx context.Context, tenantID, id string) (*model.WorkflowInstance, error) {
	inst := &model.WorkflowInstance{}
	var found bool
	err := r.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		var currentStepID, temporal *string
		var startedBy *string
		err := tx.QueryRow(ctx, `
			SELECT id, tenant_id, definition_id, COALESCE(document_id::text,''), started_by::text, status, current_step_id, temporal_workflow_id, started_at, completed_at
			FROM workflow_instances WHERE tenant_id = $1 AND id = $2`, tenantID, id).
			Scan(&inst.ID, &inst.TenantID, &inst.DefinitionID, &inst.DocumentID, &startedBy,
				&inst.Status, &currentStepID, &temporal, &inst.CreatedAt, &inst.CompletedAt)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		if startedBy != nil {
			inst.InitiatedBy = *startedBy
		}
		if currentStepID != nil {
			if n, perr := strconv.Atoi(*currentStepID); perr == nil {
				inst.CurrentStep = n
			}
		}
		if temporal != nil {
			inst.TemporalRunID = *temporal
		}
		found = true
		return nil
	})
	if err != nil || !found {
		return nil, err
	}
	return inst, nil
}

// GetActiveInstanceByDocument returns the single non-terminal instance
// for a document, if any. "Active" means status NOT IN ('completed',
// 'failed', 'cancelled'). The unique-active-per-doc invariant isn't
// enforced at the schema level, so we ORDER BY started_at DESC and
// take one; the document service must avoid starting a second
// instance when one is already active.
func (r *Repository) GetActiveInstanceByDocument(ctx context.Context, tenantID, documentID string) (*model.WorkflowInstance, error) {
	inst := &model.WorkflowInstance{}
	var found bool
	err := r.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		var currentStepID, temporal *string
		var startedBy *string
		err := tx.QueryRow(ctx, `
			SELECT id, tenant_id, definition_id, COALESCE(document_id::text,''), started_by::text, status,
			       current_step_id, temporal_workflow_id, started_at, completed_at
			FROM workflow_instances
			WHERE tenant_id = $1 AND document_id = $2
			  AND status NOT IN ('completed','failed','cancelled')
			ORDER BY started_at DESC
			LIMIT 1`, tenantID, documentID).
			Scan(&inst.ID, &inst.TenantID, &inst.DefinitionID, &inst.DocumentID, &startedBy,
				&inst.Status, &currentStepID, &temporal, &inst.CreatedAt, &inst.CompletedAt)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		if startedBy != nil {
			inst.InitiatedBy = *startedBy
		}
		if currentStepID != nil {
			if n, perr := strconv.Atoi(*currentStepID); perr == nil {
				inst.CurrentStep = n
			}
		}
		if temporal != nil {
			inst.TemporalRunID = *temporal
		}
		found = true
		return nil
	})
	if err != nil || !found {
		return nil, err
	}
	return inst, nil
}

// UpdateDefinition replaces name/description/steps on an existing
// row + bumps the updated_at trigger. Returns ErrNoRows if no row
// matches (tenant_id, id).
func (r *Repository) UpdateDefinition(ctx context.Context, d *model.WorkflowDefinition) error {
	defJSON, err := marshalDefinition(d.Steps)
	if err != nil {
		return err
	}
	return r.runTenant(ctx, d.TenantID, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, `
			UPDATE workflow_definitions
			   SET name = $3, description = $4, definition = $5::jsonb
			 WHERE tenant_id = $1 AND id = $2 AND is_active = true`,
			d.TenantID, d.ID, d.Name, d.Description, defJSON)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
		return nil
	})
}

// DeleteDefinition soft-deletes via is_active=false. Active instances
// referencing this definition keep running; only the template's
// availability to start new instances is revoked.
func (r *Repository) DeleteDefinition(ctx context.Context, tenantID, id string) error {
	return r.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, `
			UPDATE workflow_definitions SET is_active = false
			 WHERE tenant_id = $1 AND id = $2 AND is_active = true`,
			tenantID, id)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
		return nil
	})
}

// MarkInstanceCancelled flips status to 'cancelled' + stamps
// completed_at. Used by the immediate-DB-write path of
// service.CancelInstance so the UI reflects the new state without
// waiting for the Temporal callback (which may never arrive if
// Temporal is degraded).
func (r *Repository) MarkInstanceCancelled(ctx context.Context, tenantID, id string) error {
	return r.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, `
			UPDATE workflow_instances
			   SET status = 'cancelled', completed_at = now()
			 WHERE tenant_id = $1 AND id = $2
			   AND status NOT IN ('completed','failed','cancelled')`,
			tenantID, id)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
		return nil
	})
}

// ListActiveInstances returns every non-terminal instance for the
// tenant. Used by the admin panel's active-instances table.
func (r *Repository) ListActiveInstances(ctx context.Context, tenantID string) ([]*model.WorkflowInstance, error) {
	var out []*model.WorkflowInstance
	err := r.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, tenant_id, definition_id, COALESCE(document_id::text,''),
			       started_by::text, status, current_step_id, temporal_workflow_id,
			       started_at, completed_at
			FROM workflow_instances
			WHERE tenant_id = $1 AND status NOT IN ('completed','failed','cancelled')
			ORDER BY started_at DESC`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			inst := &model.WorkflowInstance{}
			var currentStepID, temporal *string
			var startedBy *string
			if err := rows.Scan(&inst.ID, &inst.TenantID, &inst.DefinitionID, &inst.DocumentID,
				&startedBy, &inst.Status, &currentStepID, &temporal,
				&inst.CreatedAt, &inst.CompletedAt); err != nil {
				return err
			}
			if startedBy != nil {
				inst.InitiatedBy = *startedBy
			}
			if currentStepID != nil {
				if n, perr := strconv.Atoi(*currentStepID); perr == nil {
					inst.CurrentStep = n
				}
			}
			if temporal != nil {
				inst.TemporalRunID = *temporal
			}
			out = append(out, inst)
		}
		return rows.Err()
	})
	return out, err
}

// ---- Tasks ----------------------------------------------------------------

// ListTasks returns tasks for an assignee across all instances, optionally
// filtered by status. The query LEFT JOINs `documents` so the inbox UI
// can render the document title without a follow-up round trip.
func (r *Repository) ListTasks(ctx context.Context, tenantID, assigneeID, status string) ([]*model.Task, error) {
	q := `SELECT t.id, t.tenant_id, t.instance_id, COALESCE(t.document_id::text, '') AS document_id,
	             COALESCE(d.title, '') AS document_title,
	             t.step_name, t.assignee_id, t.status, COALESCE(t.notes, '') AS notes,
	             t.due_at, t.created_at, t.completed_at
	        FROM workflow_tasks t
	        LEFT JOIN documents d ON d.tenant_id = t.tenant_id AND d.id = t.document_id
	       WHERE t.tenant_id = $1 AND t.assignee_id = $2`
	args := []any{tenantID, assigneeID}
	if status != "" {
		q += ` AND t.status = $3`
		args = append(args, status)
	}
	q += ` ORDER BY t.created_at DESC`
	var out []*model.Task
	err := r.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t := &model.Task{}
			if err := rows.Scan(&t.ID, &t.TenantID, &t.InstanceID, &t.DocumentID, &t.DocumentTitle, &t.StepName,
				&t.AssigneeID, &t.Status, &t.Notes, &t.DueAt, &t.CreatedAt, &t.CompletedAt); err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

// ListTasksByInstance returns every task for one instance, oldest first.
// Used by the timeline endpoint to render the run history.
func (r *Repository) ListTasksByInstance(ctx context.Context, tenantID, instanceID string) ([]*model.Task, error) {
	var out []*model.Task
	err := r.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT t.id, t.tenant_id, t.instance_id, COALESCE(t.document_id::text, '') AS document_id,
			       COALESCE(d.title, '') AS document_title,
			       t.step_name, t.assignee_id, t.status, COALESCE(t.notes, '') AS notes,
			       t.due_at, t.created_at, t.completed_at
			  FROM workflow_tasks t
			  LEFT JOIN documents d ON d.tenant_id = t.tenant_id AND d.id = t.document_id
			 WHERE t.tenant_id = $1 AND t.instance_id = $2
			 ORDER BY t.created_at ASC`, tenantID, instanceID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t := &model.Task{}
			if err := rows.Scan(&t.ID, &t.TenantID, &t.InstanceID, &t.DocumentID, &t.DocumentTitle, &t.StepName,
				&t.AssigneeID, &t.Status, &t.Notes, &t.DueAt, &t.CreatedAt, &t.CompletedAt); err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

// CompleteTask marks a task completed and sets the outcome.
func (r *Repository) CompleteTask(ctx context.Context, tenantID, taskID, status, notes string) error {
	return r.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE workflow_tasks SET status = $1, notes = $2, completed_at = $3
			WHERE tenant_id = $4 AND id = $5`,
			status, notes, time.Now().UTC(), tenantID, taskID)
		return err
	})
}
