//go:build integration

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/policy/internal/cache"
	"github.com/aieera/sedoc/services/policy/internal/opa"
	"github.com/aieera/sedoc/services/policy/internal/repository"
	"github.com/aieera/sedoc/services/policy/internal/service"
)

// policyFixture is a tenant with one workspace, one folder in it and two
// documents in the folder (one active, one disposed), plus users in the
// roles the M-11 / CB-3 cases need.
type policyFixture struct {
	pool                         *pgxpool.Pool
	mux                          *http.ServeMux
	tenant, workspace, folder    uuid.UUID
	activeDoc, disposedDoc       uuid.UUID
	wsAdmin, folderSharer, other uuid.UUID
	editor, target               uuid.UUID
}

func newPolicyFixture(ctx context.Context, t *testing.T) *policyFixture {
	t.Helper()
	dsn, cleanup, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	require.NoError(t, database.RunMigrations(dsn, "../../../document/migrations"), "document schema-of-record")
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	f := &policyFixture{pool: pool, tenant: uuid.New(), workspace: uuid.New(), folder: uuid.New(),
		activeDoc: uuid.New(), disposedDoc: uuid.New(), wsAdmin: uuid.New(), folderSharer: uuid.New(),
		other: uuid.New(), editor: uuid.New(), target: uuid.New()}
	exec := func(q string, args ...any) {
		t.Helper()
		_, err := pool.Exec(ctx, q, args...)
		require.NoError(t, err, q)
	}
	exec(`INSERT INTO organizations (id, name, slug) VALUES ($1, 'org', $2)`, f.tenant, f.tenant.String())
	for _, u := range []uuid.UUID{f.wsAdmin, f.folderSharer, f.other, f.editor, f.target} {
		exec(`INSERT INTO users (tenant_id, id, email, display_name, role) VALUES ($1, $2, $3, 'u', 'member')`,
			f.tenant, u, u.String()+"@example.com")
	}
	exec(`INSERT INTO workspaces (tenant_id, id, name) VALUES ($1, $2, 'ws')`, f.tenant, f.workspace)
	exec(`INSERT INTO workspace_members (tenant_id, workspace_id, user_id, role) VALUES ($1, $2, $3, 'admin')`,
		f.tenant, f.workspace, f.wsAdmin)
	exec(`INSERT INTO folders (tenant_id, id, workspace_id, name, path, depth) VALUES ($1, $2, $3, 'f', 'root', 0)`,
		f.tenant, f.folder, f.workspace)
	for _, d := range []struct {
		id    uuid.UUID
		state string
	}{{f.activeDoc, "active"}, {f.disposedDoc, "disposed"}} {
		exec(`INSERT INTO documents (tenant_id, id, workspace_id, folder_id, title, lifecycle_state) VALUES ($1, $2, $3, $4, 'd', $5)`,
			f.tenant, d.id, f.workspace, f.folder, d.state)
		exec(`INSERT INTO permissions (tenant_id, resource_type, resource_id, principal_type, principal_id, capability)
		      VALUES ($1, 'document', $2, 'user', $3, 'edit')`, f.tenant, d.id, f.editor)
	}
	exec(`INSERT INTO permissions (tenant_id, resource_type, resource_id, principal_type, principal_id, capability)
	      VALUES ($1, 'folder', $2, 'user', $3, 'share')`, f.tenant, f.folder, f.folderSharer)

	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	engine, err := opa.New(ctx)
	require.NoError(t, err)
	svc := service.New(service.Config{
		Repos: repository.New(pool), Cache: cache.New(redis.NewClient(&redis.Options{Addr: mr.Addr()})),
		Engine: engine, Outbox: database.NewOutboxRepository(), Logger: zerolog.Nop(),
	})
	f.mux = http.NewServeMux()
	NewHTTPHandler(svc).Register(f.mux)
	return f
}

// do sends a request as an authenticated member (what SessionAuth puts on
// the context).
func (f *policyFixture) do(t *testing.T, user uuid.UUID, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	r := httptest.NewRequest(method, path, &buf)
	r = r.WithContext(auth.WithUser(r.Context(), auth.UserInfo{ID: user, TenantID: f.tenant, Role: "member"}))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}

