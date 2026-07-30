//go:build integration
// +build integration

// Task 7 (2026-07-28 task-service design) — HTTP handler integration
// tests for the task service's REST surface: routing, request/response
// DTO shapes, the /tasks/mine + /tasks/created bare-array compat shims,
// and the ErrValidation/ErrForbidden/ErrNotFound -> 400/403/404 mapping.
//
// Harness note: this is an internal (package handler) integration test,
// same shape as services/document/internal/handler/
// clause_matches_integration_test.go — an isolated Postgres
// testcontainer, BOTH migration tracks applied (document service owns
// organizations/users/documents/tasks; the task service's own chain
// layers task_assignees/task_documents/task_comments/task_activity on
// top — see services/task/internal/repository/
// repo_fixture_integration_test.go and schema_integration_test.go for
// the same two-track pattern), a dms_app (NOBYPASSRLS) pool wired into
// a real *service.TaskService (mirrors services/task/internal/service/
// tasks_service_integration_test.go's taskServiceFixture), and a
// *http.ServeMux built via handler.New(svc, log).Register(mux) — so
// these tests exercise the full HTTP -> service -> repository ->
// Postgres path, not a mocked service layer.
//
// Run with:
//
//	go test -tags integration -race ./services/task/internal/handler/...
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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

// ---- package-level fixture state -------------------------------------
//
// Each top-level test calls newTestTenant(t), which spins up its own
// Postgres testcontainer (torn down via t.Cleanup) and repopulates these
// package vars. Tests in this file are not parallel (matches the
// document handler package's clause_matches_integration_test.go
// convention), so this is safe.
var (
	testPool  *pgxpool.Pool // dms_app (NOBYPASSRLS) — same pool the Handler's TaskService runs against
	testSuper *pgxpool.Pool // testcontainer superuser — out-of-band seeding only
	testMux   *http.ServeMux
)

// newTestTenant creates an isolated Postgres testcontainer, runs the
// document service's migrations followed by the task service's own
// track, provisions dms_app, wires a *service.TaskService against the
// NOBYPASSRLS pool, and registers every route via handler.New(...).
// Register(mux). Returns a background context and a fresh tenant id
// (not yet seeded as an organization — callers do that via
// seedOrgAndUser).
func newTestTenant(t *testing.T) (context.Context, uuid.UUID) {
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
	// chain reaches completion (same workaround every other fixture in
	// this codebase uses — see clause_matches_integration_test.go /
	// repository/schema_integration_test.go).
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
	mux := http.NewServeMux()
	New(svc, zerolog.Nop()).Register(mux)

	testPool = appPool
	testSuper = superPool
	testMux = mux

	return ctx, uuid.Must(uuid.NewV7())
}

// seedOrgAndUser inserts one organization (the tenant) plus its first
// user, via the superuser pool (out-of-band, bypasses RLS — mirrors
// service/tasks_service_integration_test.go's helper of the same name).
func seedOrgAndUser(ctx context.Context, t *testing.T, tenant, user uuid.UUID, role string) {
	t.Helper()
	_, err := testSuper.Exec(ctx, `
		INSERT INTO organizations (id, name, slug) VALUES ($1, 'Test Org', $2)`,
		tenant, "t-"+tenant.String())
	require.NoError(t, err)
	_, err = testSuper.Exec(ctx, `
		INSERT INTO users (tenant_id, id, email, display_name, role)
		VALUES ($1, $2, $3, 'Test User', $4)`,
		tenant, user, "u-"+user.String()+"@test.local", role)
	require.NoError(t, err)
}

// seedUser inserts one more user row for an already-seeded tenant.
func seedUser(ctx context.Context, t *testing.T, tenant, user uuid.UUID, role string) {
	t.Helper()
	_, err := testSuper.Exec(ctx, `
		INSERT INTO users (tenant_id, id, email, display_name, role)
		VALUES ($1, $2, $3, 'Test User', $4)`,
		tenant, user, "u-"+user.String()+"@test.local", role)
	require.NoError(t, err)
}

// seedDocument inserts the minimal parent chain (workspace, root
// folder) plus a documents row, so LinkDocument's `documents` FK lookup
// resolves.
func seedDocument(ctx context.Context, t *testing.T, tenant uuid.UUID, title string) uuid.UUID {
	t.Helper()
	wsID := uuid.Must(uuid.NewV7())
	folderID := uuid.Must(uuid.NewV7())
	docID := uuid.Must(uuid.NewV7())

	_, err := testSuper.Exec(ctx, `
		INSERT INTO workspaces (tenant_id, id, name) VALUES ($1, $2, 'ws')`,
		tenant, wsID)
	require.NoError(t, err)
	_, err = testSuper.Exec(ctx, `
		INSERT INTO folders (tenant_id, id, workspace_id, name, path, depth)
		VALUES ($1, $2, $3, 'root', 'root', 0)`,
		tenant, folderID, wsID)
	require.NoError(t, err)
	_, err = testSuper.Exec(ctx, `
		INSERT INTO documents
			(tenant_id, id, workspace_id, folder_id, title, description, region_pin, sha256_hash, mime_type)
		VALUES ($1, $2, $3, $4, $5, '', 'us-east-1', '', '')`,
		tenant, docID, wsID, folderID, title)
	require.NoError(t, err)
	return docID
}

