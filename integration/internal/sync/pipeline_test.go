//go:build integration
// +build integration

// End-to-end pipeline test (Workstream 9 [A]): a customer.created + a
// document.committed posted to the worker webhook are claimed by the worker and
// applied to a mock SeDoc — provisioning the bucket+main+6 subfolders and
// upserting the document into the right subfolder by external_id — and a
// re-fired event with the same erp_event_id is deduped (no new work).
//
// Run with: go test -tags integration ./integration/internal/sync/...
package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// mockSeDoc records what the worker sends.
type mockSeDoc struct {
	mu          sync.Mutex
	folderPOSTs int
	initiate    int
	complete    int
	upserts     int
	lastUpsert  map[string]any
}

func (m *mockSeDoc) server(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	folderID := func(name string) string { return "folder-" + strings.ToLower(name) }
	mux.HandleFunc("POST /api/v1/workspaces/{ws}/folders", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		m.mu.Lock()
		m.folderPOSTs++
		m.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"id": folderID(b.Name)})
	})
	mux.HandleFunc("PATCH /api/v1/folders/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("POST /api/v1/storage/uploads/initiate", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.initiate++
		m.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"upload_id": "up", "presigned_put_url": "http://" + r.Host + "/put"})
	})
	mux.HandleFunc("PUT /put", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("POST /api/v1/storage/uploads/{id}/complete", func(w http.ResponseWriter, _ *http.Request) {
		m.mu.Lock()
		m.complete++
		m.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"content_blob_id": "blob-1", "checksum_sha256": "sha"})
	})
	mux.HandleFunc("POST /api/v1/workspaces/{ws}/documents:upsert", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		m.mu.Lock()
		m.upserts++
		m.lastUpsert = b
		m.mu.Unlock()
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]any{"document_id": "doc-1", "created": true, "version_created": true})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL + "/api/v1"
}

func newPipeline(t *testing.T) (context.Context, *store.Store, *Worker, *httptest.Server, *mockSeDoc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)

	dsn, cleanup, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	require.NoError(t, database.RunMigrations(dsn, "../../migrations"))
	// The integration DB is single-tenant (no tenant RLS); the testcontainer
	// connects as a BYPASSRLS superuser, so opt out of the posture gate.
	cfg := database.DefaultPoolConfig()
	cfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	st := store.New(pool)
	mock := &mockSeDoc{}
	doc := sedoc.New(mock.server(t), "vdms_test")
	syncer := New(st, doc, nil, Config{WorkspaceID: "ws-1", Buckets: 16})
	// Concurrency 1 so the two jobs process in insertion order (customer before
	// document) deterministically.
	worker := NewWorker(st, syncer, zerolog.Nop(), WorkerOptions{Concurrency: 1})

	ingress := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux := http.NewServeMux()
		NewIngress(st, zerolog.Nop()).Register(mux)
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(ingress.Close)
	return ctx, st, worker, ingress, mock
}

func post(t *testing.T, srv *httptest.Server, body string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Post(srv.URL+"/webhooks/erp", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// drainUntilIdle runs the worker until no pending rows remain.
func drainUntilIdle(ctx context.Context, t *testing.T, st *store.Store, w *Worker) {
	t.Helper()
	for i := 0; i < 20; i++ {
		w.drain(ctx)
		pend, err := st.ListSyncLog(ctx, "pending", 100)
		require.NoError(t, err)
		if len(pend) == 0 {
			return
		}
	}
	t.Fatal("jobs did not drain")
}

func TestPipeline_ProvisionsAndUpserts(t *testing.T) {
	ctx, st, worker, ingress, mock := newPipeline(t)

	code, out := post(t, ingress, `{"erp_event_id":"e1","kind":"customer.created","customer_ref":"CUST-1","name":"Acme"}`)
	require.Equal(t, http.StatusAccepted, code)
	require.Equal(t, false, out["duplicate"])

	code, _ = post(t, ingress, `{"erp_event_id":"e2","kind":"document.committed","customer_ref":"CUST-1",`+
		`"doc_type":"invoice","doc_number":"188","status":"confirmed","mime":"application/pdf","bytes":"aGVsbG8="}`)
	require.Equal(t, http.StatusAccepted, code)

	drainUntilIdle(ctx, t, st, worker)

	// Provisioning: bucket + main + 6 subfolders = 8 folder POSTs.
	mock.mu.Lock()
	require.Equal(t, 8, mock.folderPOSTs, "bucket + main + 6 subfolders")
	require.Equal(t, 1, mock.initiate)
	require.Equal(t, 1, mock.complete)
	require.Equal(t, 1, mock.upserts)
	lastUpsert := mock.lastUpsert
	mock.mu.Unlock()

	// The upsert routed to the Invoices subfolder, keyed by type-prefixed id.
	require.Equal(t, "invoice-188", lastUpsert["external_id"])
	require.Equal(t, "invoice", lastUpsert["document_class"])
	require.Equal(t, "folder-invoices", lastUpsert["folder_id"])

	// State: customer mapped with 6 subfolders, both jobs done.
	m, err := st.GetCustomer(ctx, "CUST-1")
	require.NoError(t, err)
	require.Len(t, m.SubfolderIDs, 6)
	require.Equal(t, "folder-invoices", m.SubfolderIDs["invoice"])
	done, err := st.ListSyncLog(ctx, "done", 100)
	require.NoError(t, err)
	require.Len(t, done, 2)
}

func TestPipeline_DuplicateEventIsDeduped(t *testing.T) {
	ctx, st, worker, ingress, mock := newPipeline(t)

	code, out := post(t, ingress, `{"erp_event_id":"e1","kind":"customer.created","customer_ref":"CUST-1","name":"Acme"}`)
	require.Equal(t, http.StatusAccepted, code)
	require.Equal(t, false, out["duplicate"])
	drainUntilIdle(ctx, t, st, worker)

	mock.mu.Lock()
	folderPOSTs := mock.folderPOSTs
	mock.mu.Unlock()
	require.Equal(t, 8, folderPOSTs)

	// Re-fire the SAME erp_event_id → deduped at ingress, no new job, no new
	// SeDoc work.
	code, out = post(t, ingress, `{"erp_event_id":"e1","kind":"customer.created","customer_ref":"CUST-1","name":"Acme"}`)
	require.Equal(t, http.StatusAccepted, code)
	require.Equal(t, true, out["duplicate"], "same erp_event_id must dedup")
	drainUntilIdle(ctx, t, st, worker)

	mock.mu.Lock()
	require.Equal(t, folderPOSTs, mock.folderPOSTs, "duplicate event created no new folders")
	mock.mu.Unlock()

	all, err := st.ListSyncLog(ctx, "", 100)
	require.NoError(t, err)
	require.Len(t, all, 1, "duplicate event did not create a second sync_log row")
}
