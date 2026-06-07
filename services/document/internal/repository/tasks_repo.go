// ADR 0068 — lightweight tasks repo. Distinct from `workflow_tasks`
// (which is workflow-driven approval steps); this table is for
// human + workflow-generated to-do items.
package repository

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
)

// Task is the row shape used by the service + handler.
//
// JSON tags are explicit because the handler returns this struct directly
// (no DTO transform); without them Go's encoder defaulted to PascalCase
// field names — `Title`, `ID`, etc. — while the React frontend reads
// snake_case (`task.title`, `task.id`). Without the tags every field
// was `undefined` on the client: rows rendered as empty badge
// placeholders and `key={t.id}` collapsed to `key={undefined}` for
// every row, producing the "duplicate key" warning + the blank Tasks
// table seen on the 2026-05-26 inbox.
type Task struct {
	TenantID                 uuid.UUID  `json:"tenant_id"`
	ID                       uuid.UUID  `json:"id"`
	Title                    string     `json:"title"`
	Description              string     `json:"description"`
	Status                   string     `json:"status"`
	Priority                 string     `json:"priority"`
	DueAt                    *time.Time `json:"due_at,omitempty"`
	AssigneeID               *uuid.UUID `json:"assignee_id,omitempty"`
	LinkedDocumentID         *uuid.UUID `json:"linked_document_id,omitempty"`
	LinkedWorkflowInstanceID *uuid.UUID `json:"linked_workflow_instance_id,omitempty"`
	Source                   string     `json:"source"`
	CreatedBy                uuid.UUID  `json:"created_by"`
	CompletedBy              *uuid.UUID `json:"completed_by,omitempty"`
	CompletedAt              *time.Time `json:"completed_at,omitempty"`
	RemindedAt               *time.Time `json:"reminded_at,omitempty"`
	OverdueNotifiedAt        *time.Time `json:"overdue_notified_at,omitempty"`
	CreatedAt                time.Time  `json:"created_at"`
	UpdatedAt                time.Time  `json:"updated_at"`
}

// TaskFilters narrows listings.
type TaskFilters struct {
	AssigneeID       *uuid.UUID
	CreatedBy        *uuid.UUID // tasks raised by this user (the "Created by me" inbox)
	LinkedDocumentID *uuid.UUID
	Statuses         []string // when non-empty, restrict to this set
	IncludeCompleted bool     // when false (default), excludes done+cancelled
}

// TaskRepository is the persistence interface.
type TaskRepository interface {
	Create(ctx context.Context, tx pgx.Tx, t *Task) error
	GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*Task, error)
	List(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, f TaskFilters) ([]Task, error)
	Update(ctx context.Context, tx pgx.Tx, t *Task) error
	Delete(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error

	// Sweep helpers (ADR 0068 §"Sweep").
	ClaimDueSoon(ctx context.Context, tx pgx.Tx, now time.Time) ([]Task, error)
	ClaimOverdue(ctx context.Context, tx pgx.Tx, now time.Time) ([]Task, error)
}

// NewTaskRepo returns the default Postgres-backed impl.
func NewTaskRepo() TaskRepository { return &taskRepo{} }

type taskRepo struct{}

const taskColumns = `tenant_id, id, title, COALESCE(description,''),
		status, priority, due_at, assignee_id,
		linked_document_id, linked_workflow_instance_id,
		source, created_by, completed_by, completed_at,
		reminded_at, overdue_notified_at, created_at, updated_at`

func (r *taskRepo) Create(ctx context.Context, tx pgx.Tx, t *Task) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO tasks (
			tenant_id, id, title, description,
			status, priority, due_at, assignee_id,
			linked_document_id, linked_workflow_instance_id,
			source, created_by, created_at, updated_at
		) VALUES (
			$1, $2, $3, NULLIF($4, ''),
			$5, $6, $7, $8,
			$9, $10,
			$11, $12, $13, $14
		)`,
		t.TenantID, t.ID, t.Title, t.Description,
		t.Status, t.Priority, t.DueAt, t.AssigneeID,
		t.LinkedDocumentID, t.LinkedWorkflowInstanceID,
		t.Source, t.CreatedBy, t.CreatedAt, t.UpdatedAt)
	return err
}

func (r *taskRepo) GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*Task, error) {
	row := tx.QueryRow(ctx, `SELECT `+taskColumns+`
		  FROM tasks WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	t, err := scanTask(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, vdmserr.ErrNotFound
		}
		return nil, err
	}
	return t, nil
}

