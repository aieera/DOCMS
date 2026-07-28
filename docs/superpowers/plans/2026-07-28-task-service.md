# Task Service Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the document-service lightweight tasks with a dedicated `services/task` microservice providing multi-assignee, multi-document tasks with comments/@mentions, an activity trail, and `dms.task.*` domain events — plus a rebuilt web UI (task drawer, pickers, per-document tasks panel).

**Architecture:** New Go module `services/task` (modeled on `services/policy`'s main.go: HTTP + outbox publisher, no gRPC server needed). It ADOPTS the existing `public.tasks` table (all services share one Postgres database, so the table cannot be recreated under the same name — this supersedes the spec's "copy + rename" wording; intent is identical) and adds `task_assignees`, `task_documents`, `task_comments`, `task_activity`. Document titles come from a same-DB `LEFT JOIN documents` (the workflow-service precedent at `services/workflow/internal/repository/repository.go:388` — no gRPC needed; supersedes the spec's gRPC mention). Gateway repoints `/api/v1/tasks` to the new upstream; URL shapes are preserved so mobile keeps working.

**Tech Stack:** Go 1.25/1.26 workspace, pgx v5, `pkg/{config,logger,middleware,database,auth,events,health}`, golang-migrate (`task_schema_migrations` bookkeeping), NATS JetStream via transactional outbox, React 18 + TanStack Query/Router + shadcn primitives, vitest, Playwright.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-07-28-task-service-design.md`. Decisions: multi-assignee; **anyone completes**; multi-document; visibility = everyone in tenant; comments flat, ≤4000 chars; title ≤200 chars.
- Statuses `open|in_progress|done|cancelled`; priorities `low|normal|high|urgent`; source `user|workflow`. Invalid priority on create/update → **400** (no silent coercion).
- Every table: composite `(tenant_id, …)` PK, RLS `ENABLE` + `FORCE`, policies on `current_setting('app.current_tenant', true)::uuid`. All queries inside `database.WithTenantTx`.
- All DB writes that emit events insert the outbox row **in the same transaction** (`database.NewOutboxRepository().Insert(ctx, tx, evt)`). Never publish to NATS directly.
- New NATS subjects MUST be added to `pkg/events/coverage.go` `PublishedSubjects` (alphabetized within aggregate) — `coverage_test.go` gates this.
- JSON DTOs use explicit snake_case tags (see the incident note at `services/document/internal/repository/tasks_repo.go:20-28`).
- Ports for the task service: health **8095**, http **8196** (host) / 8080+8081 (container). No gRPC server.
- Go tests: `go test -race ./services/task/...`; integration: `go test -tags integration ./services/task/...`. Web: `cd web && npm test -- -t "<name>"`, `npm run build` must stay green.
- Commit after every task (conventional commits, `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>` trailer).
- Compat endpoints that MUST keep working unchanged (mobile + topbar badge): `GET /tasks/mine`, `GET /tasks/created`, `POST /tasks/{id}/complete`, `POST /tasks/{id}/reopen`, `POST /tasks/{id}/cancel`, `DELETE /tasks/{id}`. Legacy `POST /tasks/{id}/assign|unassign` are retired.

---

### Task 1: Scaffold `services/task` and wire it into the build

**Files:**
- Create: `services/task/go.mod`, `services/task/cmd/server/main.go`
- Modify: `go.work` (add `./services/task`), `Makefile:23` (SERVICES list) and `Makefile:54-62` (add `test-task`), `scripts/migrate-all.sh:83` (append `task` to the svc loop), `docker-compose.yml` (new `task` block), `scripts/run-all-services.sh:37-54` (add `task:9102:8095:8196` spec)

**Interfaces:**
- Produces: a bootable HTTP service on :8196 (host) with `/healthz` on :8095; env prefix `SEDOC` via `config.Load("task")`.

- [ ] **Step 1: Create the module**

`services/task/go.mod`:
```
module github.com/aieera/sedoc/services/task

go 1.25.0
```
Then from repo root: `cd services/task && go mod edit -require github.com/aieera/sedoc/pkg@v0.0.0 && cd ../..` is NOT needed — workspace resolution handles `pkg`; just add `./services/task` to `go.work`'s `use` block (alphabetical position, after `./services/storage`... actually between `./services/storage` and `./services/workflow`? Alphabetically `task` sorts after `storage`, before `workflow` — insert there).

- [ ] **Step 2: Write `cmd/server/main.go`** — copy the shape of `services/policy/cmd/server/main.go` (171 lines) with: `const serviceName = "task"`; config/logger/license/pool/redis/NATS/health exactly as policy does; NO gRPC server (skip the `grpc.NewServer` block entirely); HTTP surface:

```go
httpMux := http.NewServeMux()
h := handler.New(svc, *log.Z())
h.Register(httpMux)
var httpRoot http.Handler = httpMux
httpRoot = middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool})(httpRoot)
httpSrv := &http.Server{
    Addr:              fmt.Sprintf(":%d", cfg.HTTPPort),
    Handler:           middleware.RequireGatewaySignature()(httpRoot),
    ReadHeaderTimeout: 5 * time.Second,
}
```
plus the outbox publisher (verbatim policy pattern):
```go
outbox := database.NewOutboxPublisher(pool, js, serviceName, *log.Z())
go outbox.Start(ctx)
```
and graceful shutdown (`httpSrv.Shutdown`, `outbox.Stop()`, `hs.Shutdown()`). For this task only, `handler.New`/`svc` don't exist yet — create minimal placeholders: `internal/handler/handler.go` with `type Handler struct{ log zerolog.Logger }`, `func New(svc *service.TaskService, log zerolog.Logger) *Handler`, `func (h *Handler) Register(mux *http.ServeMux) {}` and `internal/service/service.go` with `type TaskService struct{ Pool *pgxpool.Pool; Outbox *database.OutboxRepository; Log zerolog.Logger }`, `func New(pool *pgxpool.Pool, log zerolog.Logger) *TaskService`.

- [ ] **Step 3: Build wiring** — `Makefile:23` becomes `SERVICES := document storage search auth policy workflow notification audit signature billing connector task`; add `test-task: ; $(GO) test -race -cover ./services/task/... ## Run task tests` beside the other per-service targets and append `test-task` to `test-services`. In `scripts/migrate-all.sh` append `task` to the `for svc in auth search audit billing connector notification` loop.

