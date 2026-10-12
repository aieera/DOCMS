package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// fakeServer is a minimal SeDoc API for the sync loop: one document in the
// workspace root, a delta feed the test scripts per cycle, and counters for
// downloads and upload initiations.
type fakeServer struct {
	mu        sync.Mutex
	deltas    [][]map[string]any // one entry per Delta call; last one repeats
	calls     int
	content   string
	downloads int
	initiates int
	puts      int
	completes int
	versions  int
	initResp  map[string]any
}

func (f *fakeServer) handler(base func() string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/sync/delta", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		i := f.calls
		if i >= len(f.deltas) {
			i = len(f.deltas) - 1
		}
		f.calls++
		_ = json.NewEncoder(w).Encode(map[string]any{"changes": f.deltas[i], "next_cursor": "c", "has_more": false})
	})
	mux.HandleFunc("GET /api/v1/documents/{id}/content", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.downloads++
		c := f.content
		f.mu.Unlock()
		_, _ = w.Write([]byte(c))
	})
	mux.HandleFunc("POST /api/v1/storage/uploads/initiate", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.initiates++
		resp := map[string]any{"upload_id": "u1", "presigned_put_url": base() + "/put/u1"}
		for k, v := range f.initResp {
			resp[k] = v
		}
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("PUT /put/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.puts++
		f.mu.Unlock()
	})
	mux.HandleFunc("POST /api/v1/storage/uploads/{id}/complete", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.completes++
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"content_blob_id": "blob-new"})
	})
	mux.HandleFunc("POST /api/v1/documents/{id}/versions", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.versions++
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "v-pushed"})
	})
	return mux
}

func newFakeAgent(t *testing.T, f *fakeServer) (*agent, func()) {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(f.handler(func() string { return srv.URL }))
	root := t.TempDir()
	return &agent{client: newClient(srv.URL, "k"), root: root, ws: "ws", device: "dev", state: newState()}, srv.Close
}

// D-1: the sync delta no longer carries sha256. Change detection keys on
// version_id, so a document whose version has not moved is neither
// re-downloaded nor re-uploaded on later cycles.
func TestSync_UnchangedVersionNotReuploaded(t *testing.T) {
	doc := map[string]any{"kind": "document", "op": "upsert", "id": "d1", "name": "a.txt",
		"version_id": "v1", "updated_at": "2026-10-12T10:00:00Z"}
	f := &fakeServer{content: "hello", deltas: [][]map[string]any{
		{doc}, // cycle 1: new document
		{},    // cycle 2: nothing changed since the cursor
		{doc}, // cycle 3: same version replayed (e.g. metadata-only touch)
	}}
	a, done := newFakeAgent(t, f)
	defer done()

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := a.cycle(ctx); err != nil {
			t.Fatalf("cycle %d: %v", i+1, err)
		}
	}
	if f.initiates != 0 || f.versions != 0 {
		t.Fatalf("unchanged document was re-uploaded: initiates=%d versions=%d", f.initiates, f.versions)
	}
	if f.downloads != 1 {
		t.Fatalf("downloads = %d, want 1 (only the first cycle)", f.downloads)
	}
	b, err := os.ReadFile(filepath.Join(a.root, "a.txt"))
	if err != nil || string(b) != "hello" {
		t.Fatalf("mirror content = %q, %v", b, err)
	}
}

// D-1 companion: a new version_id (no sha256 in the delta) is still pulled,
// and a genuine local edit is still pushed exactly once.
func TestSync_NewVersionPulledAndLocalEditPushedOnce(t *testing.T) {
	v1 := map[string]any{"kind": "document", "op": "upsert", "id": "d1", "name": "a.txt", "version_id": "v1"}
	v2 := map[string]any{"kind": "document", "op": "upsert", "id": "d1", "name": "a.txt", "version_id": "v2"}
	f := &fakeServer{content: "one", deltas: [][]map[string]any{{v1}, {v2}, {}, {}}}
	a, done := newFakeAgent(t, f)
	defer done()
	ctx := context.Background()

	if err := a.cycle(ctx); err != nil {
		t.Fatal(err)
	}
	f.content = "two"
	if err := a.cycle(ctx); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(a.root, "a.txt")); string(b) != "two" {
		t.Fatalf("new version not pulled: %q", b)
	}
	if err := os.WriteFile(filepath.Join(a.root, "a.txt"), []byte("local edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := a.cycle(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if f.versions != 1 {
		t.Fatalf("local edit pushed %d times, want 1", f.versions)
	}
}

// D-1 companion: without a remote hash, a first sync over a mirror that
// already holds identical bytes must not litter "(conflicted copy)" files.
func TestSync_FirstSyncIdenticalLocalBytesNoConflictCopy(t *testing.T) {
	doc := map[string]any{"kind": "document", "op": "upsert", "id": "d1", "name": "a.txt", "version_id": "v1"}
	f := &fakeServer{content: "same", deltas: [][]map[string]any{{doc}, {}}}
	a, done := newFakeAgent(t, f)
	defer done()
	if err := os.WriteFile(filepath.Join(a.root, "a.txt"), []byte("same"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := a.cycle(ctx); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(a.root)
	for _, e := range entries {
		if e.Name() != "a.txt" && e.Name() != ".dms-sync-state.json" {
			t.Fatalf("unexpected file in mirror: %s", e.Name())
		}
	}
	if f.initiates != 0 {
		t.Fatalf("identical bytes were uploaded")
	}
}
