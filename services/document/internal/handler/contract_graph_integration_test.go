//go:build integration

// D-19: the contract-graph read returned every neighbour's title, MIME type
// and creation time to any tenant member, with no permission check. It now
// requires view on the root (404 otherwise, same as an unknown document) and
// returns only the root's connected component among documents the caller
// may view: an unviewable neighbour is dropped together with its edges, and
// so is anything reachable only through it.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
	"github.com/aieera/sedoc/services/document/internal/repository"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// hiddenDocsPolicy allows everything except any check on a hidden resource.
type hiddenDocsPolicy struct{ hidden map[string]bool }

func (p hiddenDocsPolicy) CheckPermission(_ context.Context, in *sedocv1.CheckPermissionRequest, _ ...grpc.CallOption) (*sedocv1.CheckPermissionResponse, error) {
	return &sedocv1.CheckPermissionResponse{Allowed: !p.hidden[in.GetResourceId()]}, nil
}

func (p hiddenDocsPolicy) BatchCheckPermission(_ context.Context, in *sedocv1.BatchCheckPermissionRequest, _ ...grpc.CallOption) (*sedocv1.BatchCheckPermissionResponse, error) {
	out := &sedocv1.BatchCheckPermissionResponse{}
	for _, c := range in.GetChecks() {
		out.Results = append(out.Results, &sedocv1.CheckPermissionResponse{Allowed: !p.hidden[c.GetResourceId()]})
	}
	return out, nil
}

func TestContractGraph_HidesUnviewableNeighbours(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	dsn, pgClean, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(pgClean)
	require.NoError(t, database.RunMigrations(dsn, "../../migrations"))
	cfg := database.DefaultPoolConfig()
	cfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	tenant, user, ws, folder := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, name, slug) VALUES ($1, 'T', $2)`, []any{tenant, "t-" + tenant.String()[:8]}},
		{`INSERT INTO users (tenant_id, id, email, display_name, role) VALUES ($1, $2, $3, 'U', 'member')`,
			[]any{tenant, user, "u-" + user.String()[:8] + "@test.local"}},
		{`INSERT INTO workspaces (tenant_id, id, name) VALUES ($1, $2, 'ws')`, []any{tenant, ws}},
		{`INSERT INTO folders (tenant_id, id, workspace_id, name, path, depth) VALUES ($1, $2, $3, 'root', 'root', 0)`,
			[]any{tenant, folder, ws}},
	} {
		_, err := pool.Exec(ctx, q.sql, q.args...)
		require.NoError(t, err)
	}
	doc := func(title string) uuid.UUID {
		id := uuid.Must(uuid.NewV7())
		_, err := pool.Exec(ctx, `
			INSERT INTO documents (tenant_id, id, workspace_id, folder_id, title, lifecycle_state, created_by, updated_by)
			VALUES ($1, $2, $3, $4, $5, 'active', $6, $6)`, tenant, id, ws, folder, title, user)
		require.NoError(t, err)
		return id
	}
	edge := func(src, dst uuid.UUID) {
		_, err := pool.Exec(ctx, `
			INSERT INTO contract_graph_edges (tenant_id, src_document, dst_document, edge_type, confidence, metadata)
			VALUES ($1, $2, $3, 'references', 1.0, '{}'::jsonb)`, tenant, src, dst)
		require.NoError(t, err)
	}
	root, seen, hidden, beyond := doc("MSA"), doc("SOW-1"), doc("Secret settlement"), doc("Reachable only via secret")
	edge(root, seen)
	edge(root, hidden)
	edge(hidden, beyond)

	svc := service.New(pool, repository.New(pool), hiddenDocsPolicy{hidden: map[string]bool{hidden.String(): true}}, zerolog.Nop())
	mux := http.NewServeMux()
	NewContractGraphHandler(pool, svc).Register(mux)
	uctx := auth.WithUser(auth.SetTenantID(ctx, tenant), auth.UserInfo{ID: user, TenantID: tenant, Role: "member"})
	get := func(id uuid.UUID) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/contracts/"+id.String()+"/graph?depth=3", nil).WithContext(uctx)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	rec := get(root)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "Secret settlement")
	require.NotContains(t, rec.Body.String(), hidden.String())
	require.NotContains(t, rec.Body.String(), beyond.String(), "nothing reachable only through a hidden node")
	var resp graphResp
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	var ids []string
	for _, n := range resp.Nodes {
		ids = append(ids, n.ID)
	}
	require.ElementsMatch(t, []string{root.String(), seen.String()}, ids)
	require.Len(t, resp.Edges, 1)
	require.Equal(t, root.String(), resp.Edges[0].Src)
	require.Equal(t, seen.String(), resp.Edges[0].Dst)

	// The hidden document's own graph is not readable at all.
	rec = get(hidden)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	require.False(t, strings.Contains(rec.Body.String(), "Secret settlement"))
}