- [ ] **Step 4: Compose + run-all** — docker-compose.yml, after the `connector` block, copying the `audit` block shape verbatim with:
```yaml
  task:
    build:
      context: .
      dockerfile: build/Dockerfile.go-service
      args: { SERVICE: task }
    container_name: sedoc-task
    environment: *go-env
    ports: ["8095:8081", "8196:8080"]
    depends_on: *go-depends
    healthcheck: *go-healthcheck
    restart: unless-stopped
```
`scripts/run-all-services.sh` service_specs gains `"task:9102:8095:8196"` (grpc slot unused but the format requires it — check how non-gRPC entries are written there and follow that if one exists).

- [ ] **Step 5: Verify boot** — `go build ./services/task/... && make build` (all green). Then `docker compose build task && docker compose up -d task && curl -fsS localhost:8095/healthz` → 200.

- [ ] **Step 6: Commit** — `git commit -m "feat(task): scaffold task microservice and build wiring"`

---

### Task 2: Migration 000001 — adopt `tasks`, create side tables, backfill

**Files:**
- Create: `services/task/migrations/000001_task_service_schema.up.sql`, `.down.sql`
- Test: `services/task/internal/repository/schema_integration_test.go` (`//go:build integration`)

**Interfaces:**
- Produces: tables `task_assignees`, `task_documents`, `task_comments`, `task_activity`; `tasks` gains `deleted_at TIMESTAMPTZ`. Legacy single-value columns (`assignee_id`, `linked_document_id`) REMAIN but are no longer read after Task 11.

- [ ] **Step 1: Write the up migration.** Content (complete):

```sql
-- Task service adopts the ADR-0068 tasks table (created by document
-- migration 000033) and adds the relational side tables from the
-- 2026-07-28 task-service design. Same shared database; ownership of
-- these tables transfers to the task service from this migration on.

ALTER TABLE tasks ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;

CREATE TABLE task_assignees (
    tenant_id UUID NOT NULL,
    task_id   UUID NOT NULL,
    user_id   UUID NOT NULL,
    added_by  UUID NOT NULL,
    added_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, task_id, user_id),
    FOREIGN KEY (tenant_id, task_id) REFERENCES tasks (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_task_assignees_user ON task_assignees (tenant_id, user_id);

CREATE TABLE task_documents (
    tenant_id      UUID NOT NULL,
    task_id        UUID NOT NULL,
    document_id    UUID NOT NULL,
    workspace_id   UUID NOT NULL,
    title_snapshot TEXT NOT NULL DEFAULT '',
    linked_by      UUID NOT NULL,
    linked_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, task_id, document_id),
    FOREIGN KEY (tenant_id, task_id) REFERENCES tasks (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_task_documents_document ON task_documents (tenant_id, document_id);

CREATE TABLE task_comments (
    tenant_id  UUID NOT NULL,
    id         UUID NOT NULL DEFAULT gen_random_uuid(),
    task_id    UUID NOT NULL,
    author_id  UUID NOT NULL,
    body       TEXT NOT NULL CHECK (char_length(body) <= 4000),
    mentions   UUID[] NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, task_id) REFERENCES tasks (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_task_comments_task ON task_comments (tenant_id, task_id, created_at);

CREATE TABLE task_activity (
    tenant_id  UUID NOT NULL,
    id         BIGINT GENERATED ALWAYS AS IDENTITY,
    task_id    UUID NOT NULL,
    actor_id   UUID NOT NULL,
    action     TEXT NOT NULL CHECK (action IN (
        'created','updated','assigned','unassigned','status_changed',
        'document_linked','document_unlinked','commented','deleted')),
    detail     JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, task_id) REFERENCES tasks (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_task_activity_task ON task_activity (tenant_id, task_id, id DESC);

-- RLS: identical posture to every other tenant table.
ALTER TABLE task_assignees ENABLE ROW LEVEL SECURITY;
ALTER TABLE task_assignees FORCE ROW LEVEL SECURITY;
CREATE POLICY task_assignees_tenant_isolation ON task_assignees
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
ALTER TABLE task_documents ENABLE ROW LEVEL SECURITY;
ALTER TABLE task_documents FORCE ROW LEVEL SECURITY;
CREATE POLICY task_documents_tenant_isolation ON task_documents
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
ALTER TABLE task_comments ENABLE ROW LEVEL SECURITY;
ALTER TABLE task_comments FORCE ROW LEVEL SECURITY;
CREATE POLICY task_comments_tenant_isolation ON task_comments
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
ALTER TABLE task_activity ENABLE ROW LEVEL SECURITY;
ALTER TABLE task_activity FORCE ROW LEVEL SECURITY;
CREATE POLICY task_activity_tenant_isolation ON task_activity
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- Backfill: legacy single assignee -> task_assignees; legacy linked
-- document -> task_documents (title/workspace snapshotted via JOIN).
INSERT INTO task_assignees (tenant_id, task_id, user_id, added_by, added_at)
SELECT t.tenant_id, t.id, t.assignee_id, t.created_by, t.created_at
  FROM tasks t
 WHERE t.assignee_id IS NOT NULL
ON CONFLICT DO NOTHING;

INSERT INTO task_documents (tenant_id, task_id, document_id, workspace_id, title_snapshot, linked_by, linked_at)
SELECT t.tenant_id, t.id, t.linked_document_id, d.workspace_id, d.title, t.created_by, t.created_at
  FROM tasks t
  JOIN documents d ON d.tenant_id = t.tenant_id AND d.id = t.linked_document_id
 WHERE t.linked_document_id IS NOT NULL
ON CONFLICT DO NOTHING;
```

Down migration: drop the four tables (reverse order), `ALTER TABLE tasks DROP COLUMN IF EXISTS deleted_at;`.

