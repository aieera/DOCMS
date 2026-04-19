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

// CreateDefinition persists a workflow definition.
func (r *Repository) CreateDefinition(ctx context.Context, d *model.WorkflowDefinition) error {
	stepsJSON, _ := json.Marshal(d.Steps)
	return r.runTenant(ctx, d.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO workflow_definitions (id, tenant_id, name, description, steps, created_by, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		`, d.ID, d.TenantID, d.Name, d.Description, stepsJSON, d.CreatedBy, d.CreatedAt, d.UpdatedAt)
		return err
	})
}

// ListDefinitions returns all definitions for a tenant.
func (r *Repository) ListDefinitions(ctx context.Context, tenantID string) ([]*model.WorkflowDefinition, error) {
	var out []*model.WorkflowDefinition
	err := r.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, tenant_id, name, description, steps, created_by, created_at, updated_at
			FROM workflow_definitions WHERE tenant_id = $1 ORDER BY created_at DESC`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d := &model.WorkflowDefinition{}
			var stepsJSON []byte
			if err := rows.Scan(&d.ID, &d.TenantID, &d.Name, &d.Description, &stepsJSON, &d.CreatedBy, &d.CreatedAt, &d.UpdatedAt); err != nil {
				return err
			}
			_ = json.Unmarshal(stepsJSON, &d.Steps)
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// GetDefinition returns one definition.
func (r *Repository) GetDefinition(ctx context.Context, tenantID, id string) (*model.WorkflowDefinition, error) {
	d := &model.WorkflowDefinition{}
	var stepsJSON []byte
	var found bool
	err := r.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT id, tenant_id, name, description, steps, created_by, created_at, updated_at
			FROM workflow_definitions WHERE tenant_id = $1 AND id = $2`, tenantID, id).
			Scan(&d.ID, &d.TenantID, &d.Name, &d.Description, &stepsJSON, &d.CreatedBy, &d.CreatedAt, &d.UpdatedAt)
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
	_ = json.Unmarshal(stepsJSON, &d.Steps)
	return d, nil
}

// ---- Instances ------------------------------------------------------------

// CreateInstance persists a new workflow instance.
func (r *Repository) CreateInstance(ctx context.Context, inst *model.WorkflowInstance) error {
	return r.runTenant(ctx, inst.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO workflow_instances (id, tenant_id, definition_id, document_id, initiated_by, status, current_step, temporal_run_id, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		`, inst.ID, inst.TenantID, inst.DefinitionID, inst.DocumentID, inst.InitiatedBy,
			inst.Status, inst.CurrentStep, inst.TemporalRunID, inst.CreatedAt)
		return err
	})
}

// GetInstance returns one instance.
func (r *Repository) GetInstance(ctx context.Context, tenantID, id string) (*model.WorkflowInstance, error) {
	inst := &model.WorkflowInstance{}
	var found bool
	err := r.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT id, tenant_id, definition_id, document_id, initiated_by, status, current_step, temporal_run_id, created_at, completed_at
			FROM workflow_instances WHERE tenant_id = $1 AND id = $2`, tenantID, id).
			Scan(&inst.ID, &inst.TenantID, &inst.DefinitionID, &inst.DocumentID, &inst.InitiatedBy,
				&inst.Status, &inst.CurrentStep, &inst.TemporalRunID, &inst.CreatedAt, &inst.CompletedAt)
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
	return inst, nil
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
