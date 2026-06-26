// Unit tests for the DMS-contract adapter (dms_adapter.go) — the surface the
// CRM's dmsSync module calls. These run against an in-process fake SeDoc backend
// (httptest) with prov=nil (flat per-doc-class folders), so they need NO Postgres
// and run as plain `go test ./internal/bff/...` (no build tag).
//
// Coverage: auth gate, multipart push (initiate→PUT→complete→upsert), the
// folder-ensure fallback, pre-create dedup search, folder list/create, the
// in-ERP read surfaces (document detail + content download), the review-queue
// passthrough, and SeDoc error-status propagation.
package bff

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/integration/internal/sedoc"
)

// fakeSedoc is an in-process stand-in for the SeDoc document gateway. It records
// the calls the adapter makes and returns canned, contract-shaped responses.
type fakeSedoc struct {
	mu sync.Mutex
	// recorded calls
	initiateHits int
	putHits      int
	completeHits int
	upsertHits   int
	folderPosts  int
	lastUpsert   map[string]any
	lastFolder   map[string]any
	// behaviour toggles
	externalKeyFound string // non-empty → byExternalKey returns this document id
	docCreated       bool   // upsert reports created=true (201) vs versioned (200)
	failUpsert       int    // when >0, upsert responds with this status + error envelope
}

