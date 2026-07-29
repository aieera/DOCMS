// Task-3 (2026-07-28 task-service design) — the `tasks` aggregate repo.
// This adopts and extends the document service's ADR-0068 lightweight
// tasks repo: same table, same sweep semantics, but the single
// assignee_id / linked_document_id columns are replaced by the
// task_assignees / task_documents side tables added in migration 000002,
// so a task can carry multiple assignees and multiple linked documents.
package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/services/task/internal/model"
)

type taskRepo struct{}

// NewTaskRepo returns the default Postgres-backed TaskRepository impl.
func NewTaskRepo() TaskRepository { return &taskRepo{} }

// taskColumns/taskColumnsT are the same seventeen `tasks` columns the
// model.Task struct maps onto (everything except the legacy
// assignee_id/linked_document_id/linked_workflow_instance_id columns,
// which this service's model no longer reads or writes), in the order
// scanTask expects. taskColumnsT is the `t`-aliased form used by List's
// page query, which also joins against task_assignees/task_documents in
// its WHERE clause.
const taskColumns = `tenant_id, id, title, COALESCE(description,''), status, priority,
	due_at, completed_at, deleted_at, source, created_by, completed_by,
	reminded_at, overdue_notified_at, created_at, updated_at`

const taskColumnsT = `t.tenant_id, t.id, t.title, COALESCE(t.description,''), t.status, t.priority,
	t.due_at, t.completed_at, t.deleted_at, t.source, t.created_by, t.completed_by,
	t.reminded_at, t.overdue_notified_at, t.created_at, t.updated_at`

