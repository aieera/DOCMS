//go:build integration
// +build integration

// DB-backed acceptance tests for the BFF routes the prior suites didn't cover:
// the document-id surface (detail / download / restore — authz resolved via the
// document's folder → customer), customer-scoped search scoping, and the admin
// sync-log read. Complements authz_integration_test.go (tree) and
// bff_upload_integration_test.go (upload).
//
// Run with: go test -tags integration ./integration/internal/bff/...
package bff

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/integration/internal/sedoc"
	"github.com/aieera/sedoc/integration/internal/store"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

// perCustomerAuthz authorizes a specific (user, customer) pair — unlike the
// shared stubAuthz, it actually consults the customer ref, so we can prove a
// document in another customer's folder is denied.
type perCustomerAuthz struct{ allow map[string]bool } // key: "user|ref"

func (a perCustomerAuthz) CanAccessCustomer(_ context.Context, user, ref string) (bool, error) {
	return a.allow[user+"|"+ref], nil
}

// routesMockDoc is a SeDoc stand-in for the document-id + search surface. Folder
// membership is encoded by the document id so authz can be exercised.
type routesMockDoc struct {
	docFolder map[string]string // documentId → folderId
	restored  map[string]string // documentId → versionId restored
}

func (m *routesMockDoc) server(t *testing.T) string {
	t.Helper()
	if m.restored == nil {
		m.restored = map[string]string{}
	}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v1/documents/{id}/versions", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"versions": []map[string]any{
				{"id": "v2", "versionNumber": 2}, {"id": "v1", "versionNumber": 1},
			},
		})
	})
	mux.HandleFunc("GET /api/v1/documents/{id}/content", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF-doc-bytes"))
	})
	mux.HandleFunc("POST /api/v1/documents/{id}/versions/{vid}/restore", func(w http.ResponseWriter, r *http.Request) {
		m.restored[r.PathValue("id")] = r.PathValue("vid")
		w.WriteHeader(http.StatusOK)
	})
	// must come AFTER the more specific /{id}/... patterns.
	mux.HandleFunc("GET /api/v1/documents/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		folder, ok := m.docFolder[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"type": "NOT_FOUND", "message": "no doc"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "title": "Doc " + id, "folderId": folder})
	})
	// search service (separate URL; client posts to searchURL + "/search").
	mux.HandleFunc("POST /api/v1/search", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"id": "hit-mine", "folder_id": "sub-inv"},     // CUST-1 subtree → kept
				{"id": "hit-foreign", "folder_id": "sub-c2"},   // CUST-2 subtree → dropped
				{"id": "hit-nowhere", "folder_id": "orphan-9"}, // unknown folder → dropped
			},
			"total_count": 3,
			"page_token":  "",
		})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL + "/api/v1"
}

func newRoutesBFF(t *testing.T) (*httptest.Server, *store.Store, *routesMockDoc) {
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
	// CUST-1 owned by alice; CUST-2 owned by bob.
	require.NoError(t, st.SaveCustomer(ctx, &store.CustomerMapping{
		CustomerRef: "CUST-1", Name: "Acme", BucketFolderID: "b1", MainFolderID: "main-1",
		SubfolderIDs: map[string]string{"invoice": "sub-inv", "attachments": "sub-att"},
	}))
	require.NoError(t, st.SaveCustomer(ctx, &store.CustomerMapping{
		CustomerRef: "CUST-2", Name: "Globex", BucketFolderID: "b2", MainFolderID: "main-2",
		SubfolderIDs: map[string]string{"invoice": "sub-c2"},
	}))

	md := &routesMockDoc{docFolder: map[string]string{
		"doc-mine":    "sub-inv", // CUST-1
		"doc-foreign": "sub-c2",  // CUST-2
	}}
	sedocURL := md.server(t)
	doc := sedoc.New(sedocURL, "vdms_secret_key")
	doc.SetSearchURL(sedocURL) // search posts to <sedocURL>/search

	authz := perCustomerAuthz{allow: map[string]bool{
		"alice|CUST-1": true,
		"bob|CUST-2":   true,
	}}
	b := New(st, doc, authz, "ws-1", zerolog.Nop())
	mux := http.NewServeMux()
	b.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, st, md
}

