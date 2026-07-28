# Task service — design

Date: 2026-07-28
Status: Approved by user (brainstorm session)
Replaces: ADR 0068 lightweight tasks (document-service implementation)
Related: ADR 0066 (threaded comments — pattern source), audit doc
`docs/audit/remediation/14b-wave7-p7.4-task-inbox.md` (workflow approvals
inbox — a separate system this design does NOT touch).

## Goal

Turn the lightweight tasks feature (title / description / single
assignee / priority / due date, no document context) into a proper
document-centric task-assignment system: multiple assignees, multiple
linked documents, task comments with @mentions, an activity trail,
richer notifications, and a per-document tasks panel — implemented as a
dedicated `services/task` microservice.

## Decisions (user-confirmed)

| Question | Decision |
|---|---|
| Scope | Document-centric tasks (no subtasks/labels/saved views) |
| Assignment | **Multiple assignees** per task |
| Completion | **Anyone completes** — first assignee (or creator/admin) to complete closes the task for everyone |
| Document links | **Multiple documents** per task |
| Visibility | **Everyone in the tenant** sees all tasks (made official; list gets pagination) |
| Architecture | **C: dedicated task microservice** (`services/task`), replacing the document-service task code |

## Current state (what exists today)

- `tasks` table in the document service
  (`services/document/migrations/000033_lightweight_tasks.up.sql`):
  single `assignee_id`, `linked_document_id` (never exposed in the
  create dialog), `linked_workflow_instance_id`, status
  open/in_progress/done/cancelled, priority low/normal/high/urgent,
  source user/workflow.
- Handler `services/document/internal/handler/tasks_handler.go`,
  service `internal/service/tasks.go` (incl. hourly due-soon/overdue
  sweep), repo `internal/repository/tasks_repo.go`.
- Web: monolithic `web/src/routes/_authenticated/tasks.tsx` (736
  lines), `web/src/api/tasks.ts`, `AddToTaskDialog`, topbar badge,
  dashboard card. Mobile: `mobile/app/(tabs)/tasks.tsx` (uses
  `/tasks/mine`, `/complete`, `/reopen`).
- Notifications: `dms.notify.task.{assigned,due_soon,overdue}.v1` via
  outbox; consumed by the notification service.
- The Approvals tab reads the **workflow service** (`workflow_tasks`,
  Temporal) — completely separate; unchanged by this design.

Known defects fixed by this work:

1. `GET /api/v1/tasks/created` registered in the handler mux but not
   mounted on rootMux (`services/document/cmd/server/main.go`) — the
   "Created by me" tab 404s against the real server.
2. `GET /api/v1/tasks` returns every task in the tenant with no
   pagination (now official visibility-wise, but must paginate).
3. `AddToTaskDialog` invalidates `['tasks']` while lists/badge use
   `['my-tasks']` / `['created-tasks']` — stale UI after create.
4. `in_progress` status is unreachable (no UI/API path sets it).
5. `DeleteTask` is a hard delete with no audit trail.
6. No `dms.task.*` domain events despite the `dms.task.>` namespace
   being bound to the `WORKFLOW_EVENTS` stream and already subscribed
   by audit/SIEM/connector.
7. No rate-limit entry and no OpenAPI entry for `/api/v1/tasks`.

## Architecture

New Go microservice **`services/task`**, scaffolded like the document
service (`cmd/server` + `internal/{handler,service,repository,model}` +
`migrations/`), added to `go.work`, the Makefile service list,
docker-compose (own container + healthcheck), and both gateway configs.

- **Routing**: `deploy/gateway/routes.yaml` and `kong.yaml` repoint the
  existing `/api/v1/tasks` prefix from the document upstream to the
  task upstream. URL shape is unchanged, so mobile clients keep
  working. A rate-limit entry is added to `rate-limits.yaml`.
- **Database**: same shared Postgres instance, new tables owned by the
  task service with its own `task_schema_migrations` bookkeeping.
  RLS ENABLE + FORCE on every table with the standard
  `app.current_tenant` GUC; all queries via `database.WithTenantTx`.
- **No cross-service FKs**: `user_id` / `document_id` are stored as
  plain UUIDs (the workflow-service precedent). Linked documents store
  a `title` + `workspace_id` snapshot at link time so lists render
  without cross-service calls. Deleted documents are tolerated: the
  web UI renders a non-clickable chip if navigation 404s.
- **Events**: transactional outbox (per-service `outbox` table +
  polling publisher, exactly like the document service). Domain events
  publish under `dms.task.*.v1` (already bound to `WORKFLOW_EVENTS`);
  notification events under the existing `dms.notify.task.*.v1`.
- **Data migration**: a task-service migration copies rows from the
  legacy document-service `tasks` table (same database):
  `assignee_id` → one `task_assignees` row, `linked_document_id` → one
  `task_documents` row (title/workspace snapshot backfilled via join to
  `documents`). The legacy table is left in place, renamed
  `tasks_legacy_document_svc`, and dropped in a later release. The
  document service's task handler/service/repo/sweep code is removed
  in the same change that repoints the gateway.

