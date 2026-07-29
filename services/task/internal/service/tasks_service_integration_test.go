//go:build integration
// +build integration

// Task 4 (2026-07-28 task-service design) — service-layer integration
// tests for core CRUD (CreateTask/GetTask/ListTasks/UpdateTask/
// DeleteTask), exercising the real Postgres schema (both migration
// chains) instead of mocking the repository layer, so the activity-row
// and outbox-event side effects that only exist as a byproduct of the
// real SQL (RETURNING id/created_at, FK ordering, etc.) are covered
// too.
//
// External test package (service_test), not service — same pattern as
// the document service's external_key_upsert_test.go — so this file can
// import both internal/service and internal/repository without an
// import cycle, and its own fixture builds a *service.TaskService
// against the dms_app (NOBYPASSRLS) pool the same way
// repository.setupTaskDB does (that helper itself is unexported and
// lives in repository's _test.go files, which are not part of the
// importable package — Go test files never cross package boundaries —
// so it's re-derived here rather than literally called).
//
// Run with:
//
//	go test -tags integration -race -run TestTaskService ./services/task/internal/service/
package service_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/task/internal/service"
)

const (
	documentMigrationsDir = "../../../document/migrations"
	taskMigrationsDir     = "../../migrations"
)

// taskServiceFixture starts an isolated Postgres testcontainer, runs the
// document service's migration chain followed by the task service's own
// chain, provisions the dms_app NOBYPASSRLS role (mirrors
// repository.setupTaskDB / pkg/database/rls_integration_test.go), and
// returns a TaskService wired against that NOBYPASSRLS pool — so these
// tests exercise the same RLS posture production traffic does, not a
// BYPASSRLS shortcut. superPool (the testcontainer's default superuser)
// is returned too, for out-of-band seeding and for assertions that read
// activity/outbox rows directly.
func taskServiceFixture(t *testing.T) (context.Context, *service.TaskService, *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)

	dsn, pgCleanup, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(pgCleanup)

	poolCfg := database.DefaultPoolConfig()
	poolCfg.SkipRLSPostureCheck = true
	superPool, err := database.NewPool(ctx, dsn, poolCfg)
	require.NoError(t, err)
	t.Cleanup(superPool.Close)

	// The document service's NER/OCR migrations (000021+) ALTER
	// document_entities, owned by the intelligence service's migration
	// set. Pre-create it so a single-service run of document's migration
	// chain reaches completion (same workaround the repository package's
	// newSchemaFixture and document's external_key_upsert_test.go use).
	_, err = superPool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS document_entities (
			tenant_id    UUID NOT NULL,
			id           UUID NOT NULL DEFAULT gen_random_uuid(),
			version_id   UUID NOT NULL,
			document_id  UUID NOT NULL,
			entity_type  TEXT NOT NULL,
			entity_value TEXT NOT NULL,
			start_offset INT  NOT NULL DEFAULT 0,
			end_offset   INT  NOT NULL DEFAULT 0,
			confidence   REAL NOT NULL DEFAULT 0,
			is_pii       BOOLEAN NOT NULL DEFAULT false,
			detected_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (tenant_id, id)
		)`)
	require.NoError(t, err)

	require.NoError(t, database.RunMigrations(dsn, documentMigrationsDir))
	require.NoError(t, database.RunServiceMigrations(dsn, taskMigrationsDir, "task"))

	_, err = superPool.Exec(ctx, `
		DO $$ BEGIN
		  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'dms_app') THEN
		    CREATE ROLE dms_app LOGIN PASSWORD 'devpassword' NOBYPASSRLS;
		  END IF;
		END $$;
		GRANT USAGE ON SCHEMA public TO dms_app;
		GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dms_app;
		GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO dms_app;
	`)
	require.NoError(t, err)

	appDSN, err := testutil.RewriteDSNUser(dsn, "dms_app", "devpassword")
	require.NoError(t, err)
	appPool, err := database.NewPool(ctx, appDSN, database.DefaultPoolConfig())
	require.NoError(t, err)
	t.Cleanup(appPool.Close)

	svc := service.New(appPool, zerolog.Nop())
	return ctx, svc, superPool
}

// seedOrgAndUser inserts an organization + one user via superPool
// (bypasses RLS). Ported from repository/schema_integration_test.go.
func seedOrgAndUser(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tenant, user uuid.UUID, role string) {
	t.Helper()
	_, err := pool.Exec(ctx, `
		INSERT INTO organizations (id, name, slug) VALUES ($1, 'Test Org', $2)`,
		tenant, "t-"+tenant.String())
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO users (tenant_id, id, email, display_name, role)
		VALUES ($1, $2, $3, 'Test User', $4)`,
		tenant, user, "u-"+user.String()+"@test.local", role)
	require.NoError(t, err)
}

// seedUser inserts one more user row for an already-seeded tenant.
func seedUser(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tenant, user uuid.UUID, role string) {
	t.Helper()
	_, err := pool.Exec(ctx, `
		INSERT INTO users (tenant_id, id, email, display_name, role)
		VALUES ($1, $2, $3, 'Test User', $4)`,
		tenant, user, "u-"+user.String()+"@test.local", role)
	require.NoError(t, err)
}

// seedDocument inserts the minimal parent chain (workspace, root folder)
// plus a documents row, mirroring repository/schema_integration_test.go.
func seedDocument(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tenant uuid.UUID, title string) uuid.UUID {
	t.Helper()
	wsID := uuid.Must(uuid.NewV7())
	folderID := uuid.Must(uuid.NewV7())
	docID := uuid.Must(uuid.NewV7())

	_, err := pool.Exec(ctx, `
		INSERT INTO workspaces (tenant_id, id, name) VALUES ($1, $2, 'ws')`,
		tenant, wsID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO folders (tenant_id, id, workspace_id, name, path, depth)
		VALUES ($1, $2, $3, 'root', 'root', 0)`,
		tenant, folderID, wsID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO documents
			(tenant_id, id, workspace_id, folder_id, title, description, region_pin, sha256_hash, mime_type)
		VALUES ($1, $2, $3, $4, $5, '', 'us-east-1', '', '')`,
		tenant, docID, wsID, folderID, title)
	require.NoError(t, err)
	return docID
}

// callerCtx builds a ctx carrying auth.UserInfo the way middleware.SessionAuth
// would after a successful session lookup.
func callerCtx(ctx context.Context, tenant, user uuid.UUID, role string) context.Context {
	return auth.WithUser(ctx, auth.UserInfo{ID: user, TenantID: tenant, Role: role})
}

