package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// fakeIngestClient is the in-process double the test uses. Records
// what it was asked to do so assertions can verify call shape +
// arg propagation without standing up real gRPC servers.
type fakeIngestClient struct {
	putCalls           int32
	createVersionCalls int32
	lastPut            PutSignedBlobInput
	lastCreate         CreateVersionFromBlobInput
	putErr             error
	createErr          error
}

func (f *fakeIngestClient) PutSignedBlob(_ context.Context, in PutSignedBlobInput) (*PutSignedBlobResult, error) {
	atomic.AddInt32(&f.putCalls, 1)
	f.lastPut = in
	if f.putErr != nil {
		return nil, f.putErr
	}
	return &PutSignedBlobResult{
		ContentBlobID: "blob-" + hashBytes(in.Bytes)[:8],
		SHA256:        hashBytes(in.Bytes),
		SizeBytes:     int64(len(in.Bytes)),
	}, nil
}

func (f *fakeIngestClient) CreateVersionFromBlob(_ context.Context, in CreateVersionFromBlobInput) (string, error) {
	atomic.AddInt32(&f.createVersionCalls, 1)
	f.lastCreate = in
	if f.createErr != nil {
		return "", f.createErr
	}
	return "version-1", nil
}

func TestIngestPipeline_PutAndCreateVersion(t *testing.T) {
	fake := &fakeIngestClient{}
	p := &ingestPipeline{client: fake}
	versionID, blobID, err := p.PutAndCreateVersion(context.Background(),
		PutSignedBlobInput{
			TenantID: "t-1", UserID: "u-1",
			Filename: "signed.pdf", MimeType: "application/pdf",
			Bytes: []byte("%PDF-fake-signed-bytes"),
			RegionPin: "us-east-1",
		},
		"doc-7", "u-1", "signed via vendor X")
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	if versionID != "version-1" {
		t.Fatalf("version_id = %q", versionID)
	}
	if blobID == "" {
		t.Fatal("blob_id empty")
	}
	// PutSignedBlob received the right tenant + bytes.
	if fake.lastPut.TenantID != "t-1" {
		t.Fatalf("tenant on put = %q", fake.lastPut.TenantID)
	}
	if string(fake.lastPut.Bytes) != "%PDF-fake-signed-bytes" {
		t.Fatalf("bytes on put corrupted")
	}
	// CreateVersionFromBlob received the resolved blob id + change summary.
	if fake.lastCreate.DocumentID != "doc-7" {
		t.Fatalf("doc on create = %q", fake.lastCreate.DocumentID)
	}
	if fake.lastCreate.ContentBlobID != blobID {
		t.Fatalf("blob_id mismatch: create=%q put-resolved=%q", fake.lastCreate.ContentBlobID, blobID)
	}
	if fake.lastCreate.ChangeSummary != "signed via vendor X" {
		t.Fatalf("change summary = %q", fake.lastCreate.ChangeSummary)
	}
}

func TestIngestPipeline_PutFailsAborts(t *testing.T) {
	// When PutSignedBlob errors, we never reach CreateVersionFromBlob.
	// Important: half-uploaded bytes must NOT result in a version row
	// pointing at a blob that doesn't exist.
	fake := &fakeIngestClient{putErr: errors.New("storage 503")}
	p := &ingestPipeline{client: fake}
	_, _, err := p.PutAndCreateVersion(context.Background(),
		PutSignedBlobInput{TenantID: "t-1", Bytes: []byte("x")},
		"doc-7", "u-1", "summary")
	if err == nil {
		t.Fatal("expected error")
	}
	if fake.createVersionCalls != 0 {
		t.Fatalf("create_version was called %d times after put failed; expected 0", fake.createVersionCalls)
	}
}

func TestIngestPipeline_CreateVersionFails(t *testing.T) {
	fake := &fakeIngestClient{createErr: errors.New("doc service 503")}
	p := &ingestPipeline{client: fake}
	_, _, err := p.PutAndCreateVersion(context.Background(),
		PutSignedBlobInput{TenantID: "t-1", Bytes: []byte("x")},
		"doc-7", "u-1", "summary")
	if err == nil {
		t.Fatal("expected error")
	}
	// Put still ran — that's fine. The orphaned blob is reachable
	// via the next ingest attempt (idempotency on SHA-256), not a
	// permanent leak.
	if fake.putCalls != 1 {
		t.Fatalf("put_calls = %d", fake.putCalls)
	}
}

func TestIngestPipeline_NoClientErrors(t *testing.T) {
	p := &ingestPipeline{}
	_, _, err := p.PutAndCreateVersion(context.Background(),
		PutSignedBlobInput{Bytes: []byte("x")}, "doc-7", "u-1", "")
	if err == nil {
		t.Fatal("expected ingest-not-configured error")
	}
}

func TestIngestPipeline_EmptyBytesRejected(t *testing.T) {
	fake := &fakeIngestClient{}
	p := &ingestPipeline{client: fake}
	_, _, err := p.PutAndCreateVersion(context.Background(),
		PutSignedBlobInput{TenantID: "t-1", Bytes: nil}, "doc-7", "u-1", "")
	if err == nil {
		t.Fatal("expected error for empty bytes")
	}
	if fake.putCalls != 0 {
		t.Fatal("must not call upstream when bytes empty")
	}
}

// ----- httpPutWithRetry -------------------------------------------

func TestHTTPPutWithRetry_HappyPath(t *testing.T) {
	calls := int32(0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.Method != "PUT" {
			t.Errorf("method = %q", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if err := httpPutWithRetry(context.Background(), srv.Client(), srv.URL, []byte("x"), nil); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestHTTPPutWithRetry_RetriesOnce(t *testing.T) {
	// First call fails (server closes); second succeeds. Verifies
	// the retry loop covers transient transport errors.
	calls := int32(0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			// Hijack to force a connection error on the first call.
			hj, ok := w.(http.Hijacker)
			if !ok {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if err := httpPutWithRetry(context.Background(), srv.Client(), srv.URL, []byte("x"), nil); err != nil {
		t.Fatalf("expected retry to succeed: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestHTTPPutWithRetry_5xxNotRetried(t *testing.T) {
	// 5xx is treated as a permanent failure here (S3 doesn't
	// typically issue them on PUT once the presigned URL is valid).
	calls := int32(0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	err := httpPutWithRetry(context.Background(), srv.Client(), srv.URL, []byte("x"), nil)
	if err == nil {
		t.Fatal("expected error on 500")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (no retry on 5xx)", calls)
	}
}

func TestHashBytes_Stable(t *testing.T) {
	a := hashBytes([]byte("hello"))
	b := hashBytes([]byte("hello"))
	if a != b {
		t.Fatalf("hash not stable: %q vs %q", a, b)
	}
	if len(a) != 64 {
		t.Fatalf("hash len = %d, want 64 (sha256 hex)", len(a))
	}
	c := hashBytes([]byte("world"))
	if a == c {
		t.Fatal("collision on different input")
	}
}
