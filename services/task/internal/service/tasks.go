// Task 4 (2026-07-28 task-service design) — core task CRUD: Create, Get,
// List, Update, Delete. Status transitions, assignee/document link
// management, comments, and the sweep land in Tasks 5-6.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/services/task/internal/model"
	"github.com/aieera/sedoc/services/task/internal/repository"
)

// CreateTaskInput is the service-layer shape for POST /tasks.
type CreateTaskInput struct {
	Title, Description, Priority, Source string
	DueAt                                *time.Time
	AssigneeIDs                          []uuid.UUID `json:"assignee_ids"`
	DocumentIDs                          []uuid.UUID `json:"document_ids"`
}

// UpdateTaskInput holds the patchable fields. All pointers; nil = no
// change. Status is mutated via the Task-5 transition methods, not here.
type UpdateTaskInput struct {
	ID                           uuid.UUID
	Title, Description, Priority *string
	DueAt                        *time.Time
	ClearDueAt                   bool // explicit "remove the due date" (DueAt nil + ClearDueAt true)
}

// ListTasksInput wraps repository.TaskFilters with the mine/created/all
// scoping the handler layer exposes as a single `filter` query param.
type ListTasksInput struct {
	Filter string // "mine" | "created" | "all" | ""
	repository.TaskFilters
}

