// ADR 0068 — lightweight task service.
//
// Three notification subjects emitted via the document service's
// outbox (consumed by the notification service for email + audit):
//
//   dms.notify.task.assigned.v1   — inline on assign
//   dms.notify.task.due_soon.v1   — hourly sweep
//   dms.notify.task.overdue.v1    — hourly sweep
//
// Permission model:
//   - Create: any authenticated user
//   - Assign / unassign / complete: creator OR current assignee OR admin
//   - Edit other fields: creator OR admin
//   - Delete: creator OR admin
//
// All payloads match notification.model.DeliveryPayload so the
// notification consumer doesn't drop them (mirrors the comment-
// mention shape from ADR 0066).
package service

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/document/internal/model"
	"github.com/vaultdms/vaultdms/services/document/internal/repository"
)

// CreateTaskInput is the service-layer shape for POST /tasks.
type CreateTaskInput struct {
	Title                    string
	Description              string
	Priority                 string
	DueAt                    *time.Time
	AssigneeID               *uuid.UUID
	LinkedDocumentID         *uuid.UUID
	LinkedWorkflowInstanceID *uuid.UUID
	Source                   string // "user" or "workflow"
}

// UpdateTaskInput holds the patchable fields. All pointers; nil =
// no change. Status is mutated via Complete/Reopen, not here.
type UpdateTaskInput struct {
	ID          uuid.UUID
	Title       *string
	Description *string
	Priority    *string
	DueAt       *time.Time
	ClearDueAt  bool // explicit "remove the due date" flag (DueAt nil + ClearDueAt true)
}

// CreateTask persists a row + emits the assigned notification when
// AssigneeID is non-nil.
func (s *DocumentService) CreateTask(ctx context.Context, in CreateTaskInput) (*repository.Task, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Title) == "" {
		return nil, vdmserr.Validation("title", "required")
	}
	if len(in.Title) > 200 {
		return nil, vdmserr.Validation("title", "max 200 chars")
	}
	if !validTaskPriority(in.Priority) {
		in.Priority = "normal"
	}
	if in.Source != "user" && in.Source != "workflow" {
		in.Source = "user"
	}

	id, err := newExternalID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	t := &repository.Task{
		TenantID: tenantID, ID: id,
		Title: strings.TrimSpace(in.Title), Description: in.Description,
		Status: "open", Priority: in.Priority,
		DueAt: in.DueAt, AssigneeID: in.AssigneeID,
		LinkedDocumentID: in.LinkedDocumentID, LinkedWorkflowInstanceID: in.LinkedWorkflowInstanceID,
		Source: in.Source, CreatedBy: userID,
		CreatedAt: now, UpdatedAt: now,
	}

	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if err := s.repos.Tasks.Create(ctx, tx, t); err != nil {
			return err
		}
		if t.AssigneeID != nil && *t.AssigneeID != userID {
			return s.emitTaskAssigned(ctx, tx, t, userID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}

// GetTask is the read used by handlers.
func (s *DocumentService) GetTask(ctx context.Context, id uuid.UUID) (*repository.Task, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var t *repository.Task
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		got, err := s.repos.Tasks.GetByID(ctx, tx, tenantID, id)
		if err != nil {
			return err
		}
		t = got
		return nil
	})
	return t, err
}

// ListTasks supports the inbox + doc-scope + admin queries.
func (s *DocumentService) ListTasks(ctx context.Context, f repository.TaskFilters) ([]repository.Task, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out []repository.Task
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		out, err = s.repos.Tasks.List(ctx, tx, tenantID, f)
		return err
	})
	return out, err
}

// UpdateTask patches title/description/priority/due_at. Status
// changes go through CompleteTask / ReopenTask / CancelTask.
func (s *DocumentService) UpdateTask(ctx context.Context, in UpdateTaskInput) (*repository.Task, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	role := callerRole(ctx)

	var updated *repository.Task
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		cur, err := s.repos.Tasks.GetByID(ctx, tx, tenantID, in.ID)
		if err != nil {
			return err
		}
		if !canEditTaskFields(cur, userID, role) {
			return vdmserr.Forbidden("only the creator or an admin may edit a task's fields")
		}
		if in.Title != nil {
			t := strings.TrimSpace(*in.Title)
			if t == "" || len(t) > 200 {
				return vdmserr.Validation("title", "1..200 chars")
			}
			cur.Title = t
		}
		if in.Description != nil {
			cur.Description = *in.Description
		}
		if in.Priority != nil {
			if !validTaskPriority(*in.Priority) {
				return vdmserr.Validation("priority", "must be low|normal|high|urgent")
			}
			cur.Priority = *in.Priority
		}
		if in.DueAt != nil {
			cur.DueAt = in.DueAt
			// Reset reminder flags on a due-date change so the
			// sweep gets another shot. Without this, moving a due
			// date forward would suppress the new reminder cycle.
			cur.RemindedAt = nil
			cur.OverdueNotifiedAt = nil
		} else if in.ClearDueAt {
			cur.DueAt = nil
			cur.RemindedAt = nil
			cur.OverdueNotifiedAt = nil
		}
		if err := s.repos.Tasks.Update(ctx, tx, cur); err != nil {
			return err
		}
		updated = cur
		return nil
	})
	return updated, err
}

