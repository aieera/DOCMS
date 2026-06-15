//go:build integration
// +build integration

// Backfill acceptance tests (Prompt 2). A fake SeDoc that MODELS idempotency
// (folders keyed by Idempotency-Key; :upsert deduped on external_id+checksum;
// /ingest deduped on Idempotency-Key) proves the headline guarantee: running the
// same backfill twice creates no new folders, documents, or versions. Plus:
// per-item failures are recorded, progress is queryable, and every SeDoc write
// flows through the shared rate limiter.
//
// Run with: go test -tags integration ./integration/internal/backfill/...
package backfill

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/integration/internal/erp"
	"github.com/aieera/sedoc/integration/internal/sedoc"
	"github.com/aieera/sedoc/integration/internal/store"
	syncpkg "github.com/aieera/sedoc/integration/internal/sync"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

var samplePDF = []byte("%PDF-1.4\ntest\n%%EOF\n")

// idempotentSeDoc models the SeDoc write surface with real idempotency so a
// re-run is observably a no-op.
type idempotentSeDoc struct {
	mu             sync.Mutex
	folders        map[string]string // Idempotency-Key → folder id
	folderCreates  int
	docChecksums   map[string]string // external_id → last checksum
	versionCreates int
	ingests        map[string]string // Idempotency-Key → ingestion id
	ingestCreates  int
}

func newIdempotentSeDoc() *idempotentSeDoc {
	return &idempotentSeDoc{folders: map[string]string{}, docChecksums: map[string]string{}, ingests: map[string]string{}}
}

func (m *idempotentSeDoc) server(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/workspaces/{ws}/folders", func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		m.mu.Lock()
		id, ok := m.folders[key]
		if !ok {
			m.folderCreates++
			id = "folder-" + strconv.Itoa(m.folderCreates)
			m.folders[key] = id
		}
		m.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id})
	})
	mux.HandleFunc("PATCH /api/v1/folders/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("POST /api/v1/storage/uploads/initiate", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"upload_id": "up", "presigned_put_url": "http://" + r.Host + "/put"})
	})
	mux.HandleFunc("PUT /put", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("POST /api/v1/storage/uploads/{id}/complete", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			SHA256 string `json:"sha256_hash"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		_ = json.NewEncoder(w).Encode(map[string]any{"content_blob_id": "blob-" + b.SHA256, "checksum_sha256": b.SHA256})
	})
	mux.HandleFunc("POST /api/v1/workspaces/{ws}/documents:upsert", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			ExternalID string `json:"external_id"`
			Version    struct {
				BlobChecksum string `json:"blob_checksum"`
			} `json:"version"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		m.mu.Lock()
		prev, exists := m.docChecksums[b.ExternalID]
		created, versionCreated := false, false
		if !exists {
			created, versionCreated = true, true
			m.versionCreates++
		} else if prev != b.Version.BlobChecksum {
			versionCreated = true
			m.versionCreates++
		}
		m.docChecksums[b.ExternalID] = b.Version.BlobChecksum
		m.mu.Unlock()
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"document_id": "doc-" + b.ExternalID, "created": created, "version_created": versionCreated,
		})
	})
	mux.HandleFunc("POST /api/v1/ingest", func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		m.mu.Lock()
		id, ok := m.ingests[key]
		if !ok {
			m.ingestCreates++
			id = "ing-" + strconv.Itoa(m.ingestCreates)
			m.ingests[key] = id
		}
		m.mu.Unlock()
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]any{"ingestion_item_id": id, "status": "received"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL + "/api/v1"
}

// fakeERP is an in-memory erp.Lister + erp.DocumentSource.
type fakeERP struct {
	customers []erp.Customer
	docs      map[string][]erp.DocumentRef
}

func (f *fakeERP) ListCustomers(context.Context) ([]erp.Customer, error) { return f.customers, nil }
func (f *fakeERP) ListDocuments(_ context.Context, ref string) ([]erp.DocumentRef, error) {
	return f.docs[ref], nil
}
func (f *fakeERP) Fetch(context.Context, string) ([]byte, string, error) {
	return samplePDF, "application/pdf", nil
}

// inventory builds n customers, each with quote/po/invoice + one attachment.
func inventory(n int) *fakeERP {
	f := &fakeERP{docs: map[string][]erp.DocumentRef{}}
	for i := 1; i <= n; i++ {
		ref := "CUST-" + strconv.Itoa(i)
		f.customers = append(f.customers, erp.Customer{Ref: ref, Name: "Customer " + strconv.Itoa(i)})
		f.docs[ref] = []erp.DocumentRef{
			{Kind: "document", DocType: erp.DocQuote, DocNumber: ref + "-Q1", Status: "sent", FileRef: "f-" + ref + "-q", Mime: "application/pdf"},
			{Kind: "document", DocType: erp.DocPO, DocNumber: ref + "-P1", Status: "confirmed", FileRef: "f-" + ref + "-p", Mime: "application/pdf"},
			{Kind: "document", DocType: erp.DocInvoice, DocNumber: ref + "-I1", Status: "confirmed", FileRef: "f-" + ref + "-i", Mime: "application/pdf"},
			{Kind: "attachment", Filename: "contract.pdf", FileRef: "f-" + ref + "-a", Mime: "application/pdf"},
		}
	}
	return f
}