func (r *taskRepo) Create(ctx context.Context, tx pgx.Tx, t *model.Task) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO tasks (
			tenant_id, id, title, description, status, priority, due_at,
			source, created_by, completed_by, completed_at,
			reminded_at, overdue_notified_at, created_at, updated_at
		) VALUES (
			$1, $2, $3, NULLIF($4, ''), $5, $6, $7,
			$8, $9, $10, $11,
			$12, $13, $14, $15
		)`,
		t.TenantID, t.ID, t.Title, t.Description, t.Status, t.Priority, t.DueAt,
		t.Source, t.CreatedBy, t.CompletedBy, t.CompletedAt,
		t.RemindedAt, t.OverdueNotifiedAt, t.CreatedAt, t.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert task: %w", err)
	}

	for _, a := range t.Assignees {
		addedAt := a.AddedAt
		if addedAt.IsZero() {
			addedAt = t.CreatedAt
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO task_assignees (tenant_id, task_id, user_id, added_by, added_at)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (tenant_id, task_id, user_id) DO NOTHING`,
			t.TenantID, t.ID, a.UserID, a.AddedBy, addedAt); err != nil {
			return fmt.Errorf("insert task_assignees: %w", err)
		}
	}

	for _, d := range t.Documents {
		linkedAt := d.LinkedAt
		if linkedAt.IsZero() {
			linkedAt = t.CreatedAt
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO task_documents (tenant_id, task_id, document_id, workspace_id, title_snapshot, linked_by, linked_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (tenant_id, task_id, document_id) DO NOTHING`,
			t.TenantID, t.ID, d.DocumentID, d.WorkspaceID, d.TitleSnapshot, d.LinkedBy, linkedAt); err != nil {
			return fmt.Errorf("insert task_documents: %w", err)
		}
	}

	return nil
}

func (r *taskRepo) GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.Task, error) {
	row := tx.QueryRow(ctx, `SELECT `+taskColumns+`
		  FROM tasks WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`, tenantID, id)
	t, err := scanTask(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	tasks, err := hydrateTasks(ctx, tx, tenantID, []model.Task{*t})
	if err != nil {
		return nil, err
	}
	return &tasks[0], nil
}

func (r *taskRepo) List(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, f TaskFilters) ([]model.Task, int, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}

	where, args := buildTaskWhere(tenantID, f)

	var total int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM tasks t WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count tasks: %w", err)
	}
	if total == 0 {
		return nil, 0, nil
	}

	pageArgs := append(append([]any{}, args...), limit, offset)
	q := `SELECT ` + taskColumnsT + ` FROM tasks t WHERE ` + where + ` ` + taskOrderClause(f.Sort) +
		fmt.Sprintf(` LIMIT $%d OFFSET $%d`, len(pageArgs)-1, len(pageArgs))

	rows, err := tx.Query(ctx, q, pageArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()

	var out []model.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if len(out) == 0 {
		return out, total, nil
	}

	out, err = hydrateTasks(ctx, tx, tenantID, out)
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// buildTaskWhere builds the shared WHERE clause (against the `t` alias)
// used by both List's count query and its page query, so the total always
// matches the filter actually applied to the page.
func buildTaskWhere(tenantID uuid.UUID, f TaskFilters) (string, []any) {
	args := []any{tenantID}
	var b strings.Builder
	b.WriteString(`t.tenant_id = $1 AND t.deleted_at IS NULL`)

	if f.AssigneeID != nil {
		args = append(args, *f.AssigneeID)
		fmt.Fprintf(&b, ` AND EXISTS (SELECT 1 FROM task_assignees a WHERE a.tenant_id = t.tenant_id AND a.task_id = t.id AND a.user_id = $%d)`, len(args))
	}
	if f.CreatedBy != nil {
		args = append(args, *f.CreatedBy)
		fmt.Fprintf(&b, ` AND t.created_by = $%d`, len(args))
	}
	if f.DocumentID != nil {
		args = append(args, *f.DocumentID)
		fmt.Fprintf(&b, ` AND EXISTS (SELECT 1 FROM task_documents d WHERE d.tenant_id = t.tenant_id AND d.task_id = t.id AND d.document_id = $%d)`, len(args))
	}
	if len(f.Statuses) > 0 {
		args = append(args, f.Statuses)
		fmt.Fprintf(&b, ` AND t.status = ANY($%d)`, len(args))
	} else if !f.IncludeCompleted {
		b.WriteString(` AND t.status NOT IN ('done','cancelled')`)
	}
	if f.Priority != "" {
		args = append(args, f.Priority)
		fmt.Fprintf(&b, ` AND t.priority = $%d`, len(args))
	}
	if f.Query != "" {
		args = append(args, "%"+f.Query+"%")
		fmt.Fprintf(&b, ` AND (t.title ILIKE $%d OR t.description ILIKE $%d)`, len(args), len(args))
	}
	return b.String(), args
}

// taskOrderClause mirrors the ordering semantics of the document service's
// legacy tasks_repo.go List (L147-149): due-first (NULLs last), then
// priority weight (urgent > high > normal > low), then newest. Sort
// picks which of those three keys leads; the other two remain as
// tiebreakers so the ordering stays fully deterministic regardless of
// Sort.
func taskOrderClause(sort string) string {
	const dueFirst = `(t.due_at IS NULL), t.due_at ASC`
	const priorityWeight = `CASE t.priority WHEN 'urgent' THEN 4 WHEN 'high' THEN 3 WHEN 'normal' THEN 2 ELSE 1 END DESC`
	const newestFirst = `t.created_at DESC`

	switch sort {
	case "priority":
		return `ORDER BY ` + priorityWeight + `, ` + dueFirst + `, ` + newestFirst
	case "created_at":
		return `ORDER BY ` + newestFirst
	default: // "due_at", ""
		return `ORDER BY ` + dueFirst + `, ` + priorityWeight + `, ` + newestFirst
	}
}

func (r *taskRepo) Update(ctx context.Context, tx pgx.Tx, t *model.Task) error {
	tag, err := tx.Exec(ctx, `
		UPDATE tasks SET
			title = $3, description = NULLIF($4, ''),
			status = $5, priority = $6, due_at = $7,
			completed_by = $8, completed_at = $9,
			reminded_at = $10, overdue_notified_at = $11,
			updated_at = now()
		 WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`,
		t.TenantID, t.ID, t.Title, t.Description,
		t.Status, t.Priority, t.DueAt,
		t.CompletedBy, t.CompletedAt,
		t.RemindedAt, t.OverdueNotifiedAt)
	if err != nil {
		return fmt.Errorf("update task: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *taskRepo) SoftDelete(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error {
	tag, err := tx.Exec(ctx, `
		UPDATE tasks SET deleted_at = now(), updated_at = now()
		 WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`, tenantID, id)
	if err != nil {
		return fmt.Errorf("soft delete task: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AddAssignee reports whether a row was actually inserted (false on a
// pre-existing (tenant, task, user) pair, via ON CONFLICT DO NOTHING +
// RowsAffected) — mirrors RemoveAssignee's already-race-safe pattern, so
// the service layer can decide idempotency (activity/event/notify) from
// the database's actual outcome instead of a pre-fetched snapshot that a
// concurrent duplicate call could equally have read as "not yet
// assigned".
func (r *taskRepo) AddAssignee(ctx context.Context, tx pgx.Tx, tenantID, taskID, userID, addedBy uuid.UUID) (bool, error) {
	tag, err := tx.Exec(ctx, `
		INSERT INTO task_assignees (tenant_id, task_id, user_id, added_by)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (tenant_id, task_id, user_id) DO NOTHING`,
		tenantID, taskID, userID, addedBy)
	if err != nil {
		return false, fmt.Errorf("insert task_assignees: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (r *taskRepo) RemoveAssignee(ctx context.Context, tx pgx.Tx, tenantID, taskID, userID uuid.UUID) (bool, error) {
	tag, err := tx.Exec(ctx, `
		DELETE FROM task_assignees WHERE tenant_id = $1 AND task_id = $2 AND user_id = $3`,
		tenantID, taskID, userID)
	if err != nil {
		return false, fmt.Errorf("delete task_assignees: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// LinkDocument resolves the document, then upserts task_documents
// (ON CONFLICT DO UPDATE, so a relink always refreshes the workspace/
// title snapshot even when the row already existed). The second return
// value reports whether the row was newly inserted rather than an
// existing row being updated by the conflict, via the
// `(xmax = 0) AS inserted` Postgres idiom: a freshly inserted row's
// xmax is 0 (no deleting/updating transaction yet), while a row that
// hit the ON CONFLICT DO UPDATE path gets a non-zero xmax set by that
// same UPDATE. This lets the service layer decide idempotency
// (activity/event) from the database's actual outcome rather than a
// pre-fetched "was it already linked" snapshot, which a concurrent
// duplicate call could equally have read as "not yet linked".
func (r *taskRepo) LinkDocument(ctx context.Context, tx pgx.Tx, tenantID, taskID, documentID, linkedBy uuid.UUID) (*model.TaskDocument, bool, error) {
	var workspaceID uuid.UUID
	var title string
	err := tx.QueryRow(ctx, `
		SELECT workspace_id, title FROM documents WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`,
		tenantID, documentID).Scan(&workspaceID, &title)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, ErrNotFound
		}
		return nil, false, fmt.Errorf("resolve document: %w", err)
	}

	var d model.TaskDocument
	var inserted bool
	err = tx.QueryRow(ctx, `
		INSERT INTO task_documents (tenant_id, task_id, document_id, workspace_id, title_snapshot, linked_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (tenant_id, task_id, document_id)
		DO UPDATE SET workspace_id = EXCLUDED.workspace_id, title_snapshot = EXCLUDED.title_snapshot
		RETURNING document_id, workspace_id, title_snapshot, linked_by, linked_at, (xmax = 0) AS inserted`,
		tenantID, taskID, documentID, workspaceID, title, linkedBy).Scan(
		&d.DocumentID, &d.WorkspaceID, &d.TitleSnapshot, &d.LinkedBy, &d.LinkedAt, &inserted)
	if err != nil {
		return nil, false, fmt.Errorf("insert task_documents: %w", err)
	}
	return &d, inserted, nil
}

func (r *taskRepo) UnlinkDocument(ctx context.Context, tx pgx.Tx, tenantID, taskID, documentID uuid.UUID) (bool, error) {
	tag, err := tx.Exec(ctx, `
		DELETE FROM task_documents WHERE tenant_id = $1 AND task_id = $2 AND document_id = $3`,
		tenantID, taskID, documentID)
	if err != nil {
		return false, fmt.Errorf("delete task_documents: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ClaimDueSoon stamps `reminded_at = now()` on every open/in-progress,
// non-deleted task whose due_at falls within 24h and which hasn't been
// reminded yet. Ported from the document service's legacy
// tasks_repo.go:211, with `AND deleted_at IS NULL` added since that column
// didn't exist on the table this query was originally written against.
func (r *taskRepo) ClaimDueSoon(ctx context.Context, tx pgx.Tx, now time.Time) ([]model.Task, error) {
	rows, err := tx.Query(ctx, `
		UPDATE tasks SET reminded_at = $1, updated_at = $1
		 WHERE status IN ('open','in_progress')
		   AND due_at IS NOT NULL
		   AND due_at >= $1 AND due_at <= $1 + interval '24 hours'
		   AND reminded_at IS NULL
		   AND deleted_at IS NULL
		 RETURNING `+taskColumns, now)
	if err != nil {
		return nil, fmt.Errorf("claim due-soon tasks: %w", err)
	}
	return scanTaskRows(rows)
}

// ClaimOverdue stamps `overdue_notified_at = now()` on every
// open/in-progress, non-deleted task whose due_at has already passed and
// which hasn't been overdue-pinged yet. Ported from the document
// service's legacy tasks_repo.go:237.
func (r *taskRepo) ClaimOverdue(ctx context.Context, tx pgx.Tx, now time.Time) ([]model.Task, error) {
	rows, err := tx.Query(ctx, `
		UPDATE tasks SET overdue_notified_at = $1, updated_at = $1
		 WHERE status IN ('open','in_progress')
		   AND due_at IS NOT NULL
		   AND due_at < $1
		   AND overdue_notified_at IS NULL
		   AND deleted_at IS NULL
		 RETURNING `+taskColumns, now)
	if err != nil {
		return nil, fmt.Errorf("claim overdue tasks: %w", err)
	}
	return scanTaskRows(rows)
}

// ---- helpers -----------------------------------------------------------

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTask(row rowScanner) (*model.Task, error) {
	var t model.Task
	if err := row.Scan(
		&t.TenantID, &t.ID, &t.Title, &t.Description, &t.Status, &t.Priority,
		&t.DueAt, &t.CompletedAt, &t.DeletedAt, &t.Source, &t.CreatedBy, &t.CompletedBy,
		&t.RemindedAt, &t.OverdueNotifiedAt, &t.CreatedAt, &t.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &t, nil
}

func scanTaskRows(rows pgx.Rows) ([]model.Task, error) {
	defer rows.Close()
	var out []model.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// hydrateTasks fills in Assignees/Documents for tasks via the two-query
// approach: one SELECT ... WHERE task_id = ANY($ids) per side table,
// stitched onto the matching task in Go. Callers must not call this with
// an empty slice (List already skips it when the page is empty).
func hydrateTasks(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, tasks []model.Task) ([]model.Task, error) {
	ids := make([]uuid.UUID, len(tasks))
	idx := make(map[uuid.UUID]int, len(tasks))
	for i := range tasks {
		ids[i] = tasks[i].ID
		idx[tasks[i].ID] = i
	}

	arows, err := tx.Query(ctx, `
		SELECT task_id, user_id, added_by, added_at
		  FROM task_assignees WHERE tenant_id = $1 AND task_id = ANY($2)
		 ORDER BY added_at`, tenantID, ids)
	if err != nil {
		return nil, fmt.Errorf("list task_assignees: %w", err)
	}
	for arows.Next() {
		var taskID uuid.UUID
		var a model.TaskAssignee
		if err := arows.Scan(&taskID, &a.UserID, &a.AddedBy, &a.AddedAt); err != nil {
			arows.Close()
			return nil, err
		}
		if i, ok := idx[taskID]; ok {
			tasks[i].Assignees = append(tasks[i].Assignees, a)
		}
	}
	if err := arows.Err(); err != nil {
		arows.Close()
		return nil, err
	}
	arows.Close()

	drows, err := tx.Query(ctx, `
		SELECT task_id, document_id, workspace_id, title_snapshot, linked_by, linked_at
		  FROM task_documents WHERE tenant_id = $1 AND task_id = ANY($2)
		 ORDER BY linked_at`, tenantID, ids)
	if err != nil {
		return nil, fmt.Errorf("list task_documents: %w", err)
	}
	for drows.Next() {
		var taskID uuid.UUID
		var d model.TaskDocument
		if err := drows.Scan(&taskID, &d.DocumentID, &d.WorkspaceID, &d.TitleSnapshot, &d.LinkedBy, &d.LinkedAt); err != nil {
			drows.Close()
			return nil, err
		}
		if i, ok := idx[taskID]; ok {
			tasks[i].Documents = append(tasks[i].Documents, d)
		}
	}
	if err := drows.Err(); err != nil {
		drows.Close()
		return nil, err
	}
	drows.Close()

	return tasks, nil
}
