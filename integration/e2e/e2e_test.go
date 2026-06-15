//go:build integration
// +build integration

// End-to-end system test (Workstream 9 capstone): drive a (mock) ERP event into
// the worker's webhook, let the worker provision folders + commit the document
// to a STATEFUL mock SeDoc, then read it back through the BFF the way the UI
// would — proving the worker, store, and BFF compose into one product, with the
// per-customer authorization boundary intact.
//
// Run with: go test -tags integration ./integration/e2e/...
package e2e

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

	"github.com/aieera/sedoc/integration/internal/bff"
	"github.com/aieera/sedoc/integration/internal/sedoc"
	"github.com/aieera/sedoc/integration/internal/store"
	syncpkg "github.com/aieera/sedoc/integration/internal/sync"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

// fakeSeDoc is a stateful stand-in: it remembers folders (by parent) and
// documents (by folder) so the worker's writes are visible to the BFF's reads —
// enough to exercise the whole product without a live DMS.
type fakeSeDoc struct {
	mu               sync.Mutex
	childrenByParent map[string][]map[string]any // parent folder id → child folder rows
	docsByFolder     map[string][]map[string]any // folder id → document rows
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (f *fakeSeDoc) server(t *testing.T) string {
	t.Helper()
	f.childrenByParent = map[string][]map[string]any{}
	f.docsByFolder = map[string][]map[string]any{}
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/v1/workspaces/{ws}/folders", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Name           string `json:"name"`
			ParentFolderID string `json:"parent_folder_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		id := "f-" + slug(b.Name)
		f.mu.Lock()
		f.childrenByParent[b.ParentFolderID] = append(f.childrenByParent[b.ParentFolderID],
			map[string]any{"id": id, "name": b.Name, "parentFolderId": b.ParentFolderID})
		f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"id": id})
	})
	mux.HandleFunc("GET /api/v1/workspaces/{ws}/folders", func(w http.ResponseWriter, r *http.Request) {
		parent := r.URL.Query().Get("parent_folder_id")
		f.mu.Lock()
		kids := append([]map[string]any{}, f.childrenByParent[parent]...)
		for _, k := range kids {
			k["documentCount"] = len(f.docsByFolder[k["id"].(string)])
		}
		f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"folders": kids})
	})
	mux.HandleFunc("GET /api/v1/workspaces/{ws}/documents", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		docs := append([]map[string]any{}, f.docsByFolder[r.URL.Query().Get("folder_id")]...)
		f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"documents": docs, "pagination": map[string]any{}})
	})
	mux.HandleFunc("POST /api/v1/storage/uploads/initiate", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"upload_id": "up", "presigned_put_url": "http://" + r.Host + "/put"})
	})
	mux.HandleFunc("PUT /put", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("POST /api/v1/storage/uploads/{id}/complete", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"content_blob_id": "blob", "checksum_sha256": "sha"})
	})
	mux.HandleFunc("POST /api/v1/workspaces/{ws}/documents:upsert", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			ExternalID    string `json:"external_id"`
			FolderID      string `json:"folder_id"`
			Title         string `json:"title"`
			DocumentClass string `json:"document_class"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		docID := "doc-" + b.ExternalID
		f.mu.Lock()
		f.docsByFolder[b.FolderID] = append(f.docsByFolder[b.FolderID], map[string]any{
			"id": docID, "title": b.Title, "folderId": b.FolderID,
			"documentClass": b.DocumentClass, "lifecycleState": "active", "externalId": b.ExternalID,
		})
		f.mu.Unlock()
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]any{"document_id": docID, "created": true, "version_created": true})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL + "/api/v1"
}

type allowAuthz struct{ allow map[string]bool }

func (a allowAuthz) CanAccessCustomer(_ context.Context, user, _ string) (bool, error) {
	return a.allow[user], nil
}

func TestEndToEnd_ERPEventToBFFTree(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)

	dsn, cleanup, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	require.NoError(t, database.RunMigrations(dsn, "../migrations"))
	cfg := database.DefaultPoolConfig()
	cfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	st := store.New(pool)
	fake := &fakeSeDoc{}
	doc := sedoc.New(fake.server(t), "vdms_test")
	syncer := syncpkg.New(st, doc, nil, syncpkg.Config{WorkspaceID: "ws-1", Buckets: 16})
	worker := syncpkg.NewWorker(st, syncer, zerolog.Nop(), syncpkg.WorkerOptions{Concurrency: 1, Poll: 100 * time.Millisecond})

	// Worker ingress HTTP server.
	wmux := http.NewServeMux()
	syncpkg.NewIngress(st, zerolog.Nop()).Register(wmux)
	wsrv := httptest.NewServer(wmux)
	t.Cleanup(wsrv.Close)

	// BFF HTTP server (allow "alice", deny everyone else).
	bmux := http.NewServeMux()
	bff.New(st, doc, allowAuthz{allow: map[string]bool{"alice": true}}, "ws-1", zerolog.Nop()).Register(bmux)
	bsrv := httptest.NewServer(bmux)
	t.Cleanup(bsrv.Close)

	// 1. ERP emits customer.created + an invoice document.committed.
	postEvent(t, wsrv, `{"erp_event_id":"e1","kind":"customer.created","customer_ref":"CUST-1","name":"Acme"}`)
	postEvent(t, wsrv, `{"erp_event_id":"e2","kind":"document.committed","customer_ref":"CUST-1",`+
		`"doc_type":"invoice","doc_number":"188","status":"confirmed","mime":"application/pdf","bytes":"aGk="}`)

	// 2. Run the worker loop; wait until both jobs reach 'done'.
	wctx, wcancel := context.WithCancel(ctx)
	defer wcancel()
	go worker.Run(wctx)
	require.Eventually(t, func() bool {
		done, _ := st.ListSyncLog(ctx, "done", 10)
		pend, _ := st.ListSyncLog(ctx, "pending", 10)
		return len(pend) == 0 && len(done) == 2
	}, 30*time.Second, 100*time.Millisecond, "worker did not finish both jobs")

	// 3. BFF: authorized user sees the tree, Invoices folder has 1 doc.
	tree := bffGet(t, bsrv, "/files/customers/CUST-1/tree", "alice", http.StatusOK)
	require.Contains(t, tree, "Invoices")

	// 4. BFF: the invoice doc is listed in the Invoices subfolder.
	m, err := st.GetCustomer(ctx, "CUST-1")
	require.NoError(t, err)
	invFolder := m.SubfolderIDs["invoice"]
	docs := bffGet(t, bsrv, "/files/customers/CUST-1/folders/"+invFolder+"/documents", "alice", http.StatusOK)
	require.Contains(t, docs, "invoice-188", "the committed invoice is visible through the BFF")

	// 5. Unauthorized user can't load the tree.
	_ = bffGet(t, bsrv, "/files/customers/CUST-1/tree", "mallory", http.StatusNotFound)
}

func postEvent(t *testing.T, srv *httptest.Server, body string) {
	t.Helper()
	resp, err := http.Post(srv.URL+"/webhooks/erp", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
}

func bffGet(t *testing.T, srv *httptest.Server, path, user string, wantStatus int) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	req.Header.Set("X-ERP-User", user)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, wantStatus, resp.StatusCode, "%s as %s", path, user)
	buf := make([]byte, 1<<16)
	n, _ := resp.Body.Read(buf)
	return string(buf[:n])
}
