// Package model holds the task service's internal domain types. These are
// the row shapes the repository layer reads/writes; the handler layer maps
// them onto wire DTOs at the transport boundary.
//
// JSON tags are explicit snake_case on every exported field. The document
// service's legacy tasks_repo.go carries an incident comment for exactly
// this: without the tags Go's encoder defaults to PascalCase field names
// while the frontend reads snake_case, so every field silently comes back
// `undefined` on the client. Same rule applies here.
package model

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Task is the aggregate root: the `tasks` row plus its multi-assignee and
// multi-document side tables, hydrated by the repository layer. Assignees
// and Documents are nil/empty on a row that hasn't been hydrated (e.g. a
// bare scan) and populated by GetByID/List.
type Task struct {
	TenantID    uuid.UUID  `json:"tenant_id"`
	ID          uuid.UUID  `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Status      string     `json:"status"`
	Priority    string     `json:"priority"`
	Source      string     `json:"source"`
	DueAt       *time.Time `json:"due_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	DeletedAt   *time.Time `json:"deleted_at,omitempty"`
	CreatedBy   uuid.UUID  `json:"created_by"`
	CompletedBy *uuid.UUID `json:"completed_by,omitempty"`
	// RemindedAt / OverdueNotifiedAt are the hourly sweep's single-shot
	// bookkeeping flags (ADR 0068 §"Sweep"): at most one reminder and one
	// overdue ping per task, ever.
	RemindedAt        *time.Time `json:"reminded_at,omitempty"`
	OverdueNotifiedAt *time.Time `json:"overdue_notified_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	// Assignees / Documents are aggregated from task_assignees /
	// task_documents by GetByID and List; Create/Update read them off the
	// struct to seed those side tables.
	Assignees []TaskAssignee `json:"assignees"`
	Documents []TaskDocument `json:"documents"`
}

// TaskAssignee is one row of `task_assignees` — a user assigned to a task,
// alongside who added them and when.
type TaskAssignee struct {
	UserID  uuid.UUID `json:"user_id"`
	AddedBy uuid.UUID `json:"added_by"`
	AddedAt time.Time `json:"added_at"`
}

// TaskDocument is one row of `task_documents` — a document linked to a
// task. WorkspaceID and TitleSnapshot are captured at link time (not
// live-joined) so the task list renders without an N+1 lookup into
// `documents`, and survives the linked document being renamed later.
//
// TitleSnapshot's json tag is "title", not "title_snapshot" — this mirrors
// the wire shape the frontend already expects for a linked document's
// display name (task-3-brief.md's struct sketch calls this out explicitly).
type TaskDocument struct {
	DocumentID    uuid.UUID `json:"document_id"`
	WorkspaceID   uuid.UUID `json:"workspace_id"`
	TitleSnapshot string    `json:"title"`
	LinkedBy      uuid.UUID `json:"linked_by"`
	LinkedAt      time.Time `json:"linked_at"`
}

// TaskComment is one row of `task_comments`. Mentions is the set of user
// ids parsed out of Body at write time (same convention as the document
// service's comment mentions), stored denormalized so the notification
// fan-out doesn't need to re-parse the body.
type TaskComment struct {
	TenantID  uuid.UUID   `json:"tenant_id"`
	ID        uuid.UUID   `json:"id"`
	TaskID    uuid.UUID   `json:"task_id"`
	AuthorID  uuid.UUID   `json:"author_id"`
	Body      string      `json:"body"`
	Mentions  []uuid.UUID `json:"mentions"`
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
	DeletedAt *time.Time  `json:"deleted_at,omitempty"`
}

// TaskActivity is one row of `task_activity` — the append-only audit trail
// for a task (assigned/unassigned/status_changed/etc.). Detail is
// action-specific JSON (e.g. {"from":"open","to":"done"} for
// status_changed). No TenantID field: callers scope by tenant explicitly
// via the ActivityRepository methods' tenantID parameter, matching the
// task-3-brief.md struct sketch.
type TaskActivity struct {
	ID        int64           `json:"id"`
	TaskID    uuid.UUID       `json:"task_id"`
	ActorID   uuid.UUID       `json:"actor_id"`
	Action    string          `json:"action"`
	Detail    json.RawMessage `json:"detail"`
	CreatedAt time.Time       `json:"created_at"`
}