// Page is the envelope every list endpoint in this service returns.
type Page[T any] struct {
	Items  []T `json:"items"`
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

// CreateTask validates in, inserts the task + its deduped assignees +
// its linked documents, records the `created` + one `assigned`-per-
// assignee activity rows, and emits dms.task.created.v1 +
// dms.task.assigned.v1 (per assignee) + dms.notify.task.assigned.v1 (per
// assignee other than the creator) — all inside one transaction, so a
// crash partway through leaves neither a half-created task nor an
// orphaned event.
func (s *TaskService) CreateTask(ctx context.Context, in CreateTaskInput) (*model.Task, error) {
	tenantID, userID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}

	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, validationErr("title is required")
	}
	if len(title) > 200 {
		return nil, validationErr("title must be at most 200 characters")
	}

	priority := in.Priority
	if priority == "" {
		priority = "normal"
	} else if !validPriority(priority) {
		return nil, validationErr("priority must be one of low|normal|high|urgent")
	}

	source := in.Source
	if source == "" {
		source = "user"
	} else if source != "user" && source != "workflow" {
		return nil, validationErr("source must be one of user|workflow")
	}

	// Dedup preserves input order and drops uuid.Nil — a zero-value
	// assignee/document id can only arrive via a caller bug (e.g. an
	// unmarshaled-but-unset field), not a real reference.
	assigneeIDs := dedupUUIDs(in.AssigneeIDs)
	documentIDs := dedupUUIDs(in.DocumentIDs)

	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("generate task id: %w", err)
	}
	now := time.Now().UTC()

	t := &model.Task{
		TenantID: tenantID, ID: id,
		Title: title, Description: in.Description,
		Status: "open", Priority: priority, Source: source,
		DueAt: in.DueAt, CreatedBy: userID,
		CreatedAt: now, UpdatedAt: now,
	}
	// Assignees are NOT pre-populated onto t here (unlike a bare repo-layer
	// Create call) — they're validated against `users` first, then added
	// one at a time below, so an unknown/cross-tenant id is caught before
	// any task_assignees row (or its activity/event/notify fallout) is
	// written, symmetric with how documents are validated via
	// LinkDocument just below.

	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		// Tasks.Create must run first: task_documents/task_assignees both
		// carry a FOREIGN KEY on (tenant_id, task_id) REFERENCES tasks, so
		// AddAssignee/LinkDocument below would fail closed on a
		// not-yet-existing task row.
		if err := s.Repos.Tasks.Create(ctx, tx, t); err != nil {
			return err
		}

		// Validate every assignee id resolves to a live user in this
		// tenant before writing any task_assignees row. There is
		// deliberately no FK from task_assignees.user_id to users (no
		// cross-service FK by design), so without this check a garbage or
		// cross-tenant uuid would silently insert, then fan out an
		// "assigned" activity row, a dms.task.assigned.v1 event, and a
		// dms.notify.task.assigned.v1 notification to nobody.
		if err := validateAssigneesExist(ctx, tx, tenantID, assigneeIDs); err != nil {
			return err
		}
		for _, aid := range assigneeIDs {
			// inserted is ignored here: aid always targets a task that was
			// just created in this same transaction, so it's always a real
			// insert (dedupUUIDs already removed any duplicate aid earlier).
			if _, err := s.Repos.Tasks.AddAssignee(ctx, tx, tenantID, id, aid, userID); err != nil {
				return err
			}
			t.Assignees = append(t.Assignees, model.TaskAssignee{UserID: aid, AddedBy: userID, AddedAt: now})
		}

		for _, docID := range documentIDs {
			// inserted is ignored for the same reason as AddAssignee above.
			d, _, err := s.Repos.Tasks.LinkDocument(ctx, tx, tenantID, id, docID, userID)
			if err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					return validationErr(fmt.Sprintf("document %s not found", docID))
				}
				return err
			}
			t.Documents = append(t.Documents, *d)
		}

		if err := s.Repos.Activity.Insert(ctx, tx, tenantID, &model.TaskActivity{
			TaskID: id, ActorID: userID, Action: "created", Detail: json.RawMessage(`{}`),
		}); err != nil {
			return err
		}

		for _, aid := range assigneeIDs {
			detail, err := json.Marshal(map[string]any{"user_id": aid.String()})
			if err != nil {
				return fmt.Errorf("marshal assigned activity detail: %w", err)
			}
			if err := s.Repos.Activity.Insert(ctx, tx, tenantID, &model.TaskActivity{
				TaskID: id, ActorID: userID, Action: "assigned", Detail: detail,
			}); err != nil {
				return err
			}
			if err := s.emitTaskEvent(ctx, tx, tenantID, id, "dms.task.assigned.v1", map[string]any{
				"task_id":     id.String(),
				"user_id":     aid.String(),
				"assigned_by": userID.String(),
			}); err != nil {
				return err
			}
		}

		if err := s.emitTaskEvent(ctx, tx, tenantID, id, "dms.task.created.v1", map[string]any{
			"task_id":      id.String(),
			"title":        t.Title,
			"status":       t.Status,
			"priority":     t.Priority,
			"source":       t.Source,
			"created_by":   userID.String(),
			"assignee_ids": uuidsToStrings(assigneeIDs),
			"document_ids": uuidsToStrings(documentIDs),
		}); err != nil {
			return err
		}

		// Notify every assignee except the creator — self-assignment on
		// create shouldn't ping the person who just did it.
		for _, aid := range assigneeIDs {
			if aid == userID {
				continue
			}
			body := t.Title
			if t.DueAt != nil {
				body += " (due " + t.DueAt.Format("2006-01-02") + ")"
			}
			if err := s.emitNotify(ctx, tx, tenantID, id, "assigned", "A task was assigned to you", body, []uuid.UUID{aid}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}

// GetTask is a plain tenant-scoped read: any authenticated tenant member
// may fetch any task (no per-row visibility gate — see the brief's
// "any tenant member can Get/List" note).
func (s *TaskService) GetTask(ctx context.Context, id uuid.UUID) (*model.Task, error) {
	tenantID, _, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var t *model.Task
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		got, err := s.Repos.Tasks.GetByID(ctx, tx, tenantID, id)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return notFoundErr("task not found")
			}
			return err
		}
		t = got
		return nil
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}