func (f *fakeSedoc) URL(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()

	// ---- upload (3-step) ----
	mux.HandleFunc("POST /api/v1/storage/uploads/initiate", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.initiateHits++
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"upload_id":         "up-1",
			"presigned_put_url": "http://" + r.Host + "/put",
		})
	})
	mux.HandleFunc("PUT /put", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		f.mu.Lock()
		f.putHits++
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /api/v1/storage/uploads/{id}/complete", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		f.completeHits++
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content_blob_id": "blob-1", "checksum_sha256": "deadbeef", "size_bytes": 3,
		})
	})

	// ---- folders ----
	mux.HandleFunc("POST /api/v1/workspaces/{wid}/folders", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.folderPosts++
		f.lastFolder = body
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "folder-ensured"})
	})
	mux.HandleFunc("GET /api/v1/workspaces/{wid}/folders", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"folders": []map[string]any{
				{"id": "f1", "name": "ERP Invoices", "parentFolderId": "", "childFolderCount": 2},
			},
		})
	})

	// ---- upsert ----
	mux.HandleFunc("POST /api/v1/workspaces/{wid}/documents:upsert", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		fail := f.failUpsert
		f.upsertHits++
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.lastUpsert = body
		created := f.docCreated
		f.mu.Unlock()
		if fail > 0 {
			w.WriteHeader(fail)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"type": "VALIDATION", "message": "bad", "correlation_id": "corr-9",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"document_id": "doc-1", "current_version_id": "ver-1",
			"current_version_number": 1, "created": created, "version_created": true,
		})
	})

	// ---- byExternalKey (dedup) ----
	mux.HandleFunc("GET /api/v1/documents:byExternalKey", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		id := f.externalKeyFound
		f.mu.Unlock()
		if id == "" {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"type": "NOT_FOUND", "message": "no doc"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"document_id": id})
	})

	// ---- read surfaces ----
	mux.HandleFunc("GET /api/v1/documents/{id}/versions", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"versions": []map[string]any{{"id": "ver-1", "versionNumber": 1}},
		})
	})
	mux.HandleFunc("GET /api/v1/documents/{id}/content", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF-bytes"))
	})
	mux.HandleFunc("GET /api/v1/documents/{id}", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": r.PathValue("id"), "title": "Invoice INV-1", "documentClass": "invoice",
		})
	})

	// ---- review queue ----
	mux.HandleFunc("GET /api/v1/review-queue", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"id":"rq-1"}]}`))
	})
	mux.HandleFunc("GET /api/v1/review-queue/{id}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"` + r.PathValue("id") + `","ocr_text":"hello"}`))
	})
	mux.HandleFunc("POST /api/v1/review-queue/{id}/resolve", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"resolved":true}`))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL + "/api/v1"
}

// newAdapter wires a DMSAdapter onto a fresh fake SeDoc. token gates auth; prov
// is nil so pushes take the flat per-doc-class folder path (no Postgres).
func newAdapter(t *testing.T, f *fakeSedoc, token string) http.Handler {
	t.Helper()
	doc := sedoc.New(f.URL(t), "vdms_service_key")
	a := NewDMSAdapter(doc, "ws-1", token, nil, zerolog.Nop())
	mux := http.NewServeMux()
	a.Register(mux)
	return mux
}

// pushBody builds the CRM's exact multipart shape: file part FIRST, then fields.
func pushBody(t *testing.T, entityType, entityID, customMeta string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "doc.pdf")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write([]byte("PDF"))
	_ = mw.WriteField("erp_entity_type", entityType)
	_ = mw.WriteField("erp_entity_id", entityID)
	_ = mw.WriteField("dms_doc_type", entityType)
	_ = mw.WriteField("document_number", "INV-1")
	if customMeta != "" {
		_ = mw.WriteField("custom_metadata", customMeta)
	}
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

// ---- auth gate -------------------------------------------------------------

func TestAdapter_AuthGate(t *testing.T) {
	h := newAdapter(t, &fakeSedoc{}, "secret-token")

	// health is exempt — no bearer needed.
	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("health = %d, want 200", w.Code)
	}

	// a protected route with no/invalid bearer → 401.
	for _, auth := range []string{"", "Bearer wrong"} {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/review-queue", nil)
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("review-queue auth=%q = %d, want 401", auth, w.Code)
		}
	}

	// correct bearer → passes the gate.
	r = httptest.NewRequest(http.MethodGet, "/api/v1/review-queue", nil)
	r.Header.Set("Authorization", "Bearer secret-token")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("review-queue with valid bearer = %d, want 200", w.Code)
	}
}

func TestAdapter_EmptyTokenAcceptsAnyone(t *testing.T) {
	h := newAdapter(t, &fakeSedoc{}, "") // dev mode
	r := httptest.NewRequest(http.MethodGet, "/api/v1/review-queue", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("dev-mode review-queue = %d, want 200", w.Code)
	}
}

// ---- push (upload → ensure-folder → upsert) --------------------------------

func doPush(t *testing.T, h http.Handler, body *bytes.Buffer, ct string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/documents", body)
	r.Header.Set("Content-Type", ct)
	r.Header.Set("Authorization", "Bearer secret-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func TestAdapter_PushCreatesDocument(t *testing.T) {
	f := &fakeSedoc{docCreated: true}
	h := newAdapter(t, f, "secret-token")

	body, ct := pushBody(t, "invoice", "42", `{"erp_customer_name":"Acme"}`)
	w, out := doPush(t, h, body, ct)

	if w.Code != http.StatusCreated {
		t.Fatalf("push created = %d, want 201 (body=%s)", w.Code, w.Body.String())
	}
	if out["external_id"] != "erp:invoice:42" {
		t.Fatalf("external_id = %v, want erp:invoice:42", out["external_id"])
	}
	if out["id"] != "doc-1" || out["document_id"] != "doc-1" {
		t.Fatalf("id/document_id = %v/%v, want doc-1", out["id"], out["document_id"])
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	// full 3-step upload happened, plus the flat-folder ensure + the upsert.
	if f.initiateHits != 1 || f.putHits != 1 || f.completeHits != 1 {
		t.Fatalf("upload steps = init %d/put %d/complete %d, want 1/1/1", f.initiateHits, f.putHits, f.completeHits)
	}
	if f.folderPosts != 1 {
		t.Fatalf("folder ensure POSTs = %d, want 1 (flat fallback)", f.folderPosts)
	}
	if f.lastFolder["name"] != "ERP Invoices" {
		t.Fatalf("fallback folder name = %v, want 'ERP Invoices'", f.lastFolder["name"])
	}
	if f.upsertHits != 1 {
		t.Fatalf("upsert hits = %d, want 1", f.upsertHits)
	}
	if f.lastUpsert["external_id"] != "erp:invoice:42" {
		t.Fatalf("upsert external_id = %v", f.lastUpsert["external_id"])
	}
	// the adapter stamps dms_transferred_at into custom_metadata.
	meta, _ := f.lastUpsert["custom_metadata"].(map[string]any)
	if meta == nil || meta["dms_transferred_at"] == nil {
		t.Fatalf("custom_metadata missing dms_transferred_at: %v", f.lastUpsert["custom_metadata"])
	}
}

func TestAdapter_PushVersionsExisting(t *testing.T) {
	f := &fakeSedoc{docCreated: false} // upsert reports a new version, not a create
	h := newAdapter(t, f, "secret-token")
	body, ct := pushBody(t, "invoice", "42", "")
	w, out := doPush(t, h, body, ct)
	if w.Code != http.StatusOK {
		t.Fatalf("re-push = %d, want 200 (versioned)", w.Code)
	}
	if out["version_created"] != true {
		t.Fatalf("version_created = %v, want true", out["version_created"])
	}
}

func TestAdapter_PushMissingFile(t *testing.T) {
	h := newAdapter(t, &fakeSedoc{}, "secret-token")
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("erp_entity_type", "invoice")
	_ = mw.WriteField("erp_entity_id", "42")
	_ = mw.Close()
	w, _ := doPush(t, h, &buf, mw.FormDataContentType())
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing file = %d, want 400", w.Code)
	}
}

func TestAdapter_PushMissingEntityFields(t *testing.T) {
	h := newAdapter(t, &fakeSedoc{}, "secret-token")
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "doc.pdf")
	_, _ = fw.Write([]byte("PDF"))
	_ = mw.Close() // no erp_entity_type / erp_entity_id
	w, _ := doPush(t, h, &buf, mw.FormDataContentType())
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing entity fields = %d, want 400", w.Code)
	}
}

func TestAdapter_PushPropagatesSedocError(t *testing.T) {
	f := &fakeSedoc{failUpsert: http.StatusUnprocessableEntity}
	h := newAdapter(t, f, "secret-token")
	body, ct := pushBody(t, "invoice", "42", "")
	w, out := doPush(t, h, body, ct)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("upstream 422 → adapter %d, want 422", w.Code)
	}
	if !strings.Contains(w.Body.String(), "corr-9") && out["correlation_id"] != "corr-9" {
		t.Fatalf("correlation id not surfaced: %s", w.Body.String())
	}
}

// ---- dedup search ----------------------------------------------------------

func doSearch(t *testing.T, h http.Handler, filter map[string]any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"filter": filter, "limit": 1})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/search", bytes.NewReader(b))
	r.Header.Set("Authorization", "Bearer secret-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func TestAdapter_SearchDedupHit(t *testing.T) {
	f := &fakeSedoc{externalKeyFound: "doc-existing"}
	h := newAdapter(t, f, "secret-token")
	w, out := doSearch(t, h, map[string]any{
		"custom_metadata.erp_entity_type": "invoice",
		"custom_metadata.erp_invoice_id":  float64(42),
	})
	if w.Code != http.StatusOK {
		t.Fatalf("search = %d, want 200", w.Code)
	}
	data, _ := out["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("dedup hit data len = %d, want 1 (out=%v)", len(data), out)
	}
	hit, _ := data[0].(map[string]any)
	if hit["id"] != "doc-existing" {
		t.Fatalf("hit id = %v, want doc-existing", hit["id"])
	}
}

func TestAdapter_SearchDedupMiss(t *testing.T) {
	f := &fakeSedoc{externalKeyFound: ""} // byExternalKey 404s
	h := newAdapter(t, f, "secret-token")
	w, out := doSearch(t, h, map[string]any{
		"custom_metadata.erp_entity_type": "invoice",
		"custom_metadata.erp_invoice_id":  float64(99),
	})
	if w.Code != http.StatusOK {
		t.Fatalf("search miss = %d, want 200", w.Code)
	}
	if data, _ := out["data"].([]any); len(data) != 0 {
		t.Fatalf("miss data len = %d, want 0", len(data))
	}
}

func TestAdapter_SearchNoIdentityIsEmpty(t *testing.T) {
	h := newAdapter(t, &fakeSedoc{}, "secret-token")
	w, out := doSearch(t, h, map[string]any{"title": "something"}) // no erp identity
	if w.Code != http.StatusOK {
		t.Fatalf("search = %d, want 200", w.Code)
	}
	if data, _ := out["data"].([]any); len(data) != 0 {
		t.Fatalf("no-identity data len = %d, want 0", len(data))
	}
}

// ---- folder list / create --------------------------------------------------

func TestAdapter_ListFolders(t *testing.T) {
	h := newAdapter(t, &fakeSedoc{}, "secret-token")
	r := httptest.NewRequest(http.MethodGet, "/folders", nil)
	r.Header.Set("Authorization", "Bearer secret-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("listFolders = %d, want 200", w.Code)
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	folders, _ := out["folders"].([]any)
	if len(folders) != 1 {
		t.Fatalf("folders len = %d, want 1", len(folders))
	}
	f0, _ := folders[0].(map[string]any)
	if f0["hasChildren"] != true { // childFolderCount 2 → hasChildren true
		t.Fatalf("hasChildren = %v, want true", f0["hasChildren"])
	}
}

func TestAdapter_CreateWorkspaceFolder(t *testing.T) {
	f := &fakeSedoc{}
	h := newAdapter(t, f, "secret-token")
	b, _ := json.Marshal(map[string]any{"name": "Contracts"})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/ws-9/folders", bytes.NewReader(b))
	r.Header.Set("Authorization", "Bearer secret-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("createFolder = %d, want 201", w.Code)
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["id"] != "folder-ensured" {
		t.Fatalf("folder id = %v", out["id"])
	}
}

func TestAdapter_CreateWorkspaceFolderRequiresName(t *testing.T) {
	h := newAdapter(t, &fakeSedoc{}, "secret-token")
	r := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/ws-9/folders", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer secret-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("createFolder no-name = %d, want 400", w.Code)
	}
}

// ---- in-ERP read surfaces --------------------------------------------------

func TestAdapter_GetDocumentIncludesVersions(t *testing.T) {
	h := newAdapter(t, &fakeSedoc{}, "secret-token")
	r := httptest.NewRequest(http.MethodGet, "/api/v1/documents/doc-1", nil)
	r.Header.Set("Authorization", "Bearer secret-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("getDocument = %d, want 200", w.Code)
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["document"] == nil {
		t.Fatalf("missing document field: %s", w.Body.String())
	}
	if vers, _ := out["versions"].([]any); len(vers) != 1 {
		t.Fatalf("versions len = %d, want 1", len(vers))
	}
}

func TestAdapter_DownloadStreamsContent(t *testing.T) {
	h := newAdapter(t, &fakeSedoc{}, "secret-token")
	r := httptest.NewRequest(http.MethodGet, "/api/v1/documents/doc-1/content", nil)
	r.Header.Set("Authorization", "Bearer secret-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("download = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Fatalf("content-type = %q, want application/pdf", ct)
	}
	if w.Body.String() != "%PDF-bytes" {
		t.Fatalf("body = %q, want %%PDF-bytes", w.Body.String())
	}
}

// ---- review-queue passthrough ----------------------------------------------

func TestAdapter_ReviewQueuePassthrough(t *testing.T) {
	h := newAdapter(t, &fakeSedoc{}, "secret-token")
	cases := []struct {
		method, path, wantSub string
	}{
		{http.MethodGet, "/api/v1/review-queue", `"items"`},
		{http.MethodGet, "/api/v1/review-queue/rq-1", `"ocr_text"`},
		{http.MethodPost, "/api/v1/review-queue/rq-1/resolve", `"resolved":true`},
	}
	for _, tc := range cases {
		var bodyR io.Reader
		if tc.method == http.MethodPost {
			bodyR = strings.NewReader(`{"decision":"accept"}`)
		}
		r := httptest.NewRequest(tc.method, tc.path, bodyR)
		r.Header.Set("Authorization", "Bearer secret-token")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s = %d, want 200", tc.method, tc.path, w.Code)
		}
		if !strings.Contains(w.Body.String(), tc.wantSub) {
			t.Fatalf("%s %s body %q missing %q", tc.method, tc.path, w.Body.String(), tc.wantSub)
		}
	}
}