func (r *taskRepo) List(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, f TaskFilters) ([]Task, error) {
	args := []any{tenantID}
	q := strings.Builder{}
	q.WriteString(`SELECT `)
	q.WriteString(taskColumns)
	q.WriteString(` FROM tasks WHERE tenant_id = $1`)
	if f.AssigneeID != nil {
		args = append(args, *f.AssigneeID)
		q.WriteString(` AND assignee_id = $`)
		q.WriteString(strconv.Itoa(len(args)))
	}
	if f.CreatedBy != nil {
		args = append(args, *f.CreatedBy)
		q.WriteString(` AND created_by = $`)
		q.WriteString(strconv.Itoa(len(args)))
	}
	if f.LinkedDocumentID != nil {
		args = append(args, *f.LinkedDocumentID)
		q.WriteString(` AND linked_document_id = $`)
		q.WriteString(strconv.Itoa(len(args)))
	}
	if len(f.Statuses) > 0 {
		args = append(args, f.Statuses)
		q.WriteString(` AND status = ANY($`)
		q.WriteString(strconv.Itoa(len(args)))
		q.WriteString(`)`)
	} else if !f.IncludeCompleted {
		q.WriteString(` AND status NOT IN ('done','cancelled')`)
	}
	// Stable ordering: due-first (NULLs last), then priority weight,
	// then newest. The frontend can override via query params later.
	q.WriteString(` ORDER BY (due_at IS NULL), due_at ASC,
		CASE priority WHEN 'urgent' THEN 4 WHEN 'high' THEN 3 WHEN 'normal' THEN 2 ELSE 1 END DESC,
		created_at DESC`)

	rows, err := tx.Query(ctx, q.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (r *taskRepo) Update(ctx context.Context, tx pgx.Tx, t *Task) error {
	tag, err := tx.Exec(ctx, `
		UPDATE tasks SET
			title = $3, description = NULLIF($4, ''),
			status = $5, priority = $6, due_at = $7, assignee_id = $8,
			completed_by = $9, completed_at = $10,
			reminded_at = $11, overdue_notified_at = $12,
			updated_at = now()
		 WHERE tenant_id = $1 AND id = $2`,
		t.TenantID, t.ID, t.Title, t.Description,
		t.Status, t.Priority, t.DueAt, t.AssigneeID,
		t.CompletedBy, t.CompletedAt,
		t.RemindedAt, t.OverdueNotifiedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

func (r *taskRepo) Delete(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error {
	tag, err := tx.Exec(ctx,
		`DELETE FROM tasks WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

// ClaimDueSoon stamps `reminded_at = now()` on every open/in-progress
// task whose due_at falls within 24h and which hasn't been reminded
// yet. Returns the rows so the caller emits notify events.
//
// The UPDATE...RETURNING is the atomicity primitive: only rows that
// transition from "unreminded" to "reminded" appear in the slice, so
// a crash between the flag write and the notify emit means the
// notify never fires — which is the correct failure mode (silent
// skip beats double-fire for nags).
func (r *taskRepo) ClaimDueSoon(ctx context.Context, tx pgx.Tx, now time.Time) ([]Task, error) {
	rows, err := tx.Query(ctx, `
		UPDATE tasks SET reminded_at = $1, updated_at = $1
		 WHERE status IN ('open','in_progress')
		   AND due_at IS NOT NULL
		   AND due_at >= $1 AND due_at <= $1 + interval '24 hours'
		   AND reminded_at IS NULL
		 RETURNING `+taskColumns, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// ClaimOverdue stamps `overdue_notified_at = now()` on every
// open/in-progress task whose due_at has already passed and which
// hasn't been overdue-pinged yet.
func (r *taskRepo) ClaimOverdue(ctx context.Context, tx pgx.Tx, now time.Time) ([]Task, error) {
	rows, err := tx.Query(ctx, `
		UPDATE tasks SET overdue_notified_at = $1, updated_at = $1
		 WHERE status IN ('open','in_progress')
		   AND due_at IS NOT NULL
		   AND due_at < $1
		   AND overdue_notified_at IS NULL
		 RETURNING `+taskColumns, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// ---- helpers -----------------------------------------------------------

type rowScannerT interface {
	Scan(dest ...any) error
}

func scanTask(r rowScannerT) (*Task, error) {
	var t Task
	if err := r.Scan(
		&t.TenantID, &t.ID, &t.Title, &t.Description,
		&t.Status, &t.Priority, &t.DueAt, &t.AssigneeID,
		&t.LinkedDocumentID, &t.LinkedWorkflowInstanceID,
		&t.Source, &t.CreatedBy, &t.CompletedBy, &t.CompletedAt,
		&t.RemindedAt, &t.OverdueNotifiedAt, &t.CreatedAt, &t.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &t, nil
}