- [ ] **Step 2: Write the failing integration test.** Fixture pattern: copy the bootstrap from `services/document/internal/handler/clause_matches_integration_test.go` (`testutil.NewPostgresContainer` from `pkg/testutil/containers.go:21`, `SkipRLSPostureCheck = true`). Apply migrations in two tracks:
```go
require.NoError(t, database.RunMigrations(dsn, repoRoot+"/services/document/migrations"))
require.NoError(t, database.RunMigrations(dsn+"&x-migrations-table=task_schema_migrations", repoRoot+"/services/task/migrations"))
```
(If `dsn` has no query string, append `?x-migrations-table=…` — check and handle both.) Also pre-create `document_entities` exactly as the clause fixture does (document migration chain needs it from 000021). Test asserts: (a) seeding a legacy-style `tasks` row with `assignee_id`+`linked_document_id` BEFORE the task migrations, then running them, yields matching `task_assignees`/`task_documents` rows with `title_snapshot` = the document's title; (b) with `app.current_tenant` set to another tenant, `SELECT count(*) FROM task_assignees` returns 0 (RLS fail-closed).

- [ ] **Step 3: Run** `go test -tags integration -race -run TestTaskSchema ./services/task/internal/repository/` → must FAIL first (migration file not yet written if TDD-strict; acceptable order here: write test first, then migration).

- [ ] **Step 4: Implement/fix until PASS.**

- [ ] **Step 5: Commit** — `feat(task): schema migration adopting tasks + side tables with RLS and backfill`

---

### Task 3: Repository layer

**Files:**
- Create: `services/task/internal/model/task.go`, `services/task/internal/repository/repository.go`, `tasks_repo.go`, `comments_repo.go`, `activity_repo.go`
- Test: `services/task/internal/repository/tasks_repo_integration_test.go`

**Interfaces (Produces — later tasks depend on these exact names):**

```go
// model
type Task struct {                     // json tags all snake_case
    TenantID uuid.UUID `json:"tenant_id"`
    ID       uuid.UUID `json:"id"`
    Title, Description, Status, Priority, Source string
    DueAt, CompletedAt, DeletedAt *time.Time
    CreatedBy uuid.UUID
    CompletedBy *uuid.UUID
    RemindedAt, OverdueNotifiedAt *time.Time   // sweep bookkeeping (existing columns)
    CreatedAt, UpdatedAt time.Time
    Assignees []TaskAssignee   `json:"assignees"`   // aggregated
    Documents []TaskDocument   `json:"documents"`   // aggregated
}
type TaskAssignee struct { UserID uuid.UUID `json:"user_id"`; AddedBy uuid.UUID `json:"added_by"`; AddedAt time.Time `json:"added_at"` }
type TaskDocument struct { DocumentID, WorkspaceID uuid.UUID; TitleSnapshot string `json:"title"`; LinkedBy uuid.UUID; LinkedAt time.Time }
type TaskComment struct { TenantID, ID, TaskID, AuthorID uuid.UUID; Body string; Mentions []uuid.UUID; CreatedAt, UpdatedAt time.Time; DeletedAt *time.Time }
type TaskActivity struct { ID int64; TaskID, ActorID uuid.UUID; Action string; Detail json.RawMessage; CreatedAt time.Time }

// repository
type TaskFilters struct {
    AssigneeID, CreatedBy, DocumentID *uuid.UUID
    Statuses []string
    Priority string
    Query string          // ILIKE on title/description
    IncludeCompleted bool
    Limit, Offset int     // Limit clamped 1..100, default 50
    Sort string           // "due_at"|"priority"|"created_at" (default due_at)
}
type Repos struct { Tasks TaskRepository; Comments CommentRepository; Activity ActivityRepository; Outbox *database.OutboxRepository }
func New(pool *pgxpool.Pool) *Repos

type TaskRepository interface {
    Create(ctx, tx, *model.Task) error                          // inserts task + assignee + document rows
    GetByID(ctx, tx, tenantID, id uuid.UUID) (*model.Task, error)   // aggregates assignees+documents; excludes deleted
    List(ctx, tx, tenantID uuid.UUID, f TaskFilters) ([]model.Task, int, error)  // returns page + total
    Update(ctx, tx, *model.Task) error
    SoftDelete(ctx, tx, tenantID, id uuid.UUID) error
    AddAssignee(ctx, tx, tenantID, taskID, userID, addedBy uuid.UUID) error      // idempotent (ON CONFLICT DO NOTHING)
    RemoveAssignee(ctx, tx, tenantID, taskID, userID uuid.UUID) (bool, error)
    LinkDocument(ctx, tx, tenantID, taskID, documentID, linkedBy uuid.UUID) (*model.TaskDocument, error)
        // resolves workspace_id+title via: SELECT workspace_id, title FROM documents WHERE tenant_id=$1 AND id=$2 AND deleted_at IS NULL
        // returns repository.ErrNotFound if the document doesn't exist
    UnlinkDocument(ctx, tx, tenantID, taskID, documentID uuid.UUID) (bool, error)
    ClaimDueSoon(ctx, tx, now time.Time) ([]model.Task, error)   // port SQL verbatim from document tasks_repo.go:211
    ClaimOverdue(ctx, tx, now time.Time) ([]model.Task, error)   // port from :237
}
type CommentRepository interface {
    Create(ctx, tx, *model.TaskComment) error
    List(ctx, tx, tenantID, taskID uuid.UUID, limit, offset int) ([]model.TaskComment, error)  // oldest first, excludes deleted
    GetByID(ctx, tx, tenantID, id uuid.UUID) (*model.TaskComment, error)
    Update(ctx, tx, *model.TaskComment) error
    SoftDelete(ctx, tx, tenantID, id uuid.UUID) error
}
type ActivityRepository interface {
    Insert(ctx, tx, tenantID uuid.UUID, a *model.TaskActivity) error
    List(ctx, tx, tenantID, taskID uuid.UUID, limit, offset int) ([]model.TaskActivity, error) // newest first
}
var ErrNotFound = errors.New("not found")
```