// doAuthedJSON issues a request against the package-level testMux with a
// ctx carrying auth.UserInfo the way middleware.SessionAuth would after
// a successful session lookup — mirrors clause_matches_integration_test.go's
// helper of the same name.
func doAuthedJSON(t *testing.T, method, path string, body io.Reader, tenant, user uuid.UUID, role string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	ctx := auth.WithUser(req.Context(), auth.UserInfo{ID: user, TenantID: tenant, Role: role})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	testMux.ServeHTTP(rec, req)
	return rec
}

func decodeMap(t *testing.T, resp *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &m), "body: %s", resp.Body.String())
	return m
}

func decodeSlice(t *testing.T, resp *httptest.ResponseRecorder) []any {
	t.Helper()
	var s []any
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &s), "body must be a bare JSON array, got: %s", resp.Body.String())
	return s
}

// createTask is a small helper most tests use to seed one task via the
// real HTTP path (not a direct service call) — keeps each test focused
// on the behavior it's actually asserting.
func createTask(t *testing.T, tenant, actor uuid.UUID, bodyJSON string) map[string]any {
	t.Helper()
	resp := doAuthedJSON(t, "POST", "/api/v1/tasks", strings.NewReader(bodyJSON), tenant, actor, "member")
	require.Equal(t, http.StatusCreated, resp.Code, "create task: %s", resp.Body.String())
	return decodeMap(t, resp)
}

// ---- tests -------------------------------------------------------------

func TestCreateTask_WithAssigneesAndDocuments_Returns201Aggregated(t *testing.T) {
	ctx, tenant := newTestTenant(t)
	creator := uuid.Must(uuid.NewV7())
	assignee := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, tenant, creator, "member")
	seedUser(ctx, t, tenant, assignee, "member")
	docID := seedDocument(ctx, t, tenant, "Contract A")

	body := fmt.Sprintf(`{"title":"Review contract","priority":"high","assignee_ids":[%q],"document_ids":[%q]}`,
		assignee.String(), docID.String())
	resp := doAuthedJSON(t, "POST", "/api/v1/tasks", strings.NewReader(body), tenant, creator, "member")
	require.Equal(t, http.StatusCreated, resp.Code, resp.Body.String())

	got := decodeMap(t, resp)
	require.Equal(t, "Review contract", got["title"])
	require.Equal(t, "high", got["priority"])
	require.Equal(t, "open", got["status"])
	require.NotEmpty(t, got["id"])

	assignees, ok := got["assignees"].([]any)
	require.True(t, ok, "assignees must be present in the aggregated body")
	require.Len(t, assignees, 1)
	require.Equal(t, assignee.String(), assignees[0].(map[string]any)["user_id"])

	documents, ok := got["documents"].([]any)
	require.True(t, ok, "documents must be present in the aggregated body")
	require.Len(t, documents, 1)
	require.Equal(t, docID.String(), documents[0].(map[string]any)["document_id"])

	t.Run("invalid priority is 400", func(t *testing.T) {
		resp := doAuthedJSON(t, "POST", "/api/v1/tasks",
			strings.NewReader(`{"title":"Bad task","priority":"urgentest"}`), tenant, creator, "member")
		require.Equal(t, http.StatusBadRequest, resp.Code)
		errBody := decodeMap(t, resp)
		require.NotEmpty(t, errBody["error"])
	})

	t.Run("invalid uuid in assignee_ids is 400", func(t *testing.T) {
		resp := doAuthedJSON(t, "POST", "/api/v1/tasks",
			strings.NewReader(`{"title":"Bad ids","assignee_ids":["not-a-uuid"]}`), tenant, creator, "member")
		require.Equal(t, http.StatusBadRequest, resp.Code)
	})
}