// req issues a request with optional ERP user/admin headers and an optional body.
func req(t *testing.T, srv *httptest.Server, method, path, user string, admin bool, body string) (*http.Response, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	r, _ := http.NewRequest(method, srv.URL+path, rdr)
	if user != "" {
		r.Header.Set("X-ERP-User", user)
	}
	if admin {
		r.Header.Set("X-ERP-Admin", "true")
	}
	resp, err := http.DefaultClient.Do(r)
	require.NoError(t, err)
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

func TestBFF_DocumentDetail(t *testing.T) {
	srv, _, _ := newRoutesBFF(t)

	// authorized owner → 200 with versions.
	resp, body := req(t, srv, http.MethodGet, "/files/documents/doc-mine", "alice", false, "")
	require.Equal(t, http.StatusOK, resp.StatusCode, "body=%s", body)
	require.Contains(t, string(body), `"versions"`)
	require.Contains(t, string(body), "v2")

	// a document in CUST-2's folder, requested by alice (not authorized for CUST-2)
	// → 404 (don't reveal existence).
	resp, _ = req(t, srv, http.MethodGet, "/files/documents/doc-foreign", "alice", false, "")
	require.Equal(t, http.StatusNotFound, resp.StatusCode)

	// the owner of CUST-2 CAN see it.
	resp, _ = req(t, srv, http.MethodGet, "/files/documents/doc-foreign", "bob", false, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// unauthenticated → 401.
	resp, _ = req(t, srv, http.MethodGet, "/files/documents/doc-mine", "", false, "")
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// unknown document → SeDoc 404 propagates.
	resp, _ = req(t, srv, http.MethodGet, "/files/documents/ghost", "alice", false, "")
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestBFF_Download(t *testing.T) {
	srv, _, _ := newRoutesBFF(t)

	resp, body := req(t, srv, http.MethodGet, "/files/documents/doc-mine/download", "alice", false, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "application/pdf", resp.Header.Get("Content-Type"))
	require.Equal(t, "%PDF-doc-bytes", string(body))
	// the SeDoc service key never reaches the browser.
	require.NotContains(t, string(body), "vdms_secret_key")

	// a foreign document is denied to alice.
	resp, _ = req(t, srv, http.MethodGet, "/files/documents/doc-foreign/download", "alice", false, "")
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestBFF_Restore(t *testing.T) {
	srv, _, md := newRoutesBFF(t)

	resp, _ := req(t, srv, http.MethodPost, "/files/documents/doc-mine/versions/v1/restore", "alice", false, "")
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	require.Equal(t, "v1", md.restored["doc-mine"], "restore reached SeDoc with the version id")

	// authz still enforced on the write path.
	resp, _ = req(t, srv, http.MethodPost, "/files/documents/doc-foreign/versions/v1/restore", "alice", false, "")
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestBFF_SearchScopedToCustomerSubtree(t *testing.T) {
	srv, _, _ := newRoutesBFF(t)

	resp, body := req(t, srv, http.MethodPost, "/files/search", "alice", false, `{"customer_ref":"CUST-1","q":"invoice"}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body=%s", body)

	var out struct {
		Results []map[string]any `json:"results"`
		Scoped  bool             `json:"scoped"`
	}
	require.NoError(t, json.Unmarshal(body, &out))
	require.True(t, out.Scoped)
	// Only the hit inside CUST-1's subtree survives; the foreign + orphan hits are dropped.
	require.Len(t, out.Results, 1)
	require.Equal(t, "hit-mine", out.Results[0]["id"])

	// customer_ref required.
	resp, _ = req(t, srv, http.MethodPost, "/files/search", "alice", false, `{"q":"x"}`)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestBFF_AdminSyncLog(t *testing.T) {
	srv, st, _ := newRoutesBFF(t)
	ctx := context.Background()

	// seed a sync_log row (InsertEvent writes the log row).
	_, _, err := st.InsertEvent(ctx, "evt-1", "document.committed", "CUST-1", []byte(`{}`))
	require.NoError(t, err)

	// non-admin → 403.
	resp, _ := req(t, srv, http.MethodGet, "/files/sync/log", "alice", false, "")
	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	// admin → 200 with the row.
	resp, body := req(t, srv, http.MethodGet, "/files/sync/log", "ops", true, "")
	require.Equal(t, http.StatusOK, resp.StatusCode, "body=%s", body)
	require.Contains(t, string(body), "evt-1")
	require.Contains(t, string(body), "document.committed")
}