func newBackfillHarness(t *testing.T, src erp.Lister, fetch erp.DocumentSource) (context.Context, *store.Store, *Runner, *sedoc.Limiter, *idempotentSeDoc) {
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
	mock := newIdempotentSeDoc()
	// Rate (100/s, burst 1) below the natural in-process write pace so the limiter
	// genuinely throttles — proving the backfill flows through the shared limiter
	// (the rate-cap math itself is proven in sedoc/limiter_test). ~10ms/write keeps
	// the test sub-second.
	lim := sedoc.NewLimiter(100, 1)
	doc := sedoc.New(mock.server(t), "vdms_test").WithLimiter(lim)
	syncer := syncpkg.New(st, doc, fetch, syncpkg.Config{WorkspaceID: "ws-1", Buckets: 16})
	// Concurrency 1 → deterministic counts and limiter accounting.
	runner := NewRunner(st, syncer, src, 1, zerolog.Nop())
	return ctx, st, runner, lim, mock
}

func runOnce(ctx context.Context, t *testing.T, st *store.Store, runner *Runner) *store.BackfillRun {
	t.Helper()
	id, err := st.CreateBackfillRun(ctx, "erp", "", true)
	require.NoError(t, err)
	run, err := st.GetBackfillRun(ctx, id)
	require.NoError(t, err)
	runner.Process(ctx, run)
	final, err := st.GetBackfillRun(ctx, id)
	require.NoError(t, err)
	return final
}

func TestBackfill_RerunNoDuplicatesAndPaced(t *testing.T) {
	erpSrc := inventory(2)
	ctx, st, runner, lim, mock := newBackfillHarness(t, erpSrc, erpSrc)

	// Run #1.
	r1 := runOnce(ctx, t, st, runner)
	require.Equal(t, "completed", r1.Status)
	require.Equal(t, 10, r1.Total, "2 customers × (1 customer + 3 docs + 1 attachment)")
	require.Equal(t, 10, r1.Processed)
	require.Equal(t, 0, r1.Failed)

	mock.mu.Lock()
	foldersAfter1, versionsAfter1, ingestsAfter1 := mock.folderCreates, mock.versionCreates, mock.ingestCreates
	mock.mu.Unlock()
	require.Equal(t, 6, versionsAfter1, "2 customers × 3 documents = 6 versions")
	require.Equal(t, 2, ingestsAfter1, "2 attachments = 2 ingests")
	require.Greater(t, foldersAfter1, 0)

	// The backfill paced its writes through the shared limiter.
	require.Greater(t, lim.Stats().WaitCount, int64(0), "writes should flow through the rate limiter")

	// Run #2 over the SAME inventory → nothing new anywhere.
	r2 := runOnce(ctx, t, st, runner)
	require.Equal(t, "completed", r2.Status)
	require.Equal(t, 10, r2.Processed)
	require.Equal(t, 0, r2.Failed)

	mock.mu.Lock()
	defer mock.mu.Unlock()
	require.Equal(t, foldersAfter1, mock.folderCreates, "re-run created no new folders")
	require.Equal(t, versionsAfter1, mock.versionCreates, "re-run created no new document versions")
	require.Equal(t, ingestsAfter1, mock.ingestCreates, "re-run created no new ingests")
}

func TestBackfill_RecordsPerItemFailures(t *testing.T) {
	// One customer with a good invoice and a bogus doc_type (no such subfolder →
	// terminal ValidationError in the handler).
	erpSrc := &fakeERP{
		customers: []erp.Customer{{Ref: "CUST-1", Name: "Acme"}},
		docs: map[string][]erp.DocumentRef{"CUST-1": {
			{Kind: "document", DocType: erp.DocInvoice, DocNumber: "188", Status: "confirmed", FileRef: "f-i", Mime: "application/pdf"},
			{Kind: "document", DocType: "bogus", DocNumber: "999", Status: "x", FileRef: "f-b", Mime: "application/pdf"},
		}},
	}
	ctx, st, runner, _, _ := newBackfillHarness(t, erpSrc, erpSrc)

	run := runOnce(ctx, t, st, runner)
	require.Equal(t, "completed", run.Status)
	require.Equal(t, 3, run.Total, "1 customer + 2 documents")
	require.Equal(t, 3, run.Processed)
	require.Equal(t, 1, run.Failed, "the bogus doc_type item failed")

	fails, err := st.ListBackfillFailures(ctx, run.ID, 50)
	require.NoError(t, err)
	require.Len(t, fails, 1)
	require.Equal(t, "CUST-1", fails[0].CustomerRef)
	require.Equal(t, "bogus-999", fails[0].Item)
	require.Contains(t, fails[0].Error, "doc_type")
}