Implementation notes: `List` runs one query for the page (`LEFT JOIN LATERAL` or a second `= ANY(ids)` query to hydrate assignees/documents — use the two-query approach: page of tasks, then `SELECT … FROM task_assignees WHERE tenant_id=$1 AND task_id = ANY($2)` and same for documents, stitched in Go), plus `count(*)` with the same WHERE for the total. `filter mine` = `EXISTS (SELECT 1 FROM task_assignees a WHERE a.tenant_id=t.tenant_id AND a.task_id=t.id AND a.user_id=$N)`. Default excludes `done`,`cancelled` unless `IncludeCompleted`; always excludes `deleted_at IS NOT NULL`. Sort: `due_at NULLS LAST, priority` ordering ported from document `tasks_repo.go:147-149`.

- [ ] **Step 1: Write failing integration tests** (same fixture as Task 2, extracted into a shared `setupTaskDB(t)` helper in `repo_fixture_integration_test.go`): create task with 2 assignees + 1 document → GetByID returns both aggregated; List with `AssigneeID` filter returns it for each assignee and not for a third user; List pagination returns total=N with page size honored; ILIKE query matches title; soft-deleted task disappears from Get/List; AddAssignee is idempotent; LinkDocument snapshots title and errors `ErrNotFound` on a bogus document id.
- [ ] **Step 2: Run** `go test -tags integration -race -run TestTasksRepo ./services/task/internal/repository/` → FAIL.
- [ ] **Step 3: Implement** repositories.
- [ ] **Step 4: Run to PASS** (plus `go vet ./services/task/...`).
- [ ] **Step 5: Commit** — `feat(task): repository layer with multi-assignee/document aggregation`

---

### Task 4: Service layer — core CRUD, permissions, events

**Files:**
- Create: `services/task/internal/service/tasks.go`, `permissions.go`, `events.go`
- Modify: `services/task/internal/service/service.go` (TaskService holds `Repos *repository.Repos`, `Pool`, `Log`)
- Test: `services/task/internal/service/permissions_test.go` (unit), `services/task/internal/service/tasks_service_integration_test.go`

**Interfaces (Produces):**

```go
type CreateTaskInput struct {
    Title, Description, Priority, Source string
    DueAt *time.Time
    AssigneeIDs []uuid.UUID `json:"assignee_ids"`
    DocumentIDs []uuid.UUID `json:"document_ids"`
}
type UpdateTaskInput struct { ID uuid.UUID; Title, Description, Priority *string; DueAt *time.Time; ClearDueAt bool }
type ListTasksInput struct { Filter string /* mine|created|all */; repository.TaskFilters }
type Page[T any] struct { Items []T `json:"items"`; Total int `json:"total"`; Limit, Offset int }

func (s *TaskService) CreateTask(ctx, in CreateTaskInput) (*model.Task, error)
func (s *TaskService) GetTask(ctx, id uuid.UUID) (*model.Task, error)
func (s *TaskService) ListTasks(ctx, in ListTasksInput) (*Page[model.Task], error)
func (s *TaskService) UpdateTask(ctx, in UpdateTaskInput) (*model.Task, error)
func (s *TaskService) DeleteTask(ctx, id uuid.UUID) error

// permissions.go — pure functions, unit-testable:
func isAdmin(role string) bool                                    // "admin" || "owner"
func canEditFields(t *model.Task, userID uuid.UUID, role string) bool      // creator or admin
func isAssignee(t *model.Task, userID uuid.UUID) bool
func canTransition(t *model.Task, userID uuid.UUID, role string) bool      // assignee, creator, or admin
func canManageLinks(t *model.Task, userID uuid.UUID, role string) bool     // == canTransition
// helpers (port from document service.go:871/886):
func mustCaller(ctx) (tenantID, userID uuid.UUID, role string, err error)  // via auth.User(ctx)
func (s *TaskService) withTenantTx(ctx, tenantID uuid.UUID, fn func(tx pgx.Tx) error) error
```