## Data model (new tables, task service)

```sql
tasks (
  tenant_id   UUID NOT NULL,
  id          UUID NOT NULL DEFAULT gen_random_uuid(),
  title       TEXT NOT NULL CHECK (char_length(title) <= 200),
  description TEXT NOT NULL DEFAULT '',
  status      TEXT NOT NULL DEFAULT 'open'
              CHECK (status IN ('open','in_progress','done','cancelled')),
  priority    TEXT NOT NULL DEFAULT 'normal'
              CHECK (priority IN ('low','normal','high','urgent')),
  source      TEXT NOT NULL DEFAULT 'user' CHECK (source IN ('user','workflow')),
  due_at      TIMESTAMPTZ,
  created_by  UUID NOT NULL,
  completed_by UUID,
  completed_at TIMESTAMPTZ,
  due_soon_notified_at TIMESTAMPTZ,   -- sweep bookkeeping (as today)
  overdue_notified_at  TIMESTAMPTZ,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at  TIMESTAMPTZ,            -- soft delete
  PRIMARY KEY (tenant_id, id)
)

task_assignees (
  tenant_id UUID NOT NULL,
  task_id   UUID NOT NULL,
  user_id   UUID NOT NULL,
  added_by  UUID NOT NULL,
  added_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, task_id, user_id),
  FOREIGN KEY (tenant_id, task_id) REFERENCES tasks ON DELETE CASCADE
)

task_documents (
  tenant_id      UUID NOT NULL,
  task_id        UUID NOT NULL,
  document_id    UUID NOT NULL,
  workspace_id   UUID NOT NULL,      -- snapshot for building the doc URL
  title_snapshot TEXT NOT NULL,      -- snapshot for rendering chips
  linked_by      UUID NOT NULL,
  linked_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, task_id, document_id),
  FOREIGN KEY (tenant_id, task_id) REFERENCES tasks ON DELETE CASCADE
)

task_comments (
  tenant_id  UUID NOT NULL,
  id         UUID NOT NULL DEFAULT gen_random_uuid(),
  task_id    UUID NOT NULL,
  author_id  UUID NOT NULL,
  body       TEXT NOT NULL CHECK (char_length(body) <= 4000),
  mentions   UUID[] NOT NULL DEFAULT '{}',  -- parsed @mention user ids
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at TIMESTAMPTZ,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, task_id) REFERENCES tasks ON DELETE CASCADE
)

task_activity (
  tenant_id  UUID NOT NULL,
  id         BIGINT GENERATED ALWAYS AS IDENTITY,
  task_id    UUID NOT NULL,
  actor_id   UUID NOT NULL,
  action     TEXT NOT NULL,   -- created|updated|assigned|unassigned|status_changed|
                              -- document_linked|document_unlinked|commented|deleted
  detail     JSONB NOT NULL DEFAULT '{}',  -- e.g. {"user_id": …} / {"from":"open","to":"done"}
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, task_id) REFERENCES tasks ON DELETE CASCADE
)
```

Indexes: `task_assignees(tenant_id, user_id)` (my-tasks),
`tasks(tenant_id, status, due_at)` (list + sweep),
`tasks(tenant_id, created_by)` (created-by-me),
`task_documents(tenant_id, document_id)` (per-document panel),
`task_comments(tenant_id, task_id, created_at)`,
`task_activity(tenant_id, task_id, id)`.

Flat comments (no threading), no reactions — deliberate YAGNI; the
schema doesn't preclude adding them later.

## API

All under `/api/v1/tasks` (gateway → task service), session auth.
Response DTOs use snake_case JSON tags (repo convention).

| Method & path | Behavior |
|---|---|
| `POST /tasks` | Create. Body adds `assignee_ids: []` and `document_ids: []`. Invalid priority is rejected (400), no longer silently coerced. |
| `GET /tasks` | List, paginated (`limit` ≤ 100 default 50, `offset`), server-side filters: `filter=mine\|created\|all`, `status`, `priority`, `document_id`, `q` (title/description ILIKE), `show_completed`. Sort: `sort=due_at\|priority\|created_at`. |
| `GET /tasks/mine` | Compat shim ≡ `GET /tasks?filter=mine` (mobile uses it). |
| `GET /tasks/created` | Compat shim ≡ `filter=created`. Properly mounted this time. |
| `GET /tasks/{id}` | Full detail: task + assignees + documents + counts. |
| `PATCH /tasks/{id}` | title/description/priority/due_at. Creator or admin/owner. |
| `POST /tasks/{id}/assignees` | Add assignee `{user_id}`. Creator, existing assignee, or admin. |
| `DELETE /tasks/{id}/assignees/{userId}` | Remove assignee (same gate; self-removal always allowed). |
| `POST /tasks/{id}/documents` | Link document `{document_id}`. Task service calls document service (existing internal HTTP/gRPC read path) to snapshot title + workspace and confirm existence. |
| `DELETE /tasks/{id}/documents/{docId}` | Unlink. |
| `POST /tasks/{id}/start` | → `in_progress` (assignee/creator/admin). |
| `POST /tasks/{id}/complete` | → `done`, records `completed_by/at`. Any assignee, creator, or admin ("anyone completes"). |
| `POST /tasks/{id}/reopen` | done/cancelled → open. |
| `POST /tasks/{id}/cancel` | → `cancelled`. |
| `DELETE /tasks/{id}` | Soft delete. Creator or admin. |
| `GET /tasks/{id}/comments` | List (paginated, oldest first). |
| `POST /tasks/{id}/comments` | `{body}`; @mentions parsed server-side against tenant users. Any tenant member. |
| `PATCH /tasks/{id}/comments/{cid}` / `DELETE …` | Author only (delete also admin); soft delete. |
| `GET /tasks/{id}/activity` | Activity timeline (paginated, newest first). |

