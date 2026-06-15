package sedoc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeSeDoc is an httptest stand-in for the SeDoc gateway that records the
// Idempotency-Key + path of every write, so tests assert the client's contract
// without a live DMS.
type fakeSeDoc struct {
	mu    sync.Mutex
	calls []call
}
type call struct {
	method, path, idem string
}

func (f *fakeSeDoc) record(r *http.Request) {
	f.mu.Lock()
	f.calls = append(f.calls, call{r.Method, r.URL.Path, r.Header.Get("Idempotency-Key")})
	f.mu.Unlock()
}

func newFake(t *testing.T) (*fakeSeDoc, *Client) {
	t.Helper()
	f := &fakeSeDoc{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/storage/uploads/initiate", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		// presigned URL points back here so the client's PUT has somewhere to go.
		json.NewEncoder(w).Encode(map[string]any{
			"upload_id": "up-1", "presigned_put_url": "http://" + r.Host + "/put", "storage_key": "k",
		})
	})
	mux.HandleFunc("PUT /put", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("POST /api/v1/storage/uploads/{id}/complete", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		json.NewEncoder(w).Encode(map[string]any{"content_blob_id": "blob-1", "checksum_sha256": "sha"})
	})
	mux.HandleFunc("POST /api/v1/workspaces/{ws}/documents:upsert", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]any{"document_id": "doc-1", "created": true, "version_created": true})
	})
	mux.HandleFunc("POST /api/v1/ingest", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]any{"ingestion_item_id": "ing-1", "status": "received"})
	})
	mux.HandleFunc("POST /api/v1/workspaces/{ws}/folders", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		json.NewEncoder(w).Encode(map[string]any{"id": "folder-1"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return f, New(srv.URL+"/api/v1", "vdms_test")
}

func TestUploadBlobUsesDistinctIdempotencyKeys(t *testing.T) {
	f, c := newFake(t)
	res, err := c.UploadBlob(context.Background(), "job-1", "us-east-1", "f.pdf", "application/pdf", []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if res.ContentBlobID != "blob-1" {
		t.Fatalf("content_blob_id = %q", res.ContentBlobID)
	}
	// initiate + complete MUST carry DISTINCT keys (different paths; one shared
	// key would 422 as KEY_REUSED).
	got := map[string]string{}
	for _, cl := range f.calls {
		got[cl.path] = cl.idem
	}
	if got["/api/v1/storage/uploads/initiate"] != "job-1:initiate" {
		t.Fatalf("initiate key = %q", got["/api/v1/storage/uploads/initiate"])
	}
	if got["/api/v1/storage/uploads/up-1/complete"] != "job-1:complete" {
		t.Fatalf("complete key = %q", got["/api/v1/storage/uploads/up-1/complete"])
	}
}

func TestUpsertAndIngestSendKeys(t *testing.T) {
	f, c := newFake(t)
	if _, err := c.UpsertByExternalKey(context.Background(), "job-1:upsert", UpsertInput{WorkspaceID: "ws", ExternalID: "invoice-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Ingest(context.Background(), "job-2:ingest", IngestInput{WorkspaceID: "ws"}); err != nil {
		t.Fatal(err)
	}
	keys := map[string]string{}
	for _, cl := range f.calls {
		keys[cl.path] = cl.idem
	}
	if keys["/api/v1/workspaces/ws/documents:upsert"] != "job-1:upsert" {
		t.Fatalf("upsert key = %q", keys["/api/v1/workspaces/ws/documents:upsert"])
	}
	if keys["/api/v1/ingest"] != "job-2:ingest" {
		t.Fatalf("ingest key = %q", keys["/api/v1/ingest"])
	}
}

func TestErrorClassification(t *testing.T) {
	cases := []struct {
		status        int
		typ           string
		retryAfter    string
		wantRetryable bool
		wantAfter     time.Duration
	}{
		{409, "IDEMPOTENCY_IN_PROGRESS", "", true, 0},
		{409, "CONFLICT", "", false, 0}, // a non-idempotency 409 (e.g. trashed doc) is terminal
		{422, "IDEMPOTENCY_KEY_REUSED", "", false, 0},
		{400, "VALIDATION", "", false, 0},
		{403, "FORBIDDEN", "", false, 0},
		{429, "", "2", true, 2 * time.Second},
		{503, "IDEMPOTENCY_STORE_UNAVAILABLE", "", true, 0},
		{500, "", "", true, 0},
	}
	for _, tc := range cases {
		f := &fakeSeDoc{}
		mux := http.NewServeMux()
		mux.HandleFunc("POST /api/v1/ingest", func(w http.ResponseWriter, r *http.Request) {
			f.record(r)
			if tc.retryAfter != "" {
				w.Header().Set("Retry-After", tc.retryAfter)
			}
			w.WriteHeader(tc.status)
			json.NewEncoder(w).Encode(map[string]any{"type": tc.typ, "message": "x", "correlation_id": "corr-9"})
		})
		srv := httptest.NewServer(mux)
		c := New(srv.URL+"/api/v1", "k")
		_, err := c.Ingest(context.Background(), "j", IngestInput{WorkspaceID: "ws"})
		srv.Close()
		if err == nil {
			t.Fatalf("%d/%s: expected error", tc.status, tc.typ)
		}
		if IsRetryable(err) != tc.wantRetryable {
			t.Fatalf("%d/%s: retryable=%v want %v", tc.status, tc.typ, IsRetryable(err), tc.wantRetryable)
		}
		if RetryAfter(err) != tc.wantAfter {
			t.Fatalf("%d/%s: retryAfter=%v want %v", tc.status, tc.typ, RetryAfter(err), tc.wantAfter)
		}
		if CorrelationID(err) != "corr-9" {
			t.Fatalf("%d/%s: correlation_id=%q", tc.status, tc.typ, CorrelationID(err))
		}
	}
}
