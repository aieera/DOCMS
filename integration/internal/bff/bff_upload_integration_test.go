//go:build integration
// +build integration

// Streaming-upload acceptance (Prompt 3): the BFF reads the multipart file part
// as a stream (no ParseMultipartForm, no size cap), forwards the client sha256 +
// size so initiate can dedup, streams the bytes to the presigned PUT, and fires
// /ingest afterward. A dedup hit skips the PUT but still ingests.
//
// Run with: go test -tags integration ./integration/internal/bff/...
package bff

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
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

// uploadMockDoc models SeDoc's upload + ingest surface and records how the PUT
// was transferred.
type uploadMockDoc struct {
	mu          sync.Mutex
	dedup       bool
	putHit      bool
	putBytes    int64
	putCL       int64
	completeHit bool
	ingestHit   bool
}

func (m *uploadMockDoc) server(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/storage/uploads/initiate", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		dedup := m.dedup
		m.mu.Unlock()
		out := map[string]any{"upload_id": "up"}
		if !dedup {
			out["presigned_put_url"] = "http://" + r.Host + "/put"
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("PUT /put", func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		m.mu.Lock()
		m.putHit, m.putBytes, m.putCL = true, n, r.ContentLength
		m.mu.Unlock()
		w.WriteHeader(200)
	})
	mux.HandleFunc("POST /api/v1/storage/uploads/{id}/complete", func(w http.ResponseWriter, _ *http.Request) {
		m.mu.Lock()
		m.completeHit = true
		m.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"content_blob_id": "blob-x", "checksum_sha256": "sha"})
	})
	mux.HandleFunc("POST /api/v1/ingest", func(w http.ResponseWriter, _ *http.Request) {
		m.mu.Lock()
		m.ingestHit = true
		m.mu.Unlock()
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]any{"ingestion_item_id": "ing-1", "status": "received"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL + "/api/v1"
}

func newUploadBFF(t *testing.T, dedup bool) (*httptest.Server, *uploadMockDoc) {
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
	require.NoError(t, st.SaveCustomer(ctx, &store.CustomerMapping{
		CustomerRef: "CUST-1", Name: "Acme", BucketFolderID: "b1", MainFolderID: "main-1",
		SubfolderIDs: map[string]string{"attachments": "sub-att"},
	}))

	md := &uploadMockDoc{dedup: dedup}
	doc := sedoc.New(md.server(t), "vdms_secret_key")
	b := New(st, doc, stubAuthz{allow: map[string]bool{"alice": true}}, "ws-1", zerolog.Nop())
	mux := http.NewServeMux()
	b.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, md
}

// uploadMultipart builds a sha256→size→file ordered multipart body and POSTs it.
func uploadMultipart(t *testing.T, srv *httptest.Server, data []byte) (*http.Response, []byte) {
	t.Helper()
	sum := sha256.Sum256(data)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	require.NoError(t, mw.WriteField("sha256", hex.EncodeToString(sum[:])))
	require.NoError(t, mw.WriteField("size", strconv.Itoa(len(data))))
	fw, err := mw.CreateFormFile("file", "attach.pdf")
	require.NoError(t, err)
	_, err = fw.Write(data)
	require.NoError(t, err)
	require.NoError(t, mw.Close())

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/files/customers/CUST-1/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-ERP-User", "alice")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

func TestBFF_UploadStreamsThenIngests(t *testing.T) {
	srv, md := newUploadBFF(t, false)
	data := bytes.Repeat([]byte("a"), 1<<20) // 1 MiB

	resp, body := uploadMultipart(t, srv, data)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body=%s", body)
	require.Contains(t, string(body), "ing-1")

	md.mu.Lock()
	defer md.mu.Unlock()
	require.True(t, md.putHit, "PUT was streamed")
	require.Equal(t, int64(len(data)), md.putBytes, "full payload reached the presigned PUT")
	require.Equal(t, int64(len(data)), md.putCL, "fixed Content-Length (not chunked)")
	require.True(t, md.completeHit)
	require.True(t, md.ingestHit, "/ingest fired after upload")
}

func TestBFF_UploadDedupSkipsPutButIngests(t *testing.T) {
	srv, md := newUploadBFF(t, true) // initiate reports the blob already exists
	resp, body := uploadMultipart(t, srv, bytes.Repeat([]byte("b"), 4096))
	require.Equal(t, http.StatusOK, resp.StatusCode, "body=%s", body)

	md.mu.Lock()
	defer md.mu.Unlock()
	require.False(t, md.putHit, "dedup hit must skip the PUT")
	require.True(t, md.completeHit, "complete still runs")
	require.True(t, md.ingestHit, "/ingest still fires on dedup")
}