// ListTasks maps in.Filter onto the repository-level AssigneeID/
// CreatedBy predicates and returns one page. Limit/Offset are normalized
// with the same clamp the repository applies internally, so the returned
// Page always reports the values actually used (a caller passing Limit=0
// gets back Limit=50, not 0).
func (s *TaskService) ListTasks(ctx context.Context, in ListTasksInput) (*Page[model.Task], error) {
	tenantID, userID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}

	f := in.TaskFilters
	switch in.Filter {
	case "mine":
		f.AssigneeID = &userID
	case "created":
		f.CreatedBy = &userID
	case "all", "":
		// neither predicate — every task in the tenant (subject to the
		// filters already set on f).
	default:
		return nil, validationErr("filter must be one of mine|created|all")
	}
	f.Limit, f.Offset = normalizeLimitOffset(f.Limit, f.Offset)

	var items []model.Task
	var total int
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var err error
		items, total, err = s.Repos.Tasks.List(ctx, tx, tenantID, f)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &Page[model.Task]{Items: items, Total: total, Limit: f.Limit, Offset: f.Offset}, nil
}

// UpdateTask patches title/description/priority/due date. Gated by
// canEditFields (creator or admin) — being an assignee is not enough.
// Records one `updated` activity row naming the changed fields and emits
// dms.task.updated.v1; a no-op patch (nothing actually changed) skips
// both, so an idle PATCH doesn't spam the audit trail.
func (s *TaskService) UpdateTask(ctx context.Context, in UpdateTaskInput) (*model.Task, error) {
	tenantID, userID, role, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}

	var updated *model.Task
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		cur, err := s.Repos.Tasks.GetByID(ctx, tx, tenantID, in.ID)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return notFoundErr("task not found")
			}
			return err
		}
		if !canEditFields(cur, userID, role) {
			return forbiddenErr("only the creator or an admin may edit a task's fields")
		}

		var changed []string
		if in.Title != nil {
			title := strings.TrimSpace(*in.Title)
			if title == "" || len(title) > 200 {
				return validationErr("title must be 1..200 chars")
			}
			if title != cur.Title {
				cur.Title = title
				changed = append(changed, "title")
			}
		}
		if in.Description != nil && *in.Description != cur.Description {
			cur.Description = *in.Description
			changed = append(changed, "description")
		}
		if in.Priority != nil {
			if !validPriority(*in.Priority) {
				return validationErr("priority must be one of low|normal|high|urgent")
			}
			if *in.Priority != cur.Priority {
				cur.Priority = *in.Priority
				changed = append(changed, "priority")
			}
		}
		if in.DueAt != nil && (cur.DueAt == nil || !cur.DueAt.Equal(*in.DueAt)) {
			cur.DueAt = in.DueAt
			// Reset the sweep's single-shot flags on a real due-date change
			// so it gets another shot at reminding — without this, moving a
			// due date forward would silently suppress the next reminder
			// cycle (ported from the document service's UpdateTask). Only
			// do this when the value actually changed: a PATCH that
			// re-sends the current due_at must be a no-op, not a spurious
			// "updated" activity row that also resets reminder state and
			// re-arms a reminder the sweep already fired.
			cur.RemindedAt = nil
			cur.OverdueNotifiedAt = nil
			changed = append(changed, "due_at")
		} else if in.ClearDueAt && cur.DueAt != nil {
			cur.DueAt = nil
			cur.RemindedAt = nil
			cur.OverdueNotifiedAt = nil
			changed = append(changed, "due_at")
		}

		if len(changed) == 0 {
			updated = cur
			return nil
		}

		if err := s.Repos.Tasks.Update(ctx, tx, cur); err != nil {
			return err
		}
		detail, err := json.Marshal(map[string]any{"fields": changed})
		if err != nil {
			return fmt.Errorf("marshal updated activity detail: %w", err)
		}
		if err := s.Repos.Activity.Insert(ctx, tx, tenantID, &model.TaskActivity{
			TaskID: cur.ID, ActorID: userID, Action: "updated", Detail: detail,
		}); err != nil {
			return err
		}
		if err := s.emitTaskEvent(ctx, tx, tenantID, cur.ID, "dms.task.updated.v1", map[string]any{
			"task_id":    cur.ID.String(),
			"updated_by": userID.String(),
			"fields":     changed,
		}); err != nil {
			return err
		}
		updated = cur
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// DeleteTask soft-deletes a task. Gated by canEditFields (creator or
// admin), matching the document service's "delete = creator or admin"
// rule. Records a `deleted` activity row and emits dms.task.deleted.v1.
func (s *TaskService) DeleteTask(ctx context.Context, id uuid.UUID) error {
	tenantID, userID, role, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		cur, err := s.Repos.Tasks.GetByID(ctx, tx, tenantID, id)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return notFoundErr("task not found")
			}
			return err
		}
		if !canEditFields(cur, userID, role) {
			return forbiddenErr("only the creator or an admin may delete a task")
		}
		if err := s.Repos.Tasks.SoftDelete(ctx, tx, tenantID, id); err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return notFoundErr("task not found")
			}
			return err
		}
		if err := s.Repos.Activity.Insert(ctx, tx, tenantID, &model.TaskActivity{
			TaskID: id, ActorID: userID, Action: "deleted", Detail: json.RawMessage(`{}`),
		}); err != nil {
			return err
		}
		return s.emitTaskEvent(ctx, tx, tenantID, id, "dms.task.deleted.v1", map[string]any{
			"task_id":    id.String(),
			"deleted_by": userID.String(),
		})
	})
}