func outboxEventTypes(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tenant uuid.UUID) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT event_type FROM outbox WHERE tenant_id = $1 ORDER BY created_at`, tenant)
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var et string
		require.NoError(t, rows.Scan(&et))
		out = append(out, et)
	}
	require.NoError(t, rows.Err())
	return out
}

func activityActions(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tenant, taskID uuid.UUID) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT action FROM task_activity WHERE tenant_id = $1 AND task_id = $2 ORDER BY id`, tenant, taskID)
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		require.NoError(t, rows.Scan(&a))
		out = append(out, a)
	}
	require.NoError(t, rows.Err())
	return out
}

func countStr(items []string, want string) int {
	n := 0
	for _, s := range items {
		if s == want {
			n++
		}
	}
	return n
}

// outboxPayloads returns the JSON payload of every outbox row of
// eventType for tenant, oldest first, decoded into a generic map — used
// by the Task-5 tests below to inspect notify fan-out (user_ids) and
// status_changed detail without needing typed payload structs.
func outboxPayloads(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tenant uuid.UUID, eventType string) []map[string]any {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT payload FROM outbox WHERE tenant_id = $1 AND event_type = $2 ORDER BY created_at`, tenant, eventType)
	require.NoError(t, err)
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		require.NoError(t, rows.Scan(&raw))
		var m map[string]any
		require.NoError(t, json.Unmarshal(raw, &m))
		out = append(out, m)
	}
	require.NoError(t, rows.Err())
	return out
}

// activityDetails returns the JSON detail of every task_activity row for
// (tenant, taskID, action), oldest first, decoded into a generic map.
func activityDetails(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tenant, taskID uuid.UUID, action string) []map[string]any {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT detail FROM task_activity WHERE tenant_id = $1 AND task_id = $2 AND action = $3 ORDER BY id`, tenant, taskID, action)
	require.NoError(t, err)
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		require.NoError(t, rows.Scan(&raw))
		var m map[string]any
		require.NoError(t, json.Unmarshal(raw, &m))
		out = append(out, m)
	}
	require.NoError(t, rows.Err())
	return out
}

// stringSlice converts a decoded JSON array (dynamically typed as
// []any/[]interface{} by encoding/json) into []string, for asserting
// against a payload's "user_ids" field with require.ElementsMatch.
func stringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, len(arr))
	for i, e := range arr {
		out[i], _ = e.(string)
	}
	return out
}

// TestCreateTask_TwoAssigneesOneDocument_ActivityAndOutbox covers the
// brief's step-3 headline scenario: create with 2 assignees (both
// distinct from the creator) + 1 document. Activity must carry
// created+2×assigned; outbox must carry created.v1 + 2×assigned.v1 +
// 2×notify.assigned.v1 (one notify per non-creator assignee).
func TestCreateTask_TwoAssigneesOneDocument_ActivityAndOutbox(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	assignee1 := uuid.Must(uuid.NewV7())
	assignee2 := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator, "member")
	seedUser(ctx, t, superPool, tenant, assignee1, "member")
	seedUser(ctx, t, superPool, tenant, assignee2, "member")
	docID := seedDocument(ctx, t, superPool, tenant, "Contract v1")

	cctx := callerCtx(ctx, tenant, creator, "member")
	task, err := svc.CreateTask(cctx, service.CreateTaskInput{
		Title:       "Review contract",
		AssigneeIDs: []uuid.UUID{assignee1, assignee2},
		DocumentIDs: []uuid.UUID{docID},
	})
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, task.ID)
	require.Equal(t, "open", task.Status)
	require.Equal(t, "normal", task.Priority, "empty priority must default to normal")
	require.Equal(t, "user", task.Source, "empty source must default to user")
	require.Len(t, task.Assignees, 2)
	require.Len(t, task.Documents, 1)
	require.Equal(t, docID, task.Documents[0].DocumentID)
	require.Equal(t, "Contract v1", task.Documents[0].TitleSnapshot)

	acts := activityActions(ctx, t, superPool, tenant, task.ID)
	require.Equal(t, 1, countStr(acts, "created"), "exactly one created activity row")
	require.Equal(t, 2, countStr(acts, "assigned"), "one assigned activity row per assignee")
	require.Len(t, acts, 3)

	events := outboxEventTypes(ctx, t, superPool, tenant)
	require.Equal(t, 1, countStr(events, "dms.task.created.v1"))
	require.Equal(t, 2, countStr(events, "dms.task.assigned.v1"))
	require.Equal(t, 2, countStr(events, "dms.notify.task.assigned.v1"), "one notify per non-creator assignee")
}

// TestCreateTask_AssigneeIsCreator_NoSelfNotify: when the creator
// assigns the task to themselves, they still get the domain
// dms.task.assigned.v1 event (and an assigned activity row) but NOT a
// dms.notify.task.assigned.v1 — self-assignment shouldn't ping the actor.
func TestCreateTask_AssigneeIsCreator_NoSelfNotify(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator, "member")

	cctx := callerCtx(ctx, tenant, creator, "member")
	task, err := svc.CreateTask(cctx, service.CreateTaskInput{
		Title:       "Self-assigned task",
		AssigneeIDs: []uuid.UUID{creator},
	})
	require.NoError(t, err)
	require.Len(t, task.Assignees, 1)

	events := outboxEventTypes(ctx, t, superPool, tenant)
	require.Equal(t, 1, countStr(events, "dms.task.created.v1"))
	require.Equal(t, 1, countStr(events, "dms.task.assigned.v1"))
	require.Equal(t, 0, countStr(events, "dms.notify.task.assigned.v1"), "creator assigning themselves must not self-notify")
}

// TestCreateTask_InvalidPriority covers the brief's explicit "no silent
// coercion" rule: an unrecognized priority must fail the whole create,
// not fall back to "normal".
func TestCreateTask_InvalidPriority(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator, "member")

	cctx := callerCtx(ctx, tenant, creator, "member")
	_, err := svc.CreateTask(cctx, service.CreateTaskInput{Title: "Bad priority", Priority: "urgentz"})
	require.ErrorIs(t, err, service.ErrValidation)

	// No task, no activity, no outbox event must have leaked out of the
	// aborted transaction.
	var taskCount int
	require.NoError(t, superPool.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE tenant_id = $1`, tenant).Scan(&taskCount))
	require.Zero(t, taskCount)
}

// TestCreateTask_MissingTitle covers the required/≤200-chars rule.
func TestCreateTask_MissingTitle(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator, "member")

	cctx := callerCtx(ctx, tenant, creator, "member")
	_, err := svc.CreateTask(cctx, service.CreateTaskInput{Title: "   "})
	require.ErrorIs(t, err, service.ErrValidation)
}

// TestCreateTask_UnknownDocument_RollsBackWholeCreate covers "links each
// document (ErrValidation if any document id unknown)" — the entire
// create must roll back atomically, leaving no orphaned task/assignee
// rows even though the task row itself was briefly inserted mid-tx.
func TestCreateTask_UnknownDocument_RollsBackWholeCreate(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator, "member")
	bogusDoc := uuid.Must(uuid.NewV7())

	cctx := callerCtx(ctx, tenant, creator, "member")
	_, err := svc.CreateTask(cctx, service.CreateTaskInput{Title: "Doc-linked task", DocumentIDs: []uuid.UUID{bogusDoc}})
	require.ErrorIs(t, err, service.ErrValidation)

	var taskCount int
	require.NoError(t, superPool.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE tenant_id = $1`, tenant).Scan(&taskCount))
	require.Zero(t, taskCount, "an unknown document id must roll back the whole create, not leave a docless task behind")
}

