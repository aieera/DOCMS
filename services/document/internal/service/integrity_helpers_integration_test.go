//go:build integration

// Shared fixtures for the document data-integrity suite (review remediation
// workstream DOC1): a recording fake S3 endpoint, blobs seeded the way the
// storage service leaves them after a completed upload (content_blobs row +
// the completed upload_sessions row that produced it), and policy stubs that
// deny selected resources or park one call so a concurrent writer can run in
// the permission-check window.
package service_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/aieera/sedoc/pkg/storage"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// ---- recording S3 ----------------------------------------------------------

// recordingS3 is an httptest S3 endpoint that accepts every request and
// records each DeleteObject as "bucket/key" (minio-go uses path-style
// addressing against an IP endpoint).
type recordingS3 struct {
	srv     *httptest.Server
	mu      sync.Mutex
	deleted []string
}

func newRecordingS3(t *testing.T) *recordingS3 {
	t.Helper()
	f := &recordingS3{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			f.mu.Lock()
			f.deleted = append(f.deleted, strings.TrimPrefix(r.URL.Path, "/"))
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// install wires the recording endpoint as the service's purge S3 client.
func (f *recordingS3) install(t *testing.T, svc *service.DocumentService) {
	t.Helper()
	c, err := storage.NewS3Client(strings.TrimPrefix(f.srv.URL, "http://"), "test", "test", false)
	require.NoError(t, err)
	svc.SetS3Client(c)
}

func (f *recordingS3) deletedObjects() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deleted...)
}

// ---- seeding ---------------------------------------------------------------

type seededBlob struct {
	ID     uuid.UUID
	SHA    string
	Bucket string
	Key    string
}

// Object is the "bucket/key" string recordingS3 reports for this blob.
func (b seededBlob) Object() string { return b.Bucket + "/" + b.Key }

// seedUploadedBlob models a finished storage upload by uploader in region: the
// content_blobs row plus the completed upload_sessions row whose
// content_blob_id points at it (the session id is the upload id embedded in
// the storage key, as the storage service lays it out).
func seedUploadedBlob(t *testing.T, h *harness, uploader uuid.UUID, region string) seededBlob {
	t.Helper()
	sessionID := uuid.New()
	b := seededBlob{ID: uuid.New(), Bucket: "dms-" + region + "-hot"}
	sum := sha256.Sum256([]byte(b.ID.String()))
	b.SHA = hex.EncodeToString(sum[:])
	b.Key = fmt.Sprintf("%s/2026/10/%s/file.pdf", h.tenant, sessionID)
	_, err := h.pool.Exec(h.ctx, `
		INSERT INTO content_blobs (id, tenant_id, sha256_hash, storage_region,
		                           storage_bucket, storage_key, size_bytes, mime_type)
		VALUES ($1, $2, $3, $4, $5, $6, 1024, 'application/pdf')`,
		b.ID, h.tenant, b.SHA, region, b.Bucket, b.Key)
	require.NoError(t, err)
	_, err = h.pool.Exec(h.ctx, `
		INSERT INTO upload_sessions (tenant_id, id, filename, total_size, mime_type,
		                             upload_type, storage_region, status, created_by,
		                             completed_at, expires_at, content_blob_id)
		VALUES ($1, $2, 'file.pdf', 1024, 'application/pdf', 'single', $3,
		        'completed', $4, now(), now() + interval '1 hour', $5)`,
		h.tenant, sessionID, region, uploader, b.ID)
	require.NoError(t, err)
	return b
}

// seedOtherUser adds a second active member to the harness tenant.
func seedOtherUser(t *testing.T, h *harness) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	_, err := h.pool.Exec(h.ctx, `
		INSERT INTO users (tenant_id, id, email, display_name, role, status)
		VALUES ($1, $2, $3, 'Other User', 'member', 'active')`,
		h.tenant, id, "o-"+id.String()[:8]+"@test.local")
	require.NoError(t, err)
	return id
}