func TestListTasks_PaginationEnvelope(t *testing.T) {
	ctx, tenant := newTestTenant(t)
	creator := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, tenant, creator, "member")

	for i := 0; i < 3; i++ {
		createTask(t, tenant, creator, fmt.Sprintf(`{"title":"Task %d"}`, i))
	}

	resp := doAuthedJSON(t, "GET", "/api/v1/tasks?limit=2&offset=0", nil, tenant, creator, "member")
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	got := decodeMap(t, resp)
	require.EqualValues(t, 3, got["total"])
	require.EqualValues(t, 2, got["limit"])
	require.EqualValues(t, 0, got["offset"])
	items, ok := got["items"].([]any)
	require.True(t, ok, "GET /tasks must return the {items,total,limit,offset} envelope")
	require.Len(t, items, 2)

	t.Run("unknown filter value is 400", func(t *testing.T) {
		resp := doAuthedJSON(t, "GET", "/api/v1/tasks?filter=bogus", nil, tenant, creator, "member")
		require.Equal(t, http.StatusBadRequest, resp.Code)
	})

	t.Run("invalid document_id uuid is 400", func(t *testing.T) {
		resp := doAuthedJSON(t, "GET", "/api/v1/tasks?document_id=not-a-uuid", nil, tenant, creator, "member")
		require.Equal(t, http.StatusBadRequest, resp.Code)
	})
}

func TestListMineAndCreated_ReturnBareArrays(t *testing.T) {
	ctx, tenant := newTestTenant(t)
	creator := uuid.Must(uuid.NewV7())
	other := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, tenant, creator, "member")
	seedUser(ctx, t, tenant, other, "member")

	createTask(t, tenant, creator, fmt.Sprintf(`{"title":"Mine","assignee_ids":[%q]}`, creator.String()))
	createTask(t, tenant, other, `{"title":"Someone else's"}`)

	resp := doAuthedJSON(t, "GET", "/api/v1/tasks/mine", nil, tenant, creator, "member")
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	items := decodeSlice(t, resp)
	require.Len(t, items, 1)
	require.Equal(t, "Mine", items[0].(map[string]any)["title"])

	resp = doAuthedJSON(t, "GET", "/api/v1/tasks/created", nil, tenant, creator, "member")
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	items = decodeSlice(t, resp)
	require.Len(t, items, 1)
	require.Equal(t, "Mine", items[0].(map[string]any)["title"], "created shim must scope to created_by, not assignee")
}

func TestCompleteTask_ByAssignee200_ByStranger403(t *testing.T) {
	ctx, tenant := newTestTenant(t)
	creator := uuid.Must(uuid.NewV7())
	assignee := uuid.Must(uuid.NewV7())
	stranger := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, tenant, creator, "member")
	seedUser(ctx, t, tenant, assignee, "member")
	seedUser(ctx, t, tenant, stranger, "member")

	task := createTask(t, tenant, creator, fmt.Sprintf(`{"title":"Do it","assignee_ids":[%q]}`, assignee.String()))
	taskID := task["id"].(string)

	resp := doAuthedJSON(t, "POST", "/api/v1/tasks/"+taskID+"/complete", nil, tenant, stranger, "member")
	require.Equal(t, http.StatusForbidden, resp.Code, "a stranger with no stake in the task must not be able to complete it")
	errBody := decodeMap(t, resp)
	require.NotEmpty(t, errBody["error"])

	resp = doAuthedJSON(t, "POST", "/api/v1/tasks/"+taskID+"/complete", nil, tenant, assignee, "member")
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	got := decodeMap(t, resp)
	require.Equal(t, "done", got["status"])
	require.Equal(t, assignee.String(), got["completed_by"])
}

func TestAddComment_Returns201AndListsOldestFirst(t *testing.T) {
	ctx, tenant := newTestTenant(t)
	creator := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, tenant, creator, "member")

	task := createTask(t, tenant, creator, `{"title":"Discuss"}`)
	taskID := task["id"].(string)

	resp := doAuthedJSON(t, "POST", "/api/v1/tasks/"+taskID+"/comments",
		strings.NewReader(`{"body":"Looks good"}`), tenant, creator, "member")
	require.Equal(t, http.StatusCreated, resp.Code, resp.Body.String())
	comment := decodeMap(t, resp)
	require.Equal(t, "Looks good", comment["body"])
	require.Equal(t, creator.String(), comment["author_id"])
	require.NotEmpty(t, comment["id"])

	resp = doAuthedJSON(t, "GET", "/api/v1/tasks/"+taskID+"/comments", nil, tenant, creator, "member")
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	items := decodeSlice(t, resp)
	require.Len(t, items, 1)
	require.Equal(t, "Looks good", items[0].(map[string]any)["body"])
}