// AssignTask sets the assignee + emits the assigned notification.
// Idempotent: assigning to the same user is a no-op without an
// extra notification.
func (s *DocumentService) AssignTask(ctx context.Context, taskID, newAssignee uuid.UUID) (*repository.Task, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	role := callerRole(ctx)

	var out *repository.Task
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		cur, err := s.repos.Tasks.GetByID(ctx, tx, tenantID, taskID)
		if err != nil {
			return err
		}
		if !canChangeAssignment(cur, userID, role) {
			return vdmserr.Forbidden("only the creator, current assignee, or an admin may assign")
		}
		// No-op fast path.
		if cur.AssigneeID != nil && *cur.AssigneeID == newAssignee {
			out = cur
			return nil
		}
		cur.AssigneeID = &newAssignee
		if err := s.repos.Tasks.Update(ctx, tx, cur); err != nil {
			return err
		}
		out = cur
		// Don't ping the user who just assigned the task to themselves.
		if newAssignee != userID {
			return s.emitTaskAssigned(ctx, tx, cur, userID)
		}
		return nil
	})
	return out, err
}

// UnassignTask clears the assignee. No notification — silent.
func (s *DocumentService) UnassignTask(ctx context.Context, taskID uuid.UUID) (*repository.Task, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	role := callerRole(ctx)

	var out *repository.Task
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		cur, err := s.repos.Tasks.GetByID(ctx, tx, tenantID, taskID)
		if err != nil {
			return err
		}
		if !canChangeAssignment(cur, userID, role) {
			return vdmserr.Forbidden("not allowed")
		}
		cur.AssigneeID = nil
		if err := s.repos.Tasks.Update(ctx, tx, cur); err != nil {
			return err
		}
		out = cur
		return nil
	})
	return out, err
}

// CompleteTask marks done + stamps completed_by/at.
func (s *DocumentService) CompleteTask(ctx context.Context, taskID uuid.UUID) (*repository.Task, error) {
	return s.transitionTaskStatus(ctx, taskID, "done")
}

// ReopenTask flips done|cancelled back to open. Reminder flags stay
// where they are — re-opening doesn't reset the nag cycle on
// purpose (avoids an immediate ping flood when a stale task is
// re-opened in bulk).
func (s *DocumentService) ReopenTask(ctx context.Context, taskID uuid.UUID) (*repository.Task, error) {
	return s.transitionTaskStatus(ctx, taskID, "open")
}

// CancelTask is the soft-decline path.
func (s *DocumentService) CancelTask(ctx context.Context, taskID uuid.UUID) (*repository.Task, error) {
	return s.transitionTaskStatus(ctx, taskID, "cancelled")
}

func (s *DocumentService) transitionTaskStatus(ctx context.Context, taskID uuid.UUID, target string) (*repository.Task, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	role := callerRole(ctx)

	var out *repository.Task
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		cur, err := s.repos.Tasks.GetByID(ctx, tx, tenantID, taskID)
		if err != nil {
			return err
		}
		if !canChangeAssignment(cur, userID, role) {
			return vdmserr.Forbidden("not allowed")
		}
		now := time.Now().UTC()
		cur.Status = target
		if target == "done" {
			cur.CompletedAt = &now
			cur.CompletedBy = &userID
		} else {
			cur.CompletedAt = nil
			cur.CompletedBy = nil
		}
		if err := s.repos.Tasks.Update(ctx, tx, cur); err != nil {
			return err
		}
		out = cur
		return nil
	})
	return out, err
}

// DeleteTask hard-deletes a task. Creator or admin only.
func (s *DocumentService) DeleteTask(ctx context.Context, taskID uuid.UUID) error {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	role := callerRole(ctx)
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		cur, err := s.repos.Tasks.GetByID(ctx, tx, tenantID, taskID)
		if err != nil {
			return err
		}
		if !canEditTaskFields(cur, userID, role) {
			return vdmserr.Forbidden("only the creator or an admin may delete")
		}
		return s.repos.Tasks.Delete(ctx, tx, tenantID, taskID)
	})
}

// SweepTaskNotifications scans for due-soon and overdue tasks and
// emits one notify event per claimed row. Called by the hourly
// ticker in cmd/server/main.go.
//
// The repo's Claim* uses an UPDATE…RETURNING which atomically flips
// the per-row flag and hands back the row — so each task gets at
// most one ping per state crossing even if multiple sweepers run.
func (s *DocumentService) SweepTaskNotifications(ctx context.Context) error {
	now := time.Now().UTC()
	// We scan the whole tenancy in a single tx; RLS forces the
	// caller to be tenant-scoped so we use a special "system"
	// path here that bypasses the per-tenant guard. The repo
	// runs raw SQL; the policy on `tasks` requires
	// `current_setting('app.current_tenant')`, so the sweep
	// iterates known tenants.
	tenants, err := s.listAllTenantsForSweep(ctx)
	if err != nil {
		return err
	}
	for _, tenantID := range tenants {
		_ = s.sweepOne(ctx, tenantID, now)
	}
	return nil
}