// TestCreateTask_UnknownAssignee_RollsBackWholeCreate mirrors
// TestCreateTask_UnknownDocument_RollsBackWholeCreate for the other
// foreign reference CreateTask accepts: a bogus assignee id must fail
// validation before any row (task, task_assignees, activity, outbox) is
// left behind, not silently insert a task_assignees row nobody can ever
// see and fan out events/notifications to no one.
func TestCreateTask_UnknownAssignee_RollsBackWholeCreate(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator, "member")
	bogusAssignee := uuid.Must(uuid.NewV7())

	cctx := callerCtx(ctx, tenant, creator, "member")
	_, err := svc.CreateTask(cctx, service.CreateTaskInput{Title: "Assignee-linked task", AssigneeIDs: []uuid.UUID{bogusAssignee}})
	require.ErrorIs(t, err, service.ErrValidation)

	var taskCount int
	require.NoError(t, superPool.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE tenant_id = $1`, tenant).Scan(&taskCount))
	require.Zero(t, taskCount, "an unknown assignee id must roll back the whole create, not leave a task behind")

	events := outboxEventTypes(ctx, t, superPool, tenant)
	require.Empty(t, events, "no outbox rows must survive an aborted create")
}

// TestCreateTask_CrossTenantAssignee_IsValidationError covers the sharper
// case: a *real* user id, just not one that belongs to the caller's
// tenant. validateAssigneesExist scopes its SELECT by tenant_id, so this
// must be rejected exactly like a wholly bogus uuid.
func TestCreateTask_CrossTenantAssignee_IsValidationError(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	otherTenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	outsider := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator, "member")
	seedOrgAndUser(ctx, t, superPool, otherTenant, outsider, "member")

	cctx := callerCtx(ctx, tenant, creator, "member")
	_, err := svc.CreateTask(cctx, service.CreateTaskInput{Title: "Cross-tenant assignee", AssigneeIDs: []uuid.UUID{outsider}})
	require.ErrorIs(t, err, service.ErrValidation, "a real user id from a different tenant must still be rejected")
}

// TestUpdateTask_StrangerForbidden covers canEditFields: a tenant member
// who is neither the creator nor an admin must not be able to edit.
func TestUpdateTask_StrangerForbidden(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	stranger := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator, "member")
	seedUser(ctx, t, superPool, tenant, stranger, "member")

	creatorCtx := callerCtx(ctx, tenant, creator, "member")
	task, err := svc.CreateTask(creatorCtx, service.CreateTaskInput{Title: "Original title"})
	require.NoError(t, err)

	strangerCtx := callerCtx(ctx, tenant, stranger, "member")
	newTitle := "Hijacked title"
	_, err = svc.UpdateTask(strangerCtx, service.UpdateTaskInput{ID: task.ID, Title: &newTitle})
	require.ErrorIs(t, err, service.ErrForbidden)

	// Confirm nothing actually changed.
	got, err := svc.GetTask(creatorCtx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "Original title", got.Title)
}

// TestUpdateTask_CreatorCanEdit_RecordsActivityAndEvent covers the
// happy path: creator patches title+priority, gets one `updated`
// activity row naming both changed fields, and one dms.task.updated.v1
// event.
func TestUpdateTask_CreatorCanEdit_RecordsActivityAndEvent(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator, "member")

	cctx := callerCtx(ctx, tenant, creator, "member")
	task, err := svc.CreateTask(cctx, service.CreateTaskInput{Title: "Draft"})
	require.NoError(t, err)

	newTitle := "Final"
	newPriority := "high"
	updated, err := svc.UpdateTask(cctx, service.UpdateTaskInput{ID: task.ID, Title: &newTitle, Priority: &newPriority})
	require.NoError(t, err)
	require.Equal(t, "Final", updated.Title)
	require.Equal(t, "high", updated.Priority)

	acts := activityActions(ctx, t, superPool, tenant, task.ID)
	require.Equal(t, 1, countStr(acts, "updated"))

	events := outboxEventTypes(ctx, t, superPool, tenant)
	require.Equal(t, 1, countStr(events, "dms.task.updated.v1"))
}

// TestUpdateTask_IdenticalDueAt_IsNoOp covers the fix for a PATCH that
// re-sends the task's current due_at: it must not be treated as a
// change. Before the fix, resending the same due_at still recorded an
// `updated` activity row, emitted dms.task.updated.v1, and reset the
// sweep's single-shot reminded_at/overdue_notified_at flags — which
// would re-arm a reminder the sweep had already fired.
func TestUpdateTask_IdenticalDueAt_IsNoOp(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator, "member")

	cctx := callerCtx(ctx, tenant, creator, "member")
	due := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Microsecond)
	task, err := svc.CreateTask(cctx, service.CreateTaskInput{Title: "Has a due date", DueAt: &due})
	require.NoError(t, err)
	require.NotNil(t, task.DueAt)

	// Simulate the sweep (Task 6) having already reminded once — this is
	// exactly the state a spuriously-"changed" due_at branch would
	// clobber, so seed it directly via SQL rather than waiting on the
	// sweep to exist.
	remindedAt := time.Now().UTC().Add(-1 * time.Hour).Truncate(time.Microsecond)
	_, err = superPool.Exec(ctx, `UPDATE tasks SET reminded_at = $1 WHERE tenant_id = $2 AND id = $3`, remindedAt, tenant, task.ID)
	require.NoError(t, err)

	sameDue := due
	updated, err := svc.UpdateTask(cctx, service.UpdateTaskInput{ID: task.ID, DueAt: &sameDue})
	require.NoError(t, err)
	require.NotNil(t, updated.DueAt)
	require.True(t, updated.DueAt.Equal(due))

	acts := activityActions(ctx, t, superPool, tenant, task.ID)
	require.Zero(t, countStr(acts, "updated"), "resending the identical due_at must not record an updated activity row")

	events := outboxEventTypes(ctx, t, superPool, tenant)
	require.Zero(t, countStr(events, "dms.task.updated.v1"), "resending the identical due_at must not emit dms.task.updated.v1")

	var gotReminded time.Time
	require.NoError(t, superPool.QueryRow(ctx, `SELECT reminded_at FROM tasks WHERE tenant_id = $1 AND id = $2`, tenant, task.ID).Scan(&gotReminded))
	require.WithinDuration(t, remindedAt, gotReminded, time.Millisecond, "reminded_at must be preserved when due_at doesn't actually change")
}

// TestUpdateTask_InvalidPriority mirrors CreateTask's validation rule on
// the patch path.
func TestUpdateTask_InvalidPriority(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator, "member")

	cctx := callerCtx(ctx, tenant, creator, "member")
	task, err := svc.CreateTask(cctx, service.CreateTaskInput{Title: "Task"})
	require.NoError(t, err)

	bad := "urgentz"
	_, err = svc.UpdateTask(cctx, service.UpdateTaskInput{ID: task.ID, Priority: &bad})
	require.ErrorIs(t, err, service.ErrValidation)
}

// TestUpdateTask_AdminCanEditAnyonesTask covers isAdmin's role in
// canEditFields: an admin who is neither creator nor assignee may still
// edit.
func TestUpdateTask_AdminCanEditAnyonesTask(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	admin := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator, "member")
	seedUser(ctx, t, superPool, tenant, admin, "admin")

	creatorCtx := callerCtx(ctx, tenant, creator, "member")
	task, err := svc.CreateTask(creatorCtx, service.CreateTaskInput{Title: "Needs admin touch"})
	require.NoError(t, err)

	adminCtx := callerCtx(ctx, tenant, admin, "admin")
	newTitle := "Edited by admin"
	updated, err := svc.UpdateTask(adminCtx, service.UpdateTaskInput{ID: task.ID, Title: &newTitle})
	require.NoError(t, err)
	require.Equal(t, "Edited by admin", updated.Title)
}

// TestDeleteTask_StrangerForbidden_CreatorSucceeds covers DeleteTask's
// gate plus its activity/event side effects.
func TestDeleteTask_StrangerForbidden_CreatorSucceeds(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	stranger := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator, "member")
	seedUser(ctx, t, superPool, tenant, stranger, "member")

	creatorCtx := callerCtx(ctx, tenant, creator, "member")
	task, err := svc.CreateTask(creatorCtx, service.CreateTaskInput{Title: "To be deleted"})
	require.NoError(t, err)

	strangerCtx := callerCtx(ctx, tenant, stranger, "member")
	err = svc.DeleteTask(strangerCtx, task.ID)
	require.ErrorIs(t, err, service.ErrForbidden)

	require.NoError(t, svc.DeleteTask(creatorCtx, task.ID))

	_, err = svc.GetTask(creatorCtx, task.ID)
	require.ErrorIs(t, err, service.ErrNotFound)

	acts := activityActions(ctx, t, superPool, tenant, task.ID)
	require.Equal(t, 1, countStr(acts, "deleted"))

	events := outboxEventTypes(ctx, t, superPool, tenant)
	require.Equal(t, 1, countStr(events, "dms.task.deleted.v1"))
}

// TestGetTask_AnyTenantMemberCanView covers the brief's "any tenant
// member can Get/List (no per-row gate)" visibility rule: a stranger
// (neither creator nor assignee nor admin) can still read the task.
func TestGetTask_AnyTenantMemberCanView(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	stranger := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator, "member")
	seedUser(ctx, t, superPool, tenant, stranger, "member")

	creatorCtx := callerCtx(ctx, tenant, creator, "member")
	task, err := svc.CreateTask(creatorCtx, service.CreateTaskInput{Title: "Visible to all"})
	require.NoError(t, err)

	strangerCtx := callerCtx(ctx, tenant, stranger, "member")
	got, err := svc.GetTask(strangerCtx, task.ID)
	require.NoError(t, err, "any tenant member must be able to view a task")
	require.Equal(t, task.ID, got.ID)
}

// TestListTasks_FilterMineAndCreated covers the brief's filter mapping:
// "mine" -> AssigneeID=caller, "created" -> CreatedBy=caller, "all"/"" ->
// neither.
func TestListTasks_FilterMineAndCreated(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	assignee := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator, "member")
	seedUser(ctx, t, superPool, tenant, assignee, "member")

	creatorCtx := callerCtx(ctx, tenant, creator, "member")
	_, err := svc.CreateTask(creatorCtx, service.CreateTaskInput{Title: "Created by creator"})
	require.NoError(t, err)
	assignedToAssignee, err := svc.CreateTask(creatorCtx, service.CreateTaskInput{
		Title: "Assigned to assignee", AssigneeIDs: []uuid.UUID{assignee},
	})
	require.NoError(t, err)

	assigneeCtx := callerCtx(ctx, tenant, assignee, "member")
	minePage, err := svc.ListTasks(assigneeCtx, service.ListTasksInput{Filter: "mine"})
	require.NoError(t, err)
	require.Equal(t, 1, minePage.Total)
	require.Len(t, minePage.Items, 1)
	require.Equal(t, assignedToAssignee.ID, minePage.Items[0].ID)

	createdPage, err := svc.ListTasks(creatorCtx, service.ListTasksInput{Filter: "created"})
	require.NoError(t, err)
	require.Equal(t, 2, createdPage.Total, "creator created both tasks")

	allPage, err := svc.ListTasks(creatorCtx, service.ListTasksInput{Filter: "all"})
	require.NoError(t, err)
	require.Equal(t, 2, allPage.Total)
	require.Equal(t, 50, allPage.Limit, "unset Limit must normalize to the repository default")

	assigneeCreatedPage, err := svc.ListTasks(assigneeCtx, service.ListTasksInput{Filter: "created"})
	require.NoError(t, err)
	require.Zero(t, assigneeCreatedPage.Total, "assignee created neither task")
}

// TestListTasks_InvalidFilterIsValidationError covers the "filter must
// be mine|created|all" branch.
func TestListTasks_InvalidFilterIsValidationError(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator, "member")

	cctx := callerCtx(ctx, tenant, creator, "member")
	_, err := svc.ListTasks(cctx, service.ListTasksInput{Filter: "bogus"})
	require.ErrorIs(t, err, service.ErrValidation)
}

// ---- Task 5: status transitions, assignee & document management --------

// TestCompleteTask_ByAssignee_NotifiesCreatorAndOtherAssigneeOnly covers
// the brief's headline completion scenario: assignee B (not the
// creator) completes the task. Status moves to done, CompletedBy/At are
// stamped, a status_changed activity+event fire, and the completion
// notify goes to (assignees ∪ creator) − actor — i.e. creator A and
// other-assignee C, but NOT B (the actor who completed it).
func TestCompleteTask_ByAssignee_NotifiesCreatorAndOtherAssigneeOnly(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	creatorA := uuid.Must(uuid.NewV7())
	assigneeB := uuid.Must(uuid.NewV7())
	assigneeC := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creatorA, "member")
	seedUser(ctx, t, superPool, tenant, assigneeB, "member")
	seedUser(ctx, t, superPool, tenant, assigneeC, "member")

	creatorCtx := callerCtx(ctx, tenant, creatorA, "member")
	task, err := svc.CreateTask(creatorCtx, service.CreateTaskInput{
		Title: "Ship the report", AssigneeIDs: []uuid.UUID{assigneeB, assigneeC},
	})
	require.NoError(t, err)

	bCtx := callerCtx(ctx, tenant, assigneeB, "member")
	done, err := svc.CompleteTask(bCtx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "done", done.Status)
	require.NotNil(t, done.CompletedBy)
	require.Equal(t, assigneeB, *done.CompletedBy)
	require.NotNil(t, done.CompletedAt)

	acts := activityActions(ctx, t, superPool, tenant, task.ID)
	require.Equal(t, 1, countStr(acts, "status_changed"))
	details := activityDetails(ctx, t, superPool, tenant, task.ID, "status_changed")
	require.Len(t, details, 1)
	require.Equal(t, "open", details[0]["from"])
	require.Equal(t, "done", details[0]["to"])

	events := outboxEventTypes(ctx, t, superPool, tenant)
	require.Equal(t, 1, countStr(events, "dms.task.status_changed.v1"))
	require.Equal(t, 1, countStr(events, "dms.notify.task.completed.v1"))

	notifies := outboxPayloads(ctx, t, superPool, tenant, "dms.notify.task.completed.v1")
	require.Len(t, notifies, 1)
	require.Equal(t, "task.completed", notifies[0]["type"])
	require.Equal(t, "Task completed", notifies[0]["title"])
	require.Equal(t, task.Title, notifies[0]["body"])
	require.ElementsMatch(t, []string{creatorA.String(), assigneeC.String()}, stringSlice(notifies[0]["user_ids"]),
		"completion notify must reach creator + other assignee, and must exclude the actor (assigneeB) who completed it")
}

// TestCompleteTask_StrangerForbidden covers canTransition's gate: a
// tenant member who is neither creator, assignee, nor admin cannot
// complete a task.
func TestCompleteTask_StrangerForbidden(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	stranger := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator, "member")
	seedUser(ctx, t, superPool, tenant, stranger, "member")

	creatorCtx := callerCtx(ctx, tenant, creator, "member")
	task, err := svc.CreateTask(creatorCtx, service.CreateTaskInput{Title: "Not yours to complete"})
	require.NoError(t, err)

	strangerCtx := callerCtx(ctx, tenant, stranger, "member")
	_, err = svc.CompleteTask(strangerCtx, task.ID)
	require.ErrorIs(t, err, service.ErrForbidden)

	got, err := svc.GetTask(creatorCtx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "open", got.Status, "a forbidden complete attempt must not change status")
}

// TestReopenTask_ClearsCompletedBy covers Reopen's done->open path:
// CompletedBy/CompletedAt must be cleared, and a second status_changed
// activity+event row must be recorded for the open transition.
func TestReopenTask_ClearsCompletedBy(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	assignee := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator, "member")
	seedUser(ctx, t, superPool, tenant, assignee, "member")

	// An assignee besides the creator/actor gives CompleteTask a non-empty
	// completion-notify recipient list (creator completing their own
	// task with zero other stakeholders would otherwise emit no notify
	// at all, since emitNotify no-ops on an empty user list) — needed so
	// this test can actually assert "exactly one, not a duplicate on
	// reopen" below.
	cctx := callerCtx(ctx, tenant, creator, "member")
	task, err := svc.CreateTask(cctx, service.CreateTaskInput{Title: "Round trip", AssigneeIDs: []uuid.UUID{assignee}})
	require.NoError(t, err)

	done, err := svc.CompleteTask(cctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, done.CompletedBy)
	require.NotNil(t, done.CompletedAt)

	reopened, err := svc.ReopenTask(cctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "open", reopened.Status)
	require.Nil(t, reopened.CompletedBy, "reopen must clear completed_by")
	require.Nil(t, reopened.CompletedAt, "reopen must clear completed_at")

	acts := activityActions(ctx, t, superPool, tenant, task.ID)
	require.Equal(t, 2, countStr(acts, "status_changed"), "one status_changed for complete, one for reopen")

	details := activityDetails(ctx, t, superPool, tenant, task.ID, "status_changed")
	require.Len(t, details, 2)
	require.Equal(t, "open", details[0]["from"])
	require.Equal(t, "done", details[0]["to"])
	require.Equal(t, "done", details[1]["from"])
	require.Equal(t, "open", details[1]["to"])

	events := outboxEventTypes(ctx, t, superPool, tenant)
	require.Equal(t, 2, countStr(events, "dms.task.status_changed.v1"))
	require.Equal(t, 1, countStr(events, "dms.notify.task.completed.v1"), "reopen must not itself emit a completion notify")
}

// TestReopenTask_IllegalFromOpen_IsValidationError covers doTransition's
// message-naming-both-states rule via a live call (the exhaustive matrix
// itself is pinned down by TestValidTransition_Matrix): reopening an
// already-open task is illegal (open isn't in {done,cancelled}).
func TestReopenTask_IllegalFromOpen_IsValidationError(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator, "member")

	cctx := callerCtx(ctx, tenant, creator, "member")
	task, err := svc.CreateTask(cctx, service.CreateTaskInput{Title: "Already open"})
	require.NoError(t, err)

	_, err = svc.ReopenTask(cctx, task.ID)
	require.ErrorIs(t, err, service.ErrValidation)
}

// TestRemoveAssignee_SelfRemovalAndPeerRemoval_PlainAssigneeWorks
// covers RemoveAssignee's gate: canManageLinks OR userID == actor. A
// plain assignee (not creator, not admin) can remove themselves, and —
// because canManageLinks already grants any assignee link-management
// rights — can also remove a fellow assignee.
func TestRemoveAssignee_SelfRemovalAndPeerRemoval_PlainAssigneeWorks(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	assigneeB := uuid.Must(uuid.NewV7())
	assigneeC := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator, "member")
	seedUser(ctx, t, superPool, tenant, assigneeB, "member")
	seedUser(ctx, t, superPool, tenant, assigneeC, "member")

	creatorCtx := callerCtx(ctx, tenant, creator, "member")
	task, err := svc.CreateTask(creatorCtx, service.CreateTaskInput{
		Title: "Multi-assignee", AssigneeIDs: []uuid.UUID{assigneeB, assigneeC},
	})
	require.NoError(t, err)

	bCtx := callerCtx(ctx, tenant, assigneeB, "member")

	// B removes C (a peer, not itself) — allowed because being an
	// assignee already satisfies canManageLinks.
	afterPeerRemoval, err := svc.RemoveAssignee(bCtx, task.ID, assigneeC)
	require.NoError(t, err)
	require.Len(t, afterPeerRemoval.Assignees, 1)
	require.Equal(t, assigneeB, afterPeerRemoval.Assignees[0].UserID)

	// B removes itself — allowed via the explicit userID == actor clause.
	afterSelfRemoval, err := svc.RemoveAssignee(bCtx, task.ID, assigneeB)
	require.NoError(t, err)
	require.Empty(t, afterSelfRemoval.Assignees)

	acts := activityActions(ctx, t, superPool, tenant, task.ID)
	require.Equal(t, 2, countStr(acts, "unassigned"))

	events := outboxEventTypes(ctx, t, superPool, tenant)
	require.Equal(t, 2, countStr(events, "dms.task.unassigned.v1"))
}

// TestRemoveAssignee_StrangerForbidden covers the negative case: a
// tenant member who is neither creator, assignee, nor admin, and is not
// removing themselves, cannot remove an assignee.
func TestRemoveAssignee_StrangerForbidden(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	assignee := uuid.Must(uuid.NewV7())
	stranger := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator, "member")
	seedUser(ctx, t, superPool, tenant, assignee, "member")
	seedUser(ctx, t, superPool, tenant, stranger, "member")

	creatorCtx := callerCtx(ctx, tenant, creator, "member")
	task, err := svc.CreateTask(creatorCtx, service.CreateTaskInput{
		Title: "Guarded", AssigneeIDs: []uuid.UUID{assignee},
	})
	require.NoError(t, err)

	strangerCtx := callerCtx(ctx, tenant, stranger, "member")
	_, err = svc.RemoveAssignee(strangerCtx, task.ID, assignee)
	require.ErrorIs(t, err, service.ErrForbidden)

	got, err := svc.GetTask(creatorCtx, task.ID)
	require.NoError(t, err)
	require.Len(t, got.Assignees, 1, "a forbidden removal attempt must not change assignees")
}

// TestRemoveAssignee_NotAnAssignee_IsValidationError covers the
// "removing someone who isn't currently assigned" branch: creator (who
// passes the gate) tries to remove a user who was never assigned.
func TestRemoveAssignee_NotAnAssignee_IsValidationError(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	notAssigned := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator, "member")
	seedUser(ctx, t, superPool, tenant, notAssigned, "member")

	cctx := callerCtx(ctx, tenant, creator, "member")
	task, err := svc.CreateTask(cctx, service.CreateTaskInput{Title: "No assignees here"})
	require.NoError(t, err)

	_, err = svc.RemoveAssignee(cctx, task.ID, notAssigned)
	require.ErrorIs(t, err, service.ErrValidation)
}

// TestAddAssignee_Idempotent_NoDuplicateActivityOrEvent covers
// AddAssignee's idempotency rule: re-adding an already-current assignee
// must not record a second `assigned` activity row, dms.task.assigned.v1
// event, or dms.notify.task.assigned.v1 notify.
func TestAddAssignee_Idempotent_NoDuplicateActivityOrEvent(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	newAssignee := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator, "member")
	seedUser(ctx, t, superPool, tenant, newAssignee, "member")

	cctx := callerCtx(ctx, tenant, creator, "member")
	task, err := svc.CreateTask(cctx, service.CreateTaskInput{Title: "Needs a hand"})
	require.NoError(t, err)

	first, err := svc.AddAssignee(cctx, task.ID, newAssignee)
	require.NoError(t, err)
	require.Len(t, first.Assignees, 1)

	second, err := svc.AddAssignee(cctx, task.ID, newAssignee)
	require.NoError(t, err)
	require.Len(t, second.Assignees, 1, "re-adding the same assignee must stay idempotent")

	acts := activityActions(ctx, t, superPool, tenant, task.ID)
	require.Equal(t, 1, countStr(acts, "assigned"), "exactly one assigned activity row despite two AddAssignee calls")

	events := outboxEventTypes(ctx, t, superPool, tenant)
	require.Equal(t, 1, countStr(events, "dms.task.assigned.v1"))
	require.Equal(t, 1, countStr(events, "dms.notify.task.assigned.v1"))
}

// TestLinkDocument_UnknownDocument_IsValidationError covers the
// repository's ErrNotFound-on-unknown-document remap: linking a
// nonexistent document id must be ErrValidation, not a raw repository
// error, and must not touch the task's Documents.
func TestLinkDocument_UnknownDocument_IsValidationError(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator, "member")
	bogusDoc := uuid.Must(uuid.NewV7())

	cctx := callerCtx(ctx, tenant, creator, "member")
	task, err := svc.CreateTask(cctx, service.CreateTaskInput{Title: "No doc yet"})
	require.NoError(t, err)

	_, err = svc.LinkDocument(cctx, task.ID, bogusDoc)
	require.ErrorIs(t, err, service.ErrValidation)

	got, err := svc.GetTask(cctx, task.ID)
	require.NoError(t, err)
	require.Empty(t, got.Documents)
}

// TestLinkDocument_Idempotent_NoDuplicateActivityOrEvent covers the
// repo's ON CONFLICT upsert path: relinking an already-linked document
// must not record a second document_linked activity row or event, even
// though the repo call itself always succeeds and refreshes the
// snapshot.
func TestLinkDocument_Idempotent_NoDuplicateActivityOrEvent(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator, "member")
	docID := seedDocument(ctx, t, superPool, tenant, "Spec v1")

	cctx := callerCtx(ctx, tenant, creator, "member")
	task, err := svc.CreateTask(cctx, service.CreateTaskInput{Title: "Doc host"})
	require.NoError(t, err)

	first, err := svc.LinkDocument(cctx, task.ID, docID)
	require.NoError(t, err)
	require.Len(t, first.Documents, 1)

	second, err := svc.LinkDocument(cctx, task.ID, docID)
	require.NoError(t, err)
	require.Len(t, second.Documents, 1, "relinking the same document must stay idempotent")

	acts := activityActions(ctx, t, superPool, tenant, task.ID)
	require.Equal(t, 1, countStr(acts, "document_linked"), "exactly one document_linked activity row despite two LinkDocument calls")

	events := outboxEventTypes(ctx, t, superPool, tenant)
	require.Equal(t, 1, countStr(events, "dms.task.document_linked.v1"))
}

// TestUnlinkDocument_HappyPathAndNotLinked covers UnlinkDocument's
// success path (activity+event recorded, document removed) and its
// validation error when the document isn't currently linked.
func TestUnlinkDocument_HappyPathAndNotLinked(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator, "member")
	docID := seedDocument(ctx, t, superPool, tenant, "Spec v2")
	otherDoc := seedDocument(ctx, t, superPool, tenant, "Not linked")

	cctx := callerCtx(ctx, tenant, creator, "member")
	task, err := svc.CreateTask(cctx, service.CreateTaskInput{Title: "Doc host 2", DocumentIDs: []uuid.UUID{docID}})
	require.NoError(t, err)
	require.Len(t, task.Documents, 1)

	// Unlinking a document that was never linked is a validation error.
	_, err = svc.UnlinkDocument(cctx, task.ID, otherDoc)
	require.ErrorIs(t, err, service.ErrValidation)

	unlinked, err := svc.UnlinkDocument(cctx, task.ID, docID)
	require.NoError(t, err)
	require.Empty(t, unlinked.Documents)

	acts := activityActions(ctx, t, superPool, tenant, task.ID)
	require.Equal(t, 1, countStr(acts, "document_unlinked"))

	events := outboxEventTypes(ctx, t, superPool, tenant)
	require.Equal(t, 1, countStr(events, "dms.task.document_unlinked.v1"))
}

// ---- Task 6: comments with @mentions, activity feed --------------------

// mentionToken mirrors web/src/api/comments.ts's mentionToken helper
// (`@[${displayName}](${userId})`) so these tests build bodies in the
// exact wire format parseMentions parses.
func mentionToken(displayName string, userID uuid.UUID) string {
	return "@[" + displayName + "](" + userID.String() + ")"
}

// TestAddComment_WithMention_PersistsMentionsAndNotifiesOnlyMentionedNonAuthor
// covers the brief's headline comment scenario: author A comments
// mentioning user C. The comment row's mentions column must be [C], a
// `commented` activity row + dms.task.comment.created.v1 event must
// fire, and exactly one dms.notify.task.mention.v1 must target C only
// (not A, the author).
func TestAddComment_WithMention_PersistsMentionsAndNotifiesOnlyMentionedNonAuthor(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	author := uuid.Must(uuid.NewV7())
	mentioned := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, author, "member")
	seedUser(ctx, t, superPool, tenant, mentioned, "member")

	authorCtx := callerCtx(ctx, tenant, author, "member")
	task, err := svc.CreateTask(authorCtx, service.CreateTaskInput{Title: "Needs review"})
	require.NoError(t, err)

	body := "please take a look " + mentionToken("Mentioned User", mentioned)
	c, err := svc.AddComment(authorCtx, task.ID, body)
	require.NoError(t, err)
	require.Equal(t, body, c.Body)
	require.ElementsMatch(t, []uuid.UUID{mentioned}, c.Mentions)

	acts := activityActions(ctx, t, superPool, tenant, task.ID)
	require.Equal(t, 1, countStr(acts, "commented"))

	events := outboxEventTypes(ctx, t, superPool, tenant)
	require.Equal(t, 1, countStr(events, "dms.task.comment.created.v1"))
	require.Equal(t, 1, countStr(events, "dms.notify.task.mention.v1"))

	notifies := outboxPayloads(ctx, t, superPool, tenant, "dms.notify.task.mention.v1")
	require.Len(t, notifies, 1)
	require.Equal(t, "task.mention", notifies[0]["type"])
	require.Equal(t, "You were mentioned on a task", notifies[0]["title"])
	require.Equal(t, body, notifies[0]["body"], "a body under the 200-char cap must pass through unmodified")
	require.ElementsMatch(t, []string{mentioned.String()}, stringSlice(notifies[0]["user_ids"]),
		"mention notify must target only the mentioned user, not the author")
}

// TestAddComment_SelfMention_PersistsMentionButSendsNoNotify covers the
// brief's "notify mentioned−author" rule: mentioning yourself still
// lands in the persisted mentions column, but must not generate a
// dms.notify.task.mention.v1 (there's no point pinging yourself).
func TestAddComment_SelfMention_PersistsMentionButSendsNoNotify(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	author := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, author, "member")

	cctx := callerCtx(ctx, tenant, author, "member")
	task, err := svc.CreateTask(cctx, service.CreateTaskInput{Title: "Self note"})
	require.NoError(t, err)

	body := "note to self " + mentionToken("Me", author)
	c, err := svc.AddComment(cctx, task.ID, body)
	require.NoError(t, err)
	require.ElementsMatch(t, []uuid.UUID{author}, c.Mentions, "self-mention is still persisted in the mentions column")

	events := outboxEventTypes(ctx, t, superPool, tenant)
	require.Equal(t, 0, countStr(events, "dms.notify.task.mention.v1"), "self-mention must not notify")
}

// TestAddComment_UnknownMentionSilentlyDropped covers the "unknown ids
// are silently dropped, not an error" rule: mentioning a well-formed but
// nonexistent user id must still succeed, with that id absent from both
// the persisted mentions and (trivially, since it never lands in
// mentions) any notify.
func TestAddComment_UnknownMentionSilentlyDropped(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	author := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, author, "member")
	bogus := uuid.Must(uuid.NewV7())

	cctx := callerCtx(ctx, tenant, author, "member")
	task, err := svc.CreateTask(cctx, service.CreateTaskInput{Title: "Ghost mention"})
	require.NoError(t, err)

	body := "cc " + mentionToken("Nobody", bogus)
	c, err := svc.AddComment(cctx, task.ID, body)
	require.NoError(t, err, "an unknown mentioned id must not fail the whole comment")
	require.Empty(t, c.Mentions, "unknown mention id must be silently dropped from the persisted column")

	events := outboxEventTypes(ctx, t, superPool, tenant)
	require.Equal(t, 0, countStr(events, "dms.notify.task.mention.v1"))
}

// TestAddComment_ByNonParticipant_Succeeds covers "any authenticated
// tenant member, no participant gate": a stranger who is neither creator
// nor assignee can still comment.
func TestAddComment_ByNonParticipant_Succeeds(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	stranger := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator, "member")
	seedUser(ctx, t, superPool, tenant, stranger, "member")

	creatorCtx := callerCtx(ctx, tenant, creator, "member")
	task, err := svc.CreateTask(creatorCtx, service.CreateTaskInput{Title: "Open to all comments"})
	require.NoError(t, err)

	strangerCtx := callerCtx(ctx, tenant, stranger, "member")
	c, err := svc.AddComment(strangerCtx, task.ID, "just a bystander weighing in")
	require.NoError(t, err, "any tenant member must be able to comment, not just creator/assignees")
	require.Equal(t, stranger, c.AuthorID)
}

// TestAddComment_ValidationAndNotFound covers the body-length gate and
// the "task must exist and not be soft-deleted" gate in one test.
func TestAddComment_ValidationAndNotFound(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	author := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, author, "member")

	cctx := callerCtx(ctx, tenant, author, "member")
	task, err := svc.CreateTask(cctx, service.CreateTaskInput{Title: "Target"})
	require.NoError(t, err)

	_, err = svc.AddComment(cctx, task.ID, "   ")
	require.ErrorIs(t, err, service.ErrValidation, "blank-after-trim body must be rejected")

	_, err = svc.AddComment(cctx, task.ID, strings.Repeat("x", 4001))
	require.ErrorIs(t, err, service.ErrValidation, "body over 4000 chars must be rejected")

	_, err = svc.AddComment(cctx, uuid.Must(uuid.NewV7()), "hello")
	require.ErrorIs(t, err, service.ErrNotFound, "commenting on a nonexistent task must be ErrNotFound")

	require.NoError(t, svc.DeleteTask(cctx, task.ID))
	_, err = svc.AddComment(cctx, task.ID, "too late")
	require.ErrorIs(t, err, service.ErrNotFound, "commenting on a soft-deleted task must be ErrNotFound")
}

// TestListComments_OldestFirst covers the repository's ORDER BY
// created_at ASC contract as observed through the service.
func TestListComments_OldestFirst(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	author := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, author, "member")

	cctx := callerCtx(ctx, tenant, author, "member")
	task, err := svc.CreateTask(cctx, service.CreateTaskInput{Title: "Thread"})
	require.NoError(t, err)

	first, err := svc.AddComment(cctx, task.ID, "first")
	require.NoError(t, err)
	second, err := svc.AddComment(cctx, task.ID, "second")
	require.NoError(t, err)

	page, err := svc.ListComments(cctx, task.ID, 0, 0)
	require.NoError(t, err)
	require.Len(t, page, 2)
	require.Equal(t, first.ID, page[0].ID)
	require.Equal(t, second.ID, page[1].ID)
}

// TestUpdateComment_AuthorRewritesBody_OtherUserForbidden covers both
// halves of UpdateComment's gate in one test: the author can edit (and
// mentions re-parse to reflect the new body, with no new notify), and a
// different tenant member cannot.
func TestUpdateComment_AuthorRewritesBody_OtherUserForbidden(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	author := uuid.Must(uuid.NewV7())
	other := uuid.Must(uuid.NewV7())
	newlyMentioned := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, author, "member")
	seedUser(ctx, t, superPool, tenant, other, "member")
	seedUser(ctx, t, superPool, tenant, newlyMentioned, "member")

	authorCtx := callerCtx(ctx, tenant, author, "member")
	task, err := svc.CreateTask(authorCtx, service.CreateTaskInput{Title: "Editable"})
	require.NoError(t, err)

	c, err := svc.AddComment(authorCtx, task.ID, "original body")
	require.NoError(t, err)
	require.Empty(t, c.Mentions)

	otherCtx := callerCtx(ctx, tenant, other, "member")
	_, err = svc.UpdateComment(otherCtx, task.ID, c.ID, "hijacked")
	require.ErrorIs(t, err, service.ErrForbidden, "only the author may edit a comment")

	newBody := "edited body " + mentionToken("New Person", newlyMentioned)
	updated, err := svc.UpdateComment(authorCtx, task.ID, c.ID, newBody)
	require.NoError(t, err)
	require.Equal(t, newBody, updated.Body)
	require.ElementsMatch(t, []uuid.UUID{newlyMentioned}, updated.Mentions, "edit must re-parse mentions from the new body")

	// No activity row and no new mention notify for an edit.
	acts := activityActions(ctx, t, superPool, tenant, task.ID)
	require.Zero(t, countStr(acts, "updated"), "editing a comment must not record a task-level 'updated' activity row")
	events := outboxEventTypes(ctx, t, superPool, tenant)
	require.Zero(t, countStr(events, "dms.notify.task.mention.v1"), "editing a comment must not send a new mention notify")
}

// TestUpdateComment_ValidationAndNotFound covers UpdateComment's body
// validation and its ErrNotFound path for an unknown comment id.
func TestUpdateComment_ValidationAndNotFound(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	author := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, author, "member")

	cctx := callerCtx(ctx, tenant, author, "member")
	task, err := svc.CreateTask(cctx, service.CreateTaskInput{Title: "Target"})
	require.NoError(t, err)
	c, err := svc.AddComment(cctx, task.ID, "original")
	require.NoError(t, err)

	_, err = svc.UpdateComment(cctx, task.ID, c.ID, "")
	require.ErrorIs(t, err, service.ErrValidation)

	_, err = svc.UpdateComment(cctx, task.ID, uuid.Must(uuid.NewV7()), "new body")
	require.ErrorIs(t, err, service.ErrNotFound)
}

// TestDeleteComment_AuthorAdminAndStranger covers all three actors in
// one test: a stranger is forbidden, the author succeeds, and — on a
// second comment — an admin who is neither author nor creator also
// succeeds. Deletion must not record a task-level activity row.
func TestDeleteComment_AuthorAdminAndStranger(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	author := uuid.Must(uuid.NewV7())
	stranger := uuid.Must(uuid.NewV7())
	admin := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, author, "member")
	seedUser(ctx, t, superPool, tenant, stranger, "member")
	seedUser(ctx, t, superPool, tenant, admin, "admin")

	authorCtx := callerCtx(ctx, tenant, author, "member")
	task, err := svc.CreateTask(authorCtx, service.CreateTaskInput{Title: "Comment host"})
	require.NoError(t, err)

	c1, err := svc.AddComment(authorCtx, task.ID, "delete me (author)")
	require.NoError(t, err)
	c2, err := svc.AddComment(authorCtx, task.ID, "delete me (admin)")
	require.NoError(t, err)

	strangerCtx := callerCtx(ctx, tenant, stranger, "member")
	err = svc.DeleteComment(strangerCtx, task.ID, c1.ID)
	require.ErrorIs(t, err, service.ErrForbidden)

	require.NoError(t, svc.DeleteComment(authorCtx, task.ID, c1.ID), "the author may delete their own comment")

	adminCtx := callerCtx(ctx, tenant, admin, "admin")
	require.NoError(t, svc.DeleteComment(adminCtx, task.ID, c2.ID), "an admin may delete anyone's comment")

	page, err := svc.ListComments(authorCtx, task.ID, 0, 0)
	require.NoError(t, err)
	require.Empty(t, page, "both comments are soft-deleted and must not appear in the list")

	acts := activityActions(ctx, t, superPool, tenant, task.ID)
	require.Zero(t, countStr(acts, "deleted"), "deleting a comment must not record a task-level 'deleted' activity row (that action is reserved for DeleteTask)")
}

// TestListActivity_NewestFirst covers the activity feed's newest-first
// ordering (the repo's ORDER BY id DESC) plus the fact that AddComment's
// `commented` row shows up alongside CreateTask's `created` row.
func TestListActivity_NewestFirst(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	author := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, author, "member")

	cctx := callerCtx(ctx, tenant, author, "member")
	task, err := svc.CreateTask(cctx, service.CreateTaskInput{Title: "Feed"})
	require.NoError(t, err)
	_, err = svc.AddComment(cctx, task.ID, "a comment")
	require.NoError(t, err)

	page, err := svc.ListActivity(cctx, task.ID, 0, 0)
	require.NoError(t, err)
	require.Len(t, page, 2)
	require.Equal(t, "commented", page[0].Action, "newest (the comment) must come first")
	require.Equal(t, "created", page[1].Action)
}