// ---- tx-scoped validation helpers -----------------------------------------

// validateAssigneesExist confirms every id in assigneeIDs is a live user
// row for tenantID, in a single SELECT ... = ANY($2) query inside tx.
// There is deliberately no FOREIGN KEY from task_assignees.user_id to
// users (cross-service FK stays out by design — the task and document
// services own their schemas independently even though they currently
// share one database), so nothing at the SQL level would otherwise catch
// a garbage or cross-tenant user id before it lands in task_assignees
// and fans out an assigned activity row / event / notification to no
// one. No-ops on an empty slice.
func validateAssigneesExist(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, assigneeIDs []uuid.UUID) error {
	if len(assigneeIDs) == 0 {
		return nil
	}
	// deleted_at IS NULL matches the document-link check, which also
	// rejects soft-deleted targets: assigning a departed user would keep
	// sending them due-soon and completion notifications forever, since
	// task_assignees deliberately carries no cross-service FK to users.
	rows, err := tx.Query(ctx, `SELECT id FROM users WHERE tenant_id = $1 AND id = ANY($2) AND deleted_at IS NULL`, tenantID, assigneeIDs)
	if err != nil {
		return fmt.Errorf("validate assignee ids: %w", err)
	}
	defer rows.Close()

	found := make(map[uuid.UUID]bool, len(assigneeIDs))
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("validate assignee ids: %w", err)
		}
		found[id] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("validate assignee ids: %w", err)
	}

	var missing []string
	for _, id := range assigneeIDs {
		if !found[id] {
			missing = append(missing, id.String())
		}
	}
	if len(missing) > 0 {
		return validationErr(fmt.Sprintf("unknown assignee id(s): %s", strings.Join(missing, ", ")))
	}
	return nil
}

// ---- small pure helpers --------------------------------------------------

func validPriority(p string) bool {
	switch p {
	case "low", "normal", "high", "urgent":
		return true
	}
	return false
}

// dedupUUIDs returns ids with duplicates and uuid.Nil removed,
// preserving first-seen order.
func dedupUUIDs(ids []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]bool, len(ids))
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if id == uuid.Nil || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func uuidsToStrings(ids []uuid.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

// normalizeLimitOffset mirrors the repository's internal List clamp
// (TaskRepository.List: limit default 50, max 100; offset floor 0) so
// the Page envelope this service returns always reflects the values
// actually applied to the query, not whatever the caller passed in.
func normalizeLimitOffset(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}
