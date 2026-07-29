// Package repository is the Postgres data-access layer for the task
// service. Every method takes a pgx.Tx (never the pool directly) so the
// service layer can bundle writes + outbox inserts into one transaction.
// Callers must invoke these inside database.WithTenantTx so the
// `app.current_tenant` GUC is set and RLS policies fire (see
// pkg/database/tenant.go); a NOBYPASSRLS app role then fails closed on any
// query that forgets a tenant predicate.
package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/services/task/internal/model"
)

// ErrNotFound is returned when a lookup by id doesn't match any (live,
// non-deleted) row.
var ErrNotFound = errors.New("not found")

// TaskFilters narrows a TaskRepository.List call. Limit is clamped to
// [1,100] (default 50) inside List; Offset < 0 is treated as 0.
type TaskFilters struct {
	AssigneeID *uuid.UUID
	CreatedBy  *uuid.UUID
	DocumentID *uuid.UUID
	Statuses   []string // when non-empty, restricts to this set (overrides IncludeCompleted)
	Priority   string   // exact match on priority when non-empty
	Query      string   // ILIKE against title/description when non-empty
	// IncludeCompleted, when false (the default) and Statuses is empty,
	// excludes status IN ('done','cancelled').
	IncludeCompleted bool
	Limit, Offset    int
	// Sort is one of "due_at" (default), "priority", "created_at".
	Sort string
}

// TaskRepository is the persistence interface for the `tasks` aggregate
// (tasks + task_assignees + task_documents).
type TaskRepository interface {
	// Create inserts the task row plus one task_assignees row per
	// t.Assignees and one task_documents row per t.Documents.
	Create(ctx context.Context, tx pgx.Tx, t *model.Task) error
	// GetByID loads a task and hydrates its Assignees/Documents. Excludes
	// soft-deleted tasks (returns ErrNotFound).
	GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.Task, error)
	// List returns one page of tasks (hydrated) plus the total matching
	// row count, ignoring Limit/Offset.
	List(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, f TaskFilters) ([]model.Task, int, error)
	Update(ctx context.Context, tx pgx.Tx, t *model.Task) error
	SoftDelete(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error
	// AddAssignee is idempotent: re-adding the same (task, user) pair is a
	// no-op (ON CONFLICT DO NOTHING).
	AddAssignee(ctx context.Context, tx pgx.Tx, tenantID, taskID, userID, addedBy uuid.UUID) error
	// RemoveAssignee reports whether a row was actually removed.
	RemoveAssignee(ctx context.Context, tx pgx.Tx, tenantID, taskID, userID uuid.UUID) (bool, error)
	// LinkDocument resolves workspace_id + title from `documents` (must be
	// live, i.e. deleted_at IS NULL) and snapshots them onto task_documents.
	// Returns ErrNotFound when the document doesn't exist.
	LinkDocument(ctx context.Context, tx pgx.Tx, tenantID, taskID, documentID, linkedBy uuid.UUID) (*model.TaskDocument, error)
	// UnlinkDocument reports whether a row was actually removed.
	UnlinkDocument(ctx context.Context, tx pgx.Tx, tenantID, taskID, documentID uuid.UUID) (bool, error)
	// ClaimDueSoon/ClaimOverdue are the hourly sweep's atomic claim
	// primitives, ported from the document service's legacy
	// tasks_repo.go (ClaimDueSoon:211 / ClaimOverdue:237): an
	// UPDATE...RETURNING flips the single-shot flag and returns only the
	// rows that transitioned, so a crash between the flag write and the
	// notify emit means the notify never fires (silent skip beats
	// double-fire for nags).
	ClaimDueSoon(ctx context.Context, tx pgx.Tx, now time.Time) ([]model.Task, error)
	ClaimOverdue(ctx context.Context, tx pgx.Tx, now time.Time) ([]model.Task, error)
}

// CommentRepository is the persistence interface for `task_comments`.
type CommentRepository interface {
	Create(ctx context.Context, tx pgx.Tx, c *model.TaskComment) error
	// List returns comments oldest-first, excluding soft-deleted rows.
	List(ctx context.Context, tx pgx.Tx, tenantID, taskID uuid.UUID, limit, offset int) ([]model.TaskComment, error)
	GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.TaskComment, error)
	Update(ctx context.Context, tx pgx.Tx, c *model.TaskComment) error
	SoftDelete(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error
}

// ActivityRepository is the persistence interface for the append-only
// `task_activity` audit trail.
type ActivityRepository interface {
	// Insert writes a, filling in a.ID and a.CreatedAt from the row.
	Insert(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, a *model.TaskActivity) error
	// List returns activity newest-first.
	List(ctx context.Context, tx pgx.Tx, tenantID, taskID uuid.UUID, limit, offset int) ([]model.TaskActivity, error)
}

// Repos groups every task-service repo plus the shared transactional
// outbox helper.
type Repos struct {
	Tasks    TaskRepository
	Comments CommentRepository
	Activity ActivityRepository
	Outbox   *database.OutboxRepository
}

// New wires concrete repo implementations. pool is accepted for API
// symmetry with every other service's repository.New(pool) constructor;
// these repos are stateless (all queries run against the pgx.Tx callers
// pass in), so it is not stored.
func New(pool *pgxpool.Pool) *Repos {
	_ = pool
	return &Repos{
		Tasks:    &taskRepo{},
		Comments: &commentRepo{},
		Activity: &activityRepo{},
		Outbox:   database.NewOutboxRepository(),
	}
}