// M-11: the HTTP grant/list/revoke gate evaluated the caller with no folder
// or workspace context, so a workspace admin — or someone a folder was
// shared with — got 403 from the share dialog although the document
// service's gRPC path allows them. The policy service now resolves the
// document's folder/workspace itself.
func TestHTTPGrant_WorkspaceAdminAllowed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	f := newPolicyFixture(ctx, t)
	docPath := "/api/v1/permissions/document/" + f.activeDoc.String()

	w := f.do(t, f.wsAdmin, http.MethodPost, docPath,
		map[string]any{"principal_type": "user", "principal_id": f.target.String(), "capability": "edit"})
	require.Equal(t, http.StatusCreated, w.Code, "workspace admin must be able to share: %s", w.Body.String())

	w = f.do(t, f.wsAdmin, http.MethodGet, docPath, nil)
	require.Equal(t, http.StatusOK, w.Code, "workspace admin must be able to read the ACL")

	w = f.do(t, f.folderSharer, http.MethodPost, docPath,
		map[string]any{"principal_type": "user", "principal_id": f.other.String(), "capability": "view"})
	require.Equal(t, http.StatusCreated, w.Code, "a folder share holder may share documents in it: %s", w.Body.String())
	w = f.do(t, f.folderSharer, http.MethodPost, docPath,
		map[string]any{"principal_type": "user", "principal_id": f.other.String(), "capability": "edit"})
	require.Equal(t, http.StatusForbidden, w.Code, "but never above the share they hold")

	w = f.do(t, f.wsAdmin, http.MethodDelete, docPath+"/"+f.target.String(), nil)
	require.Equal(t, http.StatusNoContent, w.Code, "workspace admin must be able to revoke")

	// A member with no route to the document is still refused.
	w = f.do(t, f.other, http.MethodPost, docPath,
		map[string]any{"principal_type": "user", "principal_id": f.target.String(), "capability": "view"})
	require.Equal(t, http.StatusForbidden, w.Code)
}

// CB-3: collaboration authorizes rooms through POST /permissions/check, which
// evaluated without lifecycle, inheritance or clearance context, so a
// disposed document stayed editable and the clearance gate never fired.
// The endpoint now derives that context server-side, the same context the
// document service sends over gRPC.
func TestHTTPCheck_DisposedDeniesEdit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	f := newPolicyFixture(ctx, t)

	check := func(user, doc uuid.UUID, action string) (bool, string) {
		t.Helper()
		w := f.do(t, user, http.MethodPost, "/api/v1/permissions/check",
			map[string]string{"resource_type": "document", "resource_id": doc.String(), "action": action})
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var res struct {
			Allowed bool   `json:"allowed"`
			Reason  string `json:"reason"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &res))
		return res.Allowed, res.Reason
	}

	ok, _ := check(f.editor, f.activeDoc, "edit")
	require.True(t, ok, "control: a direct edit grant allows edit on an active document")
	ok, _ = check(f.editor, f.disposedDoc, "edit")
	require.False(t, ok, "a disposed document must not be editable")

	// Inheritance: the workspace admin edits through Rule 5, the folder
	// share holder views through Rule 3.
	ok, _ = check(f.wsAdmin, f.activeDoc, "edit")
	require.True(t, ok, "workspace admin inherits edit on documents in the workspace")
	ok, _ = check(f.folderSharer, f.activeDoc, "view")
	require.True(t, ok, "a folder grant cascades to the documents in it")

	// Clearance: with gating on, a restricted document needs restricted
	// clearance.
	_, err := f.pool.Exec(ctx, `INSERT INTO classification_gate_config (tenant_id, enabled) VALUES ($1, true)`, f.tenant)
	require.NoError(t, err)
	_, err = f.pool.Exec(ctx, `UPDATE documents SET security_classification = 'restricted' WHERE tenant_id = $1 AND id = $2`,
		f.tenant, f.activeDoc)
	require.NoError(t, err)
	_, err = f.pool.Exec(ctx, `UPDATE users SET clearance = 'internal' WHERE tenant_id = $1 AND id = $2`, f.tenant, f.editor)
	require.NoError(t, err)
	ok, reason := check(f.editor, f.activeDoc, "view")
	require.False(t, ok, "clearance below the document's classification must deny")
	require.Contains(t, reason, "blocked:")
}
