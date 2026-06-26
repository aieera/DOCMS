// Cross-repo contract / pipeline test. It replays the exact call sequence the
// CRM's dmsSync worker (crmapp Backend/src/core/dmsSync) performs against the
// DMS adapter, end-to-end, through the REAL adapter onto a stateful fake SeDoc:
//
//   1. pre-create dedup search  (dmsApi.findExistingDocument) → MISS
//   2. multipart push           (DMSService.pushDocument)     → 201 created
//   3. re-push the same record  (idempotent re-run)           → dedup now HITS
//   4. read the document back   (DMSExplorer getDocument)     → metadata + versions
//
// This guards the contract seam between the two repos in one runnable `go test`
// (no Postgres, no Redis): the CRM's request shapes in, SeDoc's shapes out.
package bff

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/integration/internal/sedoc"
)

// statefulSedoc remembers documents keyed by external_id so a second push for
// the same ERP record is recognised as a dedup hit — modelling SeDoc's real
// external-key upsert behaviour.
type statefulSedoc struct {
	mu      sync.Mutex
	byKey   map[string]string // external_id → document_id
	nextNum int
}

func (f *statefulSedoc) URL(t *testing.T) string {
	t.Helper()
	f.byKey = map[string]string{}
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/v1/storage/uploads/initiate", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"upload_id": "up-1", "presigned_put_url": "http://" + r.Host + "/put"})
	})
	mux.HandleFunc("PUT /put", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /api/v1/storage/uploads/{id}/complete", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"content_blob_id": "blob-1", "checksum_sha256": "sha", "size_bytes": 3})
	})
	mux.HandleFunc("POST /api/v1/workspaces/{wid}/folders", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "folder-1"})
	})
	mux.HandleFunc("GET /api/v1/workspaces/{wid}/folders", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"folders": []any{}})
	})

	// upsert: create on first sight of an external_id, version thereafter.
	mux.HandleFunc("POST /api/v1/workspaces/{wid}/documents:upsert", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ExternalID string `json:"external_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		id, existed := f.byKey[body.ExternalID]
		if !existed {
			f.nextNum++
			id = "doc-1"
			f.byKey[body.ExternalID] = id
		}
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"document_id": id, "current_version_id": "ver", "current_version_number": 1,
			"created": !existed, "version_created": true,
		})
	})

	// byExternalKey: the dedup probe.
	mux.HandleFunc("GET /api/v1/documents:byExternalKey", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("external_id")
		f.mu.Lock()
		id, ok := f.byKey[key]
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"type": "NOT_FOUND", "message": "miss"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"document_id": id})
	})

	mux.HandleFunc("GET /api/v1/documents/{id}/versions", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"versions": []map[string]any{{"id": "ver", "versionNumber": 1}}})
	})
	mux.HandleFunc("GET /api/v1/documents/{id}", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": r.PathValue("id"), "title": "Invoice INV-7", "externalId": "erp:invoice:7"})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL + "/api/v1"
}

func TestCRMContract_FullPushPipeline(t *testing.T) {
	f := &statefulSedoc{}
	doc := sedoc.New(f.URL(t), "vdms_service_key")
	a := NewDMSAdapter(doc, "ws-1", "crm-bearer", nil, zerolog.Nop())
	mux := http.NewServeMux()
	a.Register(mux)

	auth := func(r *http.Request) *http.Request { r.Header.Set("Authorization", "Bearer crm-bearer"); return r }

	// the CRM's dedup filter for invoice #7.
	dedupFilter := map[string]any{
		"custom_metadata.erp_entity_type": "invoice",
		"custom_metadata.erp_invoice_id":  float64(7),
	}
	search := func() map[string]any {
		b, _ := json.Marshal(map[string]any{"filter": dedupFilter, "limit": 1})
		r := auth(httptest.NewRequest(http.MethodPost, "/api/v1/search", bytes.NewReader(b)))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("search = %d", w.Code)
		}
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}

	// 1. pre-create dedup → MISS.
	if data, _ := search()["data"].([]any); len(data) != 0 {
		t.Fatalf("step1: expected dedup miss, got %d hits", len(data))
	}

	// 2. push (CRM multipart: file part first, then fields).
	push := func() (int, map[string]any) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("file", "invoice-INV-7.pdf")
		_, _ = fw.Write([]byte("PDF"))
		_ = mw.WriteField("erp_entity_type", "invoice")
		_ = mw.WriteField("erp_entity_id", "7")
		_ = mw.WriteField("dms_doc_type", "invoice")
		_ = mw.WriteField("document_number", "INV-7")
		_ = mw.WriteField("custom_metadata", `{"erp_entity_type":"invoice","erp_invoice_id":7,"erp_customer_name":"Acme"}`)
		_ = mw.Close()
		r := auth(httptest.NewRequest(http.MethodPost, "/api/v1/documents", &buf))
		r.Header.Set("Content-Type", mw.FormDataContentType())
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	code, out := push()
	if code != http.StatusCreated {
		t.Fatalf("step2: first push = %d, want 201 created", code)
	}
	docID, _ := out["id"].(string)
	if docID == "" {
		t.Fatalf("step2: no document id returned: %v", out)
	}

	// 3. re-push the same record → dedup probe now HITS, upsert versions (200).
	if data, _ := search()["data"].([]any); len(data) != 1 {
		t.Fatalf("step3: expected dedup hit after push, got %d", len(data))
	}
	code, out = push()
	if code != http.StatusOK {
		t.Fatalf("step3: re-push = %d, want 200 (versioned, not a duplicate create)", code)
	}
	if out["created"] != false {
		t.Fatalf("step3: re-push created = %v, want false (idempotent)", out["created"])
	}

	// 4. read it back through the in-ERP surface.
	r := auth(httptest.NewRequest(http.MethodGet, "/api/v1/documents/"+docID, nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("step4: getDocument = %d, want 200", w.Code)
	}
	var read map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &read)
	gotDoc, _ := read["document"].(map[string]any)
	if gotDoc == nil || gotDoc["title"] != "Invoice INV-7" {
		t.Fatalf("step4: read-back doc = %v, want title 'Invoice INV-7'", read["document"])
	}
	if vers, _ := read["versions"].([]any); len(vers) != 1 {
		t.Fatalf("step4: versions len = %d, want 1", len(vers))
	}
}