// mustDocIn creates a draft document in folder.
func mustDocIn(t *testing.T, h *harness, svc *service.DocumentService, wsID, folderID uuid.UUID, title string) *model.Document {
	t.Helper()
	doc, err := svc.CreateDocument(h.ctx, &service.CreateDocumentInput{
		UpdatedBy: h.user, WorkspaceID: wsID, FolderID: folderID, Title: title,
	})
	require.NoError(t, err)
	return doc
}

func mustLinkVersion(t *testing.T, h *harness, svc *service.DocumentService, docID uuid.UUID, b seededBlob) *model.Version {
	t.Helper()
	v, err := svc.CreateVersion(h.ctx, &service.CreateVersionInput{DocumentID: docID, ContentBlobID: b.ID})
	require.NoError(t, err)
	return v
}

func blobRowExists(t *testing.T, h *harness, id uuid.UUID) bool {
	t.Helper()
	return countRows(t, h, `SELECT count(*) FROM content_blobs WHERE tenant_id = $1 AND id = $2`, h.tenant, id) == 1
}

func blobRefCount(t *testing.T, h *harness, id uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, h.pool.QueryRow(h.ctx,
		`SELECT reference_count FROM content_blobs WHERE tenant_id = $1 AND id = $2`, h.tenant, id).Scan(&n))
	return n
}

// waitForLockWaiter polls until some backend is blocked on a lock (the
// concurrent writer under test reached the row lock) or the timeout passes.
func waitForLockWaiter(t *testing.T, h *harness, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var n int
		require.NoError(t, h.pool.QueryRow(context.Background(), `
			SELECT count(*) FROM pg_stat_activity
			 WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&n))
		if n > 0 {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
}

// ---- policy stubs ----------------------------------------------------------

// denyPolicy allows everything except any check whose resource id is in deny.
type denyPolicy struct{ deny map[string]bool }

func (p denyPolicy) CheckPermission(_ context.Context, in *sedocv1.CheckPermissionRequest, _ ...grpc.CallOption) (*sedocv1.CheckPermissionResponse, error) {
	return &sedocv1.CheckPermissionResponse{Allowed: !p.deny[in.GetResourceId()]}, nil
}

func (p denyPolicy) BatchCheckPermission(_ context.Context, in *sedocv1.BatchCheckPermissionRequest, _ ...grpc.CallOption) (*sedocv1.BatchCheckPermissionResponse, error) {
	out := &sedocv1.BatchCheckPermissionResponse{}
	for _, c := range in.GetChecks() {
		out.Results = append(out.Results, &sedocv1.CheckPermissionResponse{Allowed: !p.deny[c.GetResourceId()]})
	}
	return out, nil
}

// gatedPolicy allows everything; once armed, the NEXT CheckPermission call
// signals entered and blocks until release is closed — parking that caller in
// its permission-check window while the test runs a concurrent writer.
type gatedPolicy struct {
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func newGatedPolicy() *gatedPolicy {
	return &gatedPolicy{entered: make(chan struct{}), release: make(chan struct{})}
}

func (g *gatedPolicy) CheckPermission(_ context.Context, _ *sedocv1.CheckPermissionRequest, _ ...grpc.CallOption) (*sedocv1.CheckPermissionResponse, error) {
	if g.armed.CompareAndSwap(true, false) {
		close(g.entered)
		<-g.release
	}
	return &sedocv1.CheckPermissionResponse{Allowed: true}, nil
}

func (g *gatedPolicy) BatchCheckPermission(_ context.Context, in *sedocv1.BatchCheckPermissionRequest, _ ...grpc.CallOption) (*sedocv1.BatchCheckPermissionResponse, error) {
	out := &sedocv1.BatchCheckPermissionResponse{}
	for range in.GetChecks() {
		out.Results = append(out.Results, &sedocv1.CheckPermissionResponse{Allowed: true})
	}
	return out, nil
}

// serviceWith builds a second DocumentService on the harness DB with a
// different policy client.
func serviceWith(h *harness, policy service.PermissionChecker) *service.DocumentService {
	return service.New(h.pool, h.repos, policy, zerolog.Nop())
}
