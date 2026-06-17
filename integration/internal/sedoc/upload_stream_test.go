package sedoc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// zeroReader yields an endless stream of zero bytes without a backing buffer, so
// a large upload can be driven without allocating the payload.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

func shaOf(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func shaOfStream(r io.Reader) string {
	h := sha256.New()
	_, _ = io.Copy(h, r)
	return hex.EncodeToString(h.Sum(nil))
}

// uploadFake models SeDoc's 3-step upload surface and records how the PUT was
// transferred (fixed-length vs chunked, byte count) so streaming can be asserted.
type uploadFake struct {
	mu          sync.Mutex
	dedup       bool // when true, initiate returns no presigned URL (blob already exists)
	initSHA     string
	initSize    int64
	putHit      bool
	putBytes    int64
	putCL       int64
	putChunked  bool
	completeHit bool
}

func (f *uploadFake) server(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/storage/uploads/initiate", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			SHA256 string `json:"sha256_hash"`
			Size   int64  `json:"size_bytes"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		f.initSHA, f.initSize = b.SHA256, b.Size
		dedup := f.dedup
		f.mu.Unlock()
		out := map[string]any{"upload_id": "up"}
		if !dedup {
			out["presigned_put_url"] = "http://" + r.Host + "/put"
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("PUT /put", func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body) // stream-discard; never buffer server-side
		f.mu.Lock()
		f.putHit = true
		f.putBytes = n
		f.putCL = r.ContentLength
		f.putChunked = len(r.TransferEncoding) > 0 && r.TransferEncoding[0] == "chunked"
		f.mu.Unlock()
		w.WriteHeader(200)
	})
	mux.HandleFunc("POST /api/v1/storage/uploads/{id}/complete", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			SHA256 string `json:"sha256_hash"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		f.completeHit = true
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"content_blob_id": "blob-1", "checksum_sha256": b.SHA256})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL + "/api/v1"
}

// TestUploadStreamFlatMemoryForLargeFile uploads 256 MB (well past the old 64 MB
// cap) and asserts it streams with flat memory: a fixed Content-Length PUT (not
// chunked), the full byte count delivered, and total heap allocation far below
// the payload size (the old io.ReadAll path would allocate 256 MB+).
func TestUploadStreamFlatMemoryForLargeFile(t *testing.T) {
	const size = int64(256 << 20) // 256 MB
	sha := shaOfStream(io.LimitReader(zeroReader{}, size))

	f := &uploadFake{}
	c := New(f.server(t), "vdms_test")

	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)

	res, err := c.UploadStream(context.Background(), "job", "us-east-1", "big.bin",
		"application/octet-stream", io.LimitReader(zeroReader{}, size), size, sha)
	if err != nil {
		t.Fatalf("UploadStream: %v", err)
	}

	runtime.ReadMemStats(&m1)
	allocated := int64(m1.TotalAlloc - m0.TotalAlloc)
	if allocated > size/2 {
		t.Fatalf("streaming a %d-byte upload allocated %d bytes — looks buffered, not streamed", size, allocated)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.putHit {
		t.Fatal("PUT not called")
	}
	if f.putBytes != size {
		t.Fatalf("PUT received %d bytes, want %d", f.putBytes, size)
	}
	if f.putCL != size {
		t.Fatalf("PUT Content-Length = %d, want %d (fixed-length stream)", f.putCL, size)
	}
	if f.putChunked {
		t.Fatal("PUT used chunked transfer encoding; S3 requires a fixed Content-Length")
	}
	if !f.completeHit {
		t.Fatal("complete not called")
	}
	if res.ContentBlobID != "blob-1" || res.SHA256 != sha || res.SizeBytes != size {
		t.Fatalf("unexpected result: %+v", res)
	}
}

// TestUploadStreamDedupSkipsPut: when initiate reports the blob already exists
// (no presigned URL), there is no upload session — the PUT and complete are both
// skipped, the body is drained, and a checksum-only result is returned. The
// :upsert/ingest that follows resolves the existing content blob by sha256.
func TestUploadStreamDedupSkipsPut(t *testing.T) {
	body := []byte("hello world")
	f := &uploadFake{dedup: true}
	c := New(f.server(t), "vdms_test")

	res, err := c.UploadStream(context.Background(), "job", "us-east-1", "f.txt",
		"text/plain", strings.NewReader(string(body)), int64(len(body)), shaOf(body))
	if err != nil {
		t.Fatalf("UploadStream: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.putHit {
		t.Fatal("dedup hit must skip the PUT")
	}
	if f.completeHit {
		t.Fatal("dedup hit has no upload session — complete must be skipped")
	}
	if res.ContentBlobID != "" {
		t.Fatalf("dedup result should carry no content_blob_id, got %q", res.ContentBlobID)
	}
	if res.SHA256 != shaOf(body) || res.SizeBytes != int64(len(body)) {
		t.Fatalf("dedup result should carry checksum+size, got %+v", res)
	}
}

// TestUploadStreamSizeMismatch: a short body (declared > actual) is caught by the
// fixed-length PUT; a long body (declared < actual) is caught by the trailing-byte
// check. Either way no document is completed.
func TestUploadStreamSizeMismatch(t *testing.T) {
	t.Run("short body", func(t *testing.T) {
		f := &uploadFake{}
		c := New(f.server(t), "vdms_test")
		// Declare 100 bytes but supply 11 → net/http fails the fixed-length PUT.
		_, err := c.UploadStream(context.Background(), "job", "r", "f", "application/octet-stream",
			strings.NewReader("hello world"), 100, shaOf([]byte("hello world")))
		if err == nil {
			t.Fatal("expected error for under-length body")
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.completeHit {
			t.Fatal("complete must not run when the PUT fails")
		}
	})
	t.Run("long body", func(t *testing.T) {
		f := &uploadFake{}
		c := New(f.server(t), "vdms_test")
		// Declare 5 bytes but supply 11 → net/http's fixed-length writer detects the
		// over-long body and fails the PUT (our trailing-byte check is a backstop).
		_, err := c.UploadStream(context.Background(), "job", "r", "f", "application/octet-stream",
			strings.NewReader("hello world"), 5, shaOf([]byte("hello")))
		if err == nil {
			t.Fatal("expected error for over-length body")
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.completeHit {
			t.Fatal("complete must not run on an over-length source")
		}
	})
}

// TestUploadStreamFallbackSpools: with no client hash/size, the body is spooled to
// a temp file, hashed, and streamed — initiate gets the computed sha + length.
func TestUploadStreamFallbackSpools(t *testing.T) {
	data := []byte("spool me to disk, hash me, then stream me")
	f := &uploadFake{}
	c := New(f.server(t), "vdms_test")

	res, err := c.UploadStream(context.Background(), "job", "us-east-1", "f.bin",
		"application/octet-stream", strings.NewReader(string(data)), -1, "") // no size, no sha
	if err != nil {
		t.Fatalf("UploadStream: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.initSHA != shaOf(data) {
		t.Fatalf("initiate sha = %q, want %q (computed by spooling)", f.initSHA, shaOf(data))
	}
	if f.initSize != int64(len(data)) {
		t.Fatalf("initiate size = %d, want %d", f.initSize, len(data))
	}
	if f.putBytes != int64(len(data)) {
		t.Fatalf("PUT received %d bytes, want %d", f.putBytes, len(data))
	}
	if res.SHA256 != shaOf(data) {
		t.Fatalf("result sha = %q", res.SHA256)
	}
}