func TestListActivity_ReturnsNewestFirst(t *testing.T) {
	ctx, tenant := newTestTenant(t)
	creator := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, tenant, creator, "member")

	task := createTask(t, tenant, creator, `{"title":"Track me"}`)
	taskID := task["id"].(string)

	resp := doAuthedJSON(t, "POST", "/api/v1/tasks/"+taskID+"/start", nil, tenant, creator, "member")
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())

	resp = doAuthedJSON(t, "GET", "/api/v1/tasks/"+taskID+"/activity", nil, tenant, creator, "member")
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	items := decodeSlice(t, resp)
	require.Len(t, items, 2, "created + status_changed")
	require.Equal(t, "status_changed", items[0].(map[string]any)["action"], "newest (the start transition) must be first")
	require.Equal(t, "created", items[1].(map[string]any)["action"])
}

func TestGetTask_InvalidUUID_400_And_UnknownID_404(t *testing.T) {
	ctx, tenant := newTestTenant(t)
	creator := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, tenant, creator, "member")

	resp := doAuthedJSON(t, "GET", "/api/v1/tasks/not-a-uuid", nil, tenant, creator, "member")
	require.Equal(t, http.StatusBadRequest, resp.Code)

	resp = doAuthedJSON(t, "GET", "/api/v1/tasks/"+uuid.Must(uuid.NewV7()).String(), nil, tenant, creator, "member")
	require.Equal(t, http.StatusNotFound, resp.Code)
	errBody := decodeMap(t, resp)
	require.NotEmpty(t, errBody["error"])
}

func TestNoTenant_Returns401(t *testing.T) {
	_, tenant := newTestTenant(t)
	_ = tenant

	req := httptest.NewRequest("GET", "/api/v1/tasks", nil)
	rec := httptest.NewRecorder()
	testMux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	body := decodeMap(t, rec)
	require.Equal(t, "no tenant", body["error"])
}

func TestAssigneeAndDocumentLinks_AddAndRemove(t *testing.T) {
	ctx, tenant := newTestTenant(t)
	creator := uuid.Must(uuid.NewV7())
	other := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, tenant, creator, "member")
	seedUser(ctx, t, tenant, other, "member")
	docID := seedDocument(ctx, t, tenant, "Doc A")

	task := createTask(t, tenant, creator, `{"title":"Links"}`)
	taskID := task["id"].(string)

	resp := doAuthedJSON(t, "POST", "/api/v1/tasks/"+taskID+"/assignees",
		strings.NewReader(fmt.Sprintf(`{"user_id":%q}`, other.String())), tenant, creator, "member")
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	got := decodeMap(t, resp)
	assignees := got["assignees"].([]any)
	require.Len(t, assignees, 1)

	resp = doAuthedJSON(t, "POST", "/api/v1/tasks/"+taskID+"/documents",
		strings.NewReader(fmt.Sprintf(`{"document_id":%q}`, docID.String())), tenant, creator, "member")
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	got = decodeMap(t, resp)
	documents := got["documents"].([]any)
	require.Len(t, documents, 1)

	resp = doAuthedJSON(t, "DELETE", "/api/v1/tasks/"+taskID+"/assignees/"+other.String(), nil, tenant, creator, "member")
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	got = decodeMap(t, resp)
	require.Empty(t, got["assignees"])

	resp = doAuthedJSON(t, "DELETE", "/api/v1/tasks/"+taskID+"/documents/"+docID.String(), nil, tenant, creator, "member")
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	got = decodeMap(t, resp)
	require.Empty(t, got["documents"])
}

func TestUpdateAndDeleteTask(t *testing.T) {
	ctx, tenant := newTestTenant(t)
	creator := uuid.Must(uuid.NewV7())
	stranger := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, tenant, creator, "member")
	seedUser(ctx, t, tenant, stranger, "member")

	task := createTask(t, tenant, creator, `{"title":"Original"}`)
	taskID := task["id"].(string)

	resp := doAuthedJSON(t, "PATCH", "/api/v1/tasks/"+taskID,
		strings.NewReader(`{"title":"Renamed"}`), tenant, stranger, "member")
	require.Equal(t, http.StatusForbidden, resp.Code, "only creator/admin may edit fields")

	resp = doAuthedJSON(t, "PATCH", "/api/v1/tasks/"+taskID,
		strings.NewReader(`{"title":"Renamed"}`), tenant, creator, "member")
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	got := decodeMap(t, resp)
	require.Equal(t, "Renamed", got["title"])

	resp = doAuthedJSON(t, "DELETE", "/api/v1/tasks/"+taskID, nil, tenant, creator, "member")
	require.Equal(t, http.StatusNoContent, resp.Code)

	resp = doAuthedJSON(t, "GET", "/api/v1/tasks/"+taskID, nil, tenant, creator, "member")
	require.Equal(t, http.StatusNotFound, resp.Code, "a soft-deleted task must 404 on GET")
}