func (s *DocumentService) sweepOne(ctx context.Context, tenantID uuid.UUID, now time.Time) error {
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		dueSoon, err := s.repos.Tasks.ClaimDueSoon(ctx, tx, now)
		if err != nil {
			return err
		}
		for i := range dueSoon {
			_ = s.emitTaskDueSoon(ctx, tx, &dueSoon[i])
		}
		overdue, err := s.repos.Tasks.ClaimOverdue(ctx, tx, now)
		if err != nil {
			return err
		}
		for i := range overdue {
			_ = s.emitTaskOverdue(ctx, tx, &overdue[i])
		}
		return nil
	})
}

// listAllTenantsForSweep reads every active org id outside any
// tenant tx. Acceptable because organizations doesn't have a
// per-tenant RLS policy that depends on current_setting.
func (s *DocumentService) listAllTenantsForSweep(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := s.repos.Pool.Query(ctx, `SELECT id FROM organizations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ---- notification emitters ---------------------------------------------

func (s *DocumentService) emitTaskAssigned(ctx context.Context, tx pgx.Tx, t *repository.Task, byUserID uuid.UUID) error {
	if t.AssigneeID == nil {
		return nil
	}
	body := t.Title
	if t.DueAt != nil {
		body += " (due " + t.DueAt.Format("2006-01-02") + ")"
	}
	evt, err := model.NewOutboxEvent(t.TenantID, "dms.notify.task.assigned.v1", "task", t.ID, map[string]any{
		"tenant_id":     t.TenantID.String(),
		"user_ids":      []string{t.AssigneeID.String()},
		"type":          "task.assigned",
		"title":         "A task was assigned to you",
		"body":          body,
		"resource_type": "task",
		"resource_id":   t.ID.String(),
		"by_user_id":    byUserID.String(),
		"at":            time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	return s.repos.Outbox.Insert(ctx, tx, evt)
}

func (s *DocumentService) emitTaskDueSoon(ctx context.Context, tx pgx.Tx, t *repository.Task) error {
	if t.AssigneeID == nil {
		return nil
	}
	due := ""
	if t.DueAt != nil {
		due = t.DueAt.Format(time.RFC3339)
	}
	evt, err := model.NewOutboxEvent(t.TenantID, "dms.notify.task.due_soon.v1", "task", t.ID, map[string]any{
		"tenant_id":     t.TenantID.String(),
		"user_ids":      []string{t.AssigneeID.String()},
		"type":          "task.due_soon",
		"title":         "Task due within 24 hours",
		"body":          t.Title + " — due " + due,
		"resource_type": "task",
		"resource_id":   t.ID.String(),
		"at":            time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	return s.repos.Outbox.Insert(ctx, tx, evt)
}

func (s *DocumentService) emitTaskOverdue(ctx context.Context, tx pgx.Tx, t *repository.Task) error {
	recipients := []string{}
	if t.AssigneeID != nil {
		recipients = append(recipients, t.AssigneeID.String())
	}
	// Creator is also notified on overdue per ADR 0068.
	if t.CreatedBy != uuid.Nil {
		// Avoid a duplicate when creator == assignee.
		dupe := false
		for _, r := range recipients {
			if r == t.CreatedBy.String() {
				dupe = true
				break
			}
		}
		if !dupe {
			recipients = append(recipients, t.CreatedBy.String())
		}
	}
	if len(recipients) == 0 {
		return nil
	}
	evt, err := model.NewOutboxEvent(t.TenantID, "dms.notify.task.overdue.v1", "task", t.ID, map[string]any{
		"tenant_id":     t.TenantID.String(),
		"user_ids":      recipients,
		"type":          "task.overdue",
		"title":         "Task is overdue",
		"body":          t.Title,
		"resource_type": "task",
		"resource_id":   t.ID.String(),
		"at":            time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	return s.repos.Outbox.Insert(ctx, tx, evt)
}

// ---- permission helpers ------------------------------------------------

func canEditTaskFields(t *repository.Task, userID uuid.UUID, role string) bool {
	return t.CreatedBy == userID || role == "admin" || role == "owner"
}

func canChangeAssignment(t *repository.Task, userID uuid.UUID, role string) bool {
	if t.CreatedBy == userID {
		return true
	}
	if t.AssigneeID != nil && *t.AssigneeID == userID {
		return true
	}
	return role == "admin" || role == "owner"
}

func validTaskPriority(p string) bool {
	switch p {
	case "low", "normal", "high", "urgent":
		return true
	}
	return false
}