Legacy `POST /tasks/{id}/assign` / `unassign` are retired (web is
updated in the same release; mobile never used them).

## Permissions

- **See / list**: any authenticated tenant member (user decision).
- **Edit fields**: creator or admin/owner.
- **Status transitions**: any assignee, creator, or admin/owner.
- **Assignee & document management**: creator, current assignee, or
  admin/owner.
- **Comment**: any tenant member. Edit/delete own comments.
- **Delete task**: creator or admin/owner (soft).

## Events & notifications

Domain events (outbox → `WORKFLOW_EVENTS` via existing `dms.task.>`
binding; add subjects to `pkg/events/coverage.go` allowlist):

- `dms.task.created.v1`, `dms.task.updated.v1`,
  `dms.task.assigned.v1`, `dms.task.unassigned.v1`,
  `dms.task.status_changed.v1`, `dms.task.document_linked.v1`,
  `dms.task.comment.created.v1`, `dms.task.deleted.v1`

Audit and SIEM consume these with zero new code (they already
subscribe to `dms.task.>`).

Notifications (existing `dms.notify.>` path, DeliveryPayload shape):

- assigned → each newly-added assignee (skip self-assign) — as today
- due soon (≤24 h) → all assignees; overdue → assignees + creator —
  hourly sweep moves into the task service unchanged
- **new**: comment @mention → mentioned users
- **new**: completion → other assignees + creator ("X completed …")
- Notification prefs page gains ids `task.mention`; overdue reuses
  the existing `task.due` toggle (documented, closing today's gap).

## Web UI

- **Split the monolith**: `tasks.tsx` route keeps only the tab shell;
  new components under `web/src/components/tasks/`:
  `TaskList`, `TaskRow`, `TaskKanban`, `TaskDetailDrawer`,
  `TaskCreateDialog`, `AssigneePicker`, `DocumentPicker`,
  `TaskComments`, `TaskActivity`.
- **Task detail drawer** (opens on row/card click): full description,
  assignee avatar chips with add/remove (searchable picker reusing the
  mention-search users query), linked-document chips navigating to the
  document page, Start/Complete/Reopen/Cancel per permissions, comment
  thread with @mention autocomplete, activity timeline, delete.
- **Create dialog**: multi-assignee picker, document search picker
  (pre-filled when opened from a document), priority, due date.
- **Document detail page**: new **Tasks panel** (like the clause-matches
  panel): all tasks linked to the document with status/assignees, plus
  a pre-linked "New task" button. `AddToTaskDialog` is replaced by this
  flow; its cache-key bug dies with it.
- **List improvements**: server-side pagination/filtering; "Start"
  action makes `in_progress` reachable; kanban stays (no drag-drop —
  out of scope).
- Query keys consolidated: `['tasks', filters…]` family with correct
  invalidation everywhere (topbar badge, dashboard card, panels).
- Mobile: untouched this release (compat shims keep it working).

## Testing

- Task service: service-layer unit tests (permission matrix, status
  transitions, anyone-completes semantics, mention parsing), handler
  tests, testcontainers integration tests (RLS isolation, FORCE RLS
  posture per the TestProdPosture conventions, legacy-data copy
  migration).
- `pkg/events` coverage test updated for the new subjects.
- Gateway guard tests updated for the repointed route (routing-drift
  protection).
- Web: vitest for drawer/pickers/panel; Playwright `50-tasks.spec.ts`
  extended (create with 2 assignees + 1 document, complete from the
  other assignee, comment with mention, document panel shows task).

## Out of scope (explicit)

- Subtasks/checklists, labels, recurrence, saved views, kanban
  drag-drop, attachments-as-files (documents ARE the attachments),
  watchers (superseded by multi-assignee + mentions), real-time
  WebSocket updates (30 s polling stays), Approvals-tab/workflow
  changes, mobile UI changes, per-document permission-based task
  visibility.