Behavior:
- CreateTask: title required/≤200 → 400-mapped `ErrValidation` (define `var ErrValidation`, `ErrForbidden`, `ErrNotFound` in service.go; handler maps them to 400/403/404); **invalid priority → ErrValidation**; inserts task, assignees (dedup, skip creator-duplicate rules — creator may also be assignee), links each document (ErrValidation if any document id unknown); activity rows `created` + one `assigned` per assignee (detail `{"user_id": "…"}`); outbox events (see events.go below); notify each assignee ≠ creator (Task 5 wires notification emit — include it here directly, it's small).
- events.go emits domain events with `database.NewOutboxEvent(tenantID, eventType, "task", taskID, payload)` where payload marshals `map[string]any`. Event types produced across Tasks 4-6 (add ALL to coverage in Task 7): `dms.task.created.v1`, `dms.task.updated.v1`, `dms.task.assigned.v1`, `dms.task.unassigned.v1`, `dms.task.status_changed.v1`, `dms.task.document_linked.v1`, `dms.task.document_unlinked.v1`, `dms.task.comment.created.v1`, `dms.task.deleted.v1`. Note: `pkg/database.NewOutboxEvent` takes `json.RawMessage` — marshal explicitly:
```go
func newTaskEvent(tenantID uuid.UUID, eventType string, taskID uuid.UUID, payload map[string]any) (*database.OutboxEvent, error) {
    raw, err := json.Marshal(payload)
    if err != nil { return nil, err }
    return database.NewOutboxEvent(tenantID, eventType, "task", taskID, raw), nil
}
```
(Verify `NewOutboxEvent`'s exact return — `pkg/database/outbox.go:54`; adjust if it returns a single value.)
- Notification emit helper (port shape from document `tasks.go:397-419` verbatim, adjusted to N recipients):
```go
func (s *TaskService) emitNotify(ctx, tx, tenantID uuid.UUID, taskID uuid.UUID, notifType, title, body string, userIDs []uuid.UUID) error
```
building the DeliveryPayload map exactly like the document version (`tenant_id`, `user_ids`, `type`, `title`, `body`, `resource_type: "task"`, `resource_id`) under event type `dms.notify.task.<suffix>.v1`.

- [ ] **Step 1: Unit tests for permissions.go** (table-driven: creator/assignee/admin/stranger × edit/transition). Run → FAIL.
- [ ] **Step 2: Implement permissions.go; PASS.**
- [ ] **Step 3: Integration tests**: create (2 assignees, 1 doc) → activity has created+2×assigned, outbox has `dms.task.created.v1` + 2× `dms.task.assigned.v1` + 1× `dms.notify.task.assigned.v1` per non-creator assignee (query `SELECT event_type FROM outbox WHERE tenant_id=$1` inside the fixture); stranger `UpdateTask` → ErrForbidden; invalid priority → ErrValidation; list filter=mine/created behave.
- [ ] **Step 4: Implement tasks.go + events.go; PASS.**
- [ ] **Step 5: Commit** — `feat(task): core service CRUD with permissions, activity, events`

---

### Task 5: Service layer — status transitions, assignee & document management

**Files:**
- Create: `services/task/internal/service/transitions.go`, `links.go`
- Test: extend `tasks_service_integration_test.go` (+ `transitions_test.go` unit for the transition matrix)

**Interfaces (Produces):**
```go
func (s *TaskService) StartTask(ctx, id uuid.UUID) (*model.Task, error)     // open -> in_progress
func (s *TaskService) CompleteTask(ctx, id uuid.UUID) (*model.Task, error)  // open|in_progress -> done
func (s *TaskService) ReopenTask(ctx, id uuid.UUID) (*model.Task, error)    // done|cancelled -> open
func (s *TaskService) CancelTask(ctx, id uuid.UUID) (*model.Task, error)    // open|in_progress -> cancelled
func (s *TaskService) AddAssignee(ctx, taskID, userID uuid.UUID) (*model.Task, error)
func (s *TaskService) RemoveAssignee(ctx, taskID, userID uuid.UUID) (*model.Task, error)  // self-removal always allowed
func (s *TaskService) LinkDocument(ctx, taskID, documentID uuid.UUID) (*model.Task, error)
func (s *TaskService) UnlinkDocument(ctx, taskID, documentID uuid.UUID) (*model.Task, error)
```
Behavior: transitions gate on `canTransition`; illegal source state → ErrValidation ("cannot complete a cancelled task"). Complete records `completed_by/at`, clears them on reopen, emits `dms.task.status_changed.v1` (`detail {"from":…,"to":…}` mirrored in activity) and — **anyone-completes** — a `dms.notify.task.completed.v1` to (assignees ∪ creator) − actor with title `"Task completed"`, body = task title. AddAssignee (gate `canManageLinks`): idempotent, activity `assigned`, notify the new assignee if ≠ actor (`dms.notify.task.assigned.v1`, reuse Task 4 helper). RemoveAssignee: `canManageLinks` OR `userID == actor`. Link/Unlink document: activity `document_linked`/`document_unlinked` with `{"document_id":…,"title":…}`, events accordingly; linking a second time is idempotent (repo ON CONFLICT).

- [ ] **Step 1: Unit test the transition matrix** (valid/invalid pairs) against a pure `func validTransition(from, to string) bool` you define in transitions.go. FAIL → implement → PASS.
- [ ] **Step 2: Integration tests**: assignee B (not creator) completes → done + completion notify rows to A(creator)+C(other assignee) only; stranger complete → ErrForbidden; reopen clears completed_by; self-remove as non-manager works, removing someone else as plain assignee works (canManageLinks includes assignees), stranger → ErrForbidden; link unknown doc → ErrValidation.
- [ ] **Step 3: Implement; PASS.**
- [ ] **Step 4: Commit** — `feat(task): status transitions and assignee/document management`

---

### Task 6: Service layer — comments with @mentions, activity feed, sweep

**Files:**
- Create: `services/task/internal/service/comments.go`, `sweep.go`
- Test: `comments_mentions_test.go` (unit for mention parsing), extend integration file; `sweep_integration_test.go`

**Interfaces (Produces):**
```go
func (s *TaskService) AddComment(ctx, taskID uuid.UUID, body string) (*model.TaskComment, error)
func (s *TaskService) ListComments(ctx, taskID uuid.UUID, limit, offset int) ([]model.TaskComment, error)
func (s *TaskService) UpdateComment(ctx, taskID, commentID uuid.UUID, body string) (*model.TaskComment, error)
func (s *TaskService) DeleteComment(ctx, taskID, commentID uuid.UUID) error
func (s *TaskService) ListActivity(ctx, taskID uuid.UUID, limit, offset int) ([]model.TaskActivity, error)
func (s *TaskService) SweepTaskNotifications(ctx) error
func parseMentions(body string) []uuid.UUID   // pure — extracts ids from @[Display Name](uuid) tokens
```
Mention token format MUST match the web's `mentionToken` helper (`web/src/api/comments.ts:78` — read it; it is the same format document comments use, `@[name](userId)`). `parseMentions` regex: `@\[[^\]]*\]\(([0-9a-f-]{36})\)`, dedup, drop non-parsing UUIDs. AddComment: any tenant member (no gate beyond auth); validates body non-empty/≤4000; verifies mentioned ids exist in `users` (single `SELECT id FROM users WHERE tenant_id=$1 AND id = ANY($2)`; silently drops unknown); activity `commented`; event `dms.task.comment.created.v1`; notify mentioned users − author via `dms.notify.task.mention.v1` (`type: "task.mention"`, title `"You were mentioned on a task"`). Update/Delete: author only (delete also admin); no re-notification on edit. Sweep: port `SweepTaskNotifications`/`sweepOne`/`listAllTenantsForSweep` from document `tasks.go:337-390` (tenant list = `SELECT id FROM organizations` outside RLS — port exactly), recipients change: due-soon → all assignees; overdue → assignees ∪ creator. Wire the hourly goroutine in `cmd/server/main.go` copying the document block verbatim (`main.go:1080-1100` shape).

- [ ] **Step 1: Unit tests** for `parseMentions` (token, multiple, duplicates, malformed). FAIL → implement → PASS.
- [ ] **Step 2: Integration**: comment with mention of user C → comment row's `mentions=[C]`, notify row targets C only; comment by non-participant succeeds; edit by other user → ErrForbidden; sweep with a due-in-2h task → one `dms.notify.task.due_soon.v1` targeting both assignees, second sweep run emits nothing (claim idempotence).
- [ ] **Step 3: Implement; PASS.**
- [ ] **Step 4: Commit** — `feat(task): comments with mentions, activity feed, notification sweep`

---

### Task 7: HTTP handlers + events coverage allowlist

**Files:**
- Create: `services/task/internal/handler/tasks_handler.go`, `comments_handler.go`
- Modify: `services/task/internal/handler/handler.go` (Register wires both), `pkg/events/coverage.go`
- Test: `services/task/internal/handler/tasks_handler_integration_test.go`; `pkg/events` tests must stay green

**Interfaces (Produces):** routes — the REST surface later consumed by web/mobile:
```
POST   /api/v1/tasks                       GET    /api/v1/tasks (paginated envelope)
GET    /api/v1/tasks/mine                  GET    /api/v1/tasks/created      ← compat: return BARE ARRAY (mobile parses Task[])
GET    /api/v1/tasks/{id}                  PATCH  /api/v1/tasks/{id}
POST   /api/v1/tasks/{id}/start            POST   /api/v1/tasks/{id}/complete
POST   /api/v1/tasks/{id}/reopen           POST   /api/v1/tasks/{id}/cancel
DELETE /api/v1/tasks/{id}
POST   /api/v1/tasks/{id}/assignees        DELETE /api/v1/tasks/{id}/assignees/{userId}
POST   /api/v1/tasks/{id}/documents        DELETE /api/v1/tasks/{id}/documents/{docId}
GET    /api/v1/tasks/{id}/comments         POST   /api/v1/tasks/{id}/comments
PATCH  /api/v1/tasks/{id}/comments/{cid}   DELETE /api/v1/tasks/{id}/comments/{cid}
GET    /api/v1/tasks/{id}/activity
```
`GET /tasks` responds `{"items":[…],"total":N,"limit":L,"offset":O}` with query params `filter=mine|created|all` (default all), `status`, `priority`, `document_id`, `q`, `include_completed`, `limit`, `offset`, `sort`. `/mine` + `/created` shims call the same service with forced filter and `include_completed` passthrough, returning `[]model.Task` bare (back-compat with `web/src/api/tasks.ts:52-67` and mobile). Error mapping helper: ErrValidation→400, ErrForbidden→403, ErrNotFound/pgx.ErrNoRows→404, else 500 (JSON `{"error": "..."}`; port `writeJSON` shape from document handlers).

coverage.go: add to `PublishedSubjects` under a new `// task` group (alphabetized): the nine `dms.task.*.v1` subjects from Task 4 plus `dms.notify.task.assigned.v1`, `dms.notify.task.completed.v1`, `dms.notify.task.due_soon.v1`, `dms.notify.task.mention.v1`, `dms.notify.task.overdue.v1` (the first/due/overdue three are emitted today but missing from the list — fixing that gap per Explore finding).

- [ ] **Step 1: Failing handler integration tests** (fixture: mux with `handler.New(svc, log).Register(mux)`, contexts via `auth.WithUser` — copy `doAuthedJSON` from the clause test): create with assignee_ids+document_ids → 201 + aggregated body; GET /tasks pagination envelope; /tasks/mine bare array; complete by assignee → 200; complete by stranger → 403; invalid priority → 400; comment POST → 201; activity GET returns newest-first.
- [ ] **Step 2: Implement handlers; PASS** (`go test -tags integration -race ./services/task/...`).
- [ ] **Step 3:** `go test ./pkg/events/...` → PASS with new subjects.
- [ ] **Step 4: Commit** — `feat(task): REST handlers with compat shims; register dms.task events`

---

### Task 8: Gateway + dev-proxy repoint

**Files:**
- Modify: `deploy/gateway/routes.yaml:312` → `- { service: task, prefix: /api/v1/tasks, methods: [], auth: required }`
- Modify: `deploy/gateway/kong.yaml` — remove the `tasks` route line (L222) from the `document` service block; add a new top-level service block:
```yaml
  - name: task
    url: http://task:8080
    routes:
      - { name: tasks, paths: [/api/v1/tasks], strip_path: false }
```
- Modify: `web/vite.config.ts:221` → `'/api/v1/tasks': wsig('http://localhost:8196'),`
- Modify: `web/src/test/devProxyCoverage.test.ts:19-38` — add `task: '8196'` to SERVICE_PORTS
- Test: `go test ./pkg/archtest/...` (route mirror guards), `cd web && npx vitest run src/test/devProxyCoverage.test.ts`

- [ ] **Step 1:** Make the four edits.
- [ ] **Step 2:** Run both guard suites → PASS (they FAIL if routes.yaml/kong.yaml/proxy drift — this is the test for this task).
- [ ] **Step 3:** Live check: `docker compose up -d task kong && curl -fsS -o /dev/null -w '%{http_code}' localhost:8080/api/v1/tasks` → 401 (unauthenticated, but routed — not 404).
- [ ] **Step 4: Commit** — `feat(task): repoint /api/v1/tasks gateway + dev proxy to task service`

---

### Task 9: Remove document-service task code

**Files:**
- Delete: `services/document/internal/handler/tasks_handler.go`, `services/document/internal/service/tasks.go`, `services/document/internal/service/tasks_test.go`, `tasks_notify_payload_test.go`, `services/document/internal/repository/tasks_repo.go`
- Modify: `services/document/cmd/server/main.go` — remove the tasks mux mounts (L1043-1057 region) and the sweep goroutine (L1080-1100); `services/document/internal/repository/repository.go:314` — remove the Tasks repo field/registration.

- [ ] **Step 1:** Delete files, remove wiring; chase compile errors (`grep -rn "TasksHandler\|CreateTask\|repository.Task\b" services/document/` — anything else referencing tasks, e.g. AddToTask flows server-side, must be gone; workflow `source='workflow'` rows were never created per Explore, nothing else to port).
- [ ] **Step 2:** `go build ./services/document/... && go test -race ./services/document/...` → PASS. `make build` → PASS.
- [ ] **Step 3:** Rebuild + restart: `docker compose build document task && docker compose up -d document task`; verify `curl localhost:8182/api/v1/tasks` (document svc direct) now 404s while gateway routes to task svc fine.
- [ ] **Step 4: Commit** — `refactor(document): remove task code now owned by task service`

---

### Task 10: Web API client rewrite

**Files:**
- Rewrite: `web/src/api/tasks.ts`
- Test: `web/src/api/__tests__/tasks.test.ts` (new; follow an existing api test's msw/axios-mock pattern — check `web/src/api/__tests__/` for the house style; if none exists there, colocate with a vitest mock of `./client`)

**Interfaces (Produces — consumed by Tasks 11-14):**
```ts
export interface TaskAssignee { user_id: string; added_by: string; added_at: string }
export interface TaskDocument { document_id: string; workspace_id: string; title: string; linked_by: string; linked_at: string }
export interface Task { …existing scalar fields minus assignee_id/linked_*…; assignees: TaskAssignee[]; documents: TaskDocument[] }
export interface TaskComment { id: string; task_id: string; author_id: string; body: string; mentions: string[]; created_at: string; updated_at: string }
export interface TaskActivityEntry { id: number; task_id: string; actor_id: string; action: string; detail: Record<string, unknown>; created_at: string }
export interface TaskPage { items: Task[]; total: number; limit: number; offset: number }
export interface ListTasksParams { filter?: 'mine'|'created'|'all'; status?: TaskStatus; priority?: TaskPriority; document_id?: string; q?: string; include_completed?: boolean; limit?: number; offset?: number; sort?: 'due_at'|'priority'|'created_at' }
export interface CreateTaskInput { title: string; description?: string; priority?: TaskPriority; due_at?: string; assignee_ids?: string[]; document_ids?: string[] }

listTasks(params: ListTasksParams): Promise<TaskPage>          // GET /tasks
listMyTasks(includeCompleted = false): Promise<Task[]>         // unchanged signature (topbar badge, dashboard)
listMyCreatedTasks(includeCompleted = false): Promise<Task[]>
createTask / getTask / updateTask / deleteTask                 // as today
startTask(id) / completeTask(id) / reopenTask(id) / cancelTask(id)
addAssignee(id, user_id) / removeAssignee(id, user_id)
linkDocument(id, document_id) / unlinkDocument(id, document_id)
listComments(id) / addComment(id, body) / updateComment(id, cid, body) / deleteComment(id, cid)
listActivity(id): Promise<TaskActivityEntry[]>
```
Remove `assignTask`/`unassignTask`. Query-key convention for all consumers: `['tasks', …]` family — `['tasks','mine']`, `['tasks','created']`, `['tasks','list',params]`, `['tasks','detail',id]`, `['tasks','comments',id]`, `['tasks','activity',id]`; a shared `invalidateTasks(queryClient)` helper exported from tasks.ts runs `queryClient.invalidateQueries({ queryKey: ['tasks'] })`.

- [ ] Steps: failing api unit test (URL/params/envelope handling for listTasks + bare-array shims) → implement → PASS → fix all existing compile errors in consumers (`app-topbar.tsx:222`, `routes/_authenticated/index.tsx` dashboard card — swap their query keys to `['tasks','mine']`) → `npm run build` PASS → commit `feat(web): task api client for task service surface`.

---

### Task 11: AssigneePicker + DocumentPicker components

**Files:**
- Create: `web/src/components/tasks/AssigneePicker.tsx`, `web/src/components/tasks/DocumentPicker.tsx`
- Test: `web/src/components/tasks/__tests__/AssigneePicker.test.tsx`, `DocumentPicker.test.tsx`

**Interfaces (Produces):**
```tsx
// Multi-select user picker. Renders selected users as avatar chips (initials
// via shadcn Avatar) with an X to remove; a search input (debounced 250ms)
// queries listUserDirectory(q) from '@/api/auth' under queryKey
// ['mention-search', q] (shared cache with comments/mentions).
export function AssigneePicker(props: {
  value: string[]                        // user ids
  onChange: (ids: string[]) => void
  disabled?: boolean
}): JSX.Element

// Multi-select document picker. Search via suggest(q) from '@/api/search'
// (SuggestResult — check web/src/api/search.ts:19-26 for the concrete item
// shape and map to {id, title}); selected docs render as chips with title.
export function DocumentPicker(props: {
  value: { document_id: string; title: string }[]
  onChange: (docs: { document_id: string; title: string }[]) => void
  disabled?: boolean
}): JSX.Element
```
Both build on the existing `Popover` + `Command` shadcn primitives (see `web/src/components/ui/shadcn/combobox.tsx` for the composed pattern to copy). Vitest: mock the api modules; assert typing a query lists candidates, clicking adds a chip, X removes it, and `onChange` fires with ids.

- [ ] Steps: failing component tests → implement → `npx vitest run src/components/tasks` PASS → commit `feat(web): assignee and document picker components`.

---

### Task 12: TaskDetailDrawer (+ comments & activity)

**Files:**
- Create: `web/src/components/tasks/TaskDetailDrawer.tsx`, `TaskComments.tsx`, `TaskActivity.tsx`
- Test: `web/src/components/tasks/__tests__/TaskDetailDrawer.test.tsx`

**Interfaces (Produces):**
```tsx
export function TaskDetailDrawer(props: { taskId: string | null; onClose: () => void }): JSX.Element | null
```
Built on shadcn `Sheet` (side="right"). Content: title + status/priority badges; description; **Assignees** section = `AssigneePicker` bound to `addAssignee`/`removeAssignee` mutations; **Documents** section = chips linking via TanStack `Link` `to="/workspaces/$workspaceId/documents/$documentId"` (workspace_id is on TaskDocument) + `DocumentPicker` for adding; action buttons per status (`open`: Start/Complete/Cancel; `in_progress`: Complete/Cancel; `done|cancelled`: Reopen) calling the respective api fns; Delete (confirm via existing `confirm-dialog`); `TaskComments` (reuses the CommentInput mention pattern from `web/src/components/documents/CommentsPanel.tsx:367-434` — extract that mention-textarea into the new component by copying, importing `listUserDirectory` from `@/api/auth` and `mentionToken` from `@/api/comments`); `TaskActivity` renders `listActivity` entries as a timeline (`action` → human line, e.g. `status_changed` → "moved from open to done", using `detail`). All mutations call `invalidateTasks(queryClient)` on success. Permissions mirror server: hide Edit/Delete unless `created_by === me || role admin/owner`; transitions shown when me ∈ assignees ∪ creator ∪ admin (role from `useAuthStore`).

Vitest (mock api module): renders task fields; complete button fires `completeTask`; comment submit posts body containing a mention token; activity entries render.

- [ ] Steps: failing tests → implement → PASS → commit `feat(web): task detail drawer with comments and activity`.

---

### Task 13: Tasks page rebuild + create dialog

**Files:**
- Create: `web/src/components/tasks/TaskCreateDialog.tsx`, `TaskListSection.tsx` (list+kanban+filters, extracted)
- Rewrite: `web/src/routes/_authenticated/tasks.tsx` (route keeps tabs + ApprovalsSection untouched; `MyTasksSection`→`TaskListSection` using server-side `listTasks({filter, status, priority, q, include_completed, limit, offset, sort})`; row click opens `TaskDetailDrawer`; keep the existing table/kanban toggle and kanban columns, now with a Start action so `in_progress` is reachable)
- Modify: `web/src/routes/_authenticated/workspaces/$workspaceId/documents/$documentId.tsx` — swap the `CreateTaskDialog` import to the new `TaskCreateDialog`
- Test: `web/src/components/tasks/__tests__/TaskCreateDialog.test.tsx`; existing route tests/`npm run build`

`TaskCreateDialog` props: `{ open: boolean; onOpenChange: (o: boolean) => void; defaultDocument?: { document_id: string; title: string } }` — fields Title, Description, `AssigneePicker`, `DocumentPicker` (seeded with defaultDocument), Priority select, Due `datetime-local`; submits `createTask({…, assignee_ids, document_ids})`; on success `invalidateTasks` + toast. Replaces the old exported `CreateTaskDialog` and `AddToTaskDialog` (delete `web/src/components/documents/AddToTaskDialog.tsx` and its `['tasks']`-key bug; update `DocumentActionsMenu.tsx:27,274` to open `TaskCreateDialog` with `defaultDocument`).

- [ ] Steps: failing dialog test (submit builds correct payload) → implement page+dialog → `npm run build` + `npx vitest run src/components/tasks src/routes` PASS → commit `feat(web): rebuild tasks page with server filters, drawer, and rich create dialog`.

---

### Task 14: Document page Tasks panel

**Files:**
- Create: `web/src/components/tasks/DocumentTasksPanel.tsx`
- Modify: `web/src/routes/_authenticated/workspaces/$workspaceId/documents/$documentId.tsx` — render the panel in the right-hand details column (same region as the clause-matches panel)
- Test: `web/src/components/tasks/__tests__/DocumentTasksPanel.test.tsx`

```tsx
export function DocumentTasksPanel(props: { documentId: string; documentTitle: string }): JSX.Element | null
```
Query `listTasks({ document_id: documentId, include_completed: false, limit: 20 })` under `['tasks','list',{document_id}]`; self-hides when empty AND offers a small "+ Task" button that opens `TaskCreateDialog` with `defaultDocument`; rows: title, status badge, assignee count, due — click opens `TaskDetailDrawer`.

- [ ] Steps: failing test (renders rows from mocked listTasks; hides list when empty) → implement → PASS → commit `feat(web): per-document tasks panel`.

---

### Task 15: Notification prefs + E2E + final verification

**Files:**
- Modify: `web/src/api/notification-prefs.ts:39-40` — add `{ id: 'task.mention', … }` and `{ id: 'task.completed', … }` entries following the exact object shape of the existing `task.assigned` / `task.due` entries (also confirm the notification service honors unknown types by default — check `services/notification/internal/service/service.go:269-326` consumer's type handling; if prefs are allowlist-based, add the ids wherever `task.assigned` is registered server-side).
- Modify: `web/e2e/50-tasks.spec.ts` — extend: create task with 2 assignees + 1 document via UI; drawer shows both; second-user complete via API; comment with @mention; document page shows the panel.
- Verify: whole-repo gates.

- [ ] **Step 1:** prefs entries + any server-side registration; `npm run build`.
- [ ] **Step 2:** Extend the Playwright spec (run against the compose stack: `LD_LIBRARY_PATH` note + port 4173 per `docs`-recorded a11y setup — see memory: run `npm run test:e2e -- 50-tasks`).
- [ ] **Step 3: Full gates:** `make build && make test` (Go), `go test -tags integration ./services/task/...`, `cd web && npm run lint && npm test && npm run build`.
- [ ] **Step 4:** Live smoke on compose: create a task with two assignees + a linked document via the UI at :3000; complete it from the drawer; check the activity timeline; confirm a notification row lands for the second assignee.
- [ ] **Step 5: Commit** — `feat(task): notification prefs, e2e coverage, final wiring`

---

## Self-review notes (already applied)

- **Spec deviations locked in:** (1) table ADOPTION instead of copy+rename — same-DB name collision makes the spec's rename impossible without breaking the document migration chain; (2) document titles via same-DB JOIN (workflow precedent) instead of gRPC; (3) rate-limits.yaml needs NO new entry — the `default` class covers every route unless overridden (rate-limits.yaml L14 comment), closing the spec's "add rate limit" item as already-satisfied; document this in the PR description.
- **Type consistency:** `Page[T]` envelope (Go) ↔ `TaskPage` (TS); `filter=mine|created|all` naming consistent across Task 4/7/10; compat shims return bare arrays — double-checked against `web/src/api/tasks.ts` current parsing and mobile.
- **Coverage:** every spec section maps to a task: architecture/scaffold→1, data model→2-3, API→4-7, events/notifications→4-7+15, gateway→8, legacy removal→9, web UI→10-14, testing→per-task + 15. Defect list from the spec: 404 created-tab→7 (properly mounted), tenant dump→7 (paginated, official), stale badge keys→10, in_progress unreachable→5+13, hard delete→3 (SoftDelete), missing domain events→4-7, rate limit→closed as above.
