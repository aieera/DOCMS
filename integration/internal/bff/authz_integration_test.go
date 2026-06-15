//go:build integration
// +build integration

// Acceptance test for the BFF security boundary (Workstream 9 [C], prompt §9–10):
// an unauthorized user cannot load a customer's tree, and the SeDoc API key never
// appears in the response handed to the browser.
//
// Run with: go test -tags integration ./integration/internal/bff/...
package bff

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/integration/internal/sedoc"
	"github.com/aieera/sedoc/integration/internal/store"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

type stubAuthz struct{ allow map[string]bool }

func (s stubAuthz) CanAccessCustomer(_ context.Context, user, _ string) (bool, error) {
	return s.allow[user], nil
}

func newBFF(t *testing.T) (*httptest.Server, *mockDoc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)

	dsn, cleanup, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	require.NoError(t, database.RunMigrations(dsn, "../../migrations"))
	cfg := database.DefaultPoolConfig()
	cfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	st := store.New(pool)
	// Provision CUST-1 directly.
	require.NoError(t, st.SaveCustomer(ctx, &store.CustomerMapping{
		CustomerRef: "CUST-1", Name: "Acme", BucketFolderID: "b1", MainFolderID: "main-1",
		SubfolderIDs: map[string]string{"invoice": "sub-inv", "attachments": "sub-att"},
	}))

	md := &mockDoc{}
	doc := sedoc.New(md.server(t), "vdms_secret_key")
	b := New(st, doc, stubAuthz{allow: map[string]bool{"alice": true}}, "ws-1", zerolog.Nop())

	mux := http.NewServeMux()
	b.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, md
}

// mockDoc is a SeDoc stand-in that records whether it was called + the auth header.
type mockDoc struct {
	mu         sync.Mutex
	folderHits int
	authSeen   string
}

func (m *mockDoc) server(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/workspaces/{ws}/folders", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.folderHits++
		m.authSeen = r.Header.Get("Authorization")
		m.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{
			"folders": []map[string]any{{"id": "sub-inv", "name": "Invoices", "documentCount": 3}},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL + "/api/v1"
}

func get(t *testing.T, srv *httptest.Server, path, user string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if user != "" {
		req.Header.Set("X-ERP-User", user)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body := make([]byte, 0)
	buf := make([]byte, 4096)
	for {
		n, e := resp.Body.Read(buf)
		body = append(body, buf[:n]...)
		if e != nil {
			break
		}
	}
	return resp, body
}

func TestBFF_TreeAuthorizationBoundary(t *testing.T) {
	srv, md := newBFF(t)

	// Authorized user → 200, folders returned, SeDoc called with the key.
	resp, body := get(t, srv, "/files/customers/CUST-1/tree", "alice")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Invoices")

	md.mu.Lock()
	require.Equal(t, 1, md.folderHits, "SeDoc was called server-side")
	require.Equal(t, "Bearer vdms_secret_key", md.authSeen, "key used server-side")
	md.mu.Unlock()

	// CRITICAL: the API key never appears in the response handed to the browser.
	require.NotContains(t, string(body), "vdms_secret_key")

	// Unauthorized user → 404, and SeDoc is NOT called for them.
	before := md.folderHits
	resp, _ = get(t, srv, "/files/customers/CUST-1/tree", "mallory")
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	md.mu.Lock()
	require.Equal(t, before, md.folderHits, "denied user must not trigger a SeDoc call")
	md.mu.Unlock()

	// Unauthenticated → 401.
	resp, _ = get(t, srv, "/files/customers/CUST-1/tree", "")
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestBFF_FolderOutsideCustomerForbidden(t *testing.T) {
	srv, _ := newBFF(t)
	// A folder id not in CUST-1's subtree → 403 even for an authorized user.
	resp, _ := get(t, srv, "/files/customers/CUST-1/folders/some-other-folder/documents", "alice")
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}
