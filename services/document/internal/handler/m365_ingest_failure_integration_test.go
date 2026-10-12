//go:build integration

// D-25: when the storage pipeline is wired but the blob upload fails, the
// m365 ingest logged the error and still answered 201 — the add-in told the
// user "saved" while the documents were empty shells that the orphan GC
// then soft-deleted a day later. A failed upload now rolls back every
// document the request created and answers 502 — including the parent
// whose body upload had already succeeded when an attachment failed. When
// the rollback itself is refused (purge or delete not allowed), the 502
// says which documents remain and where, never "nothing was saved".
package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
	pkgstorage "github.com/aieera/sedoc/pkg/storage"
	"github.com/aieera/sedoc/pkg/testutil"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
	"github.com/aieera/sedoc/services/document/internal/repository"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// downStorage is a storage client whose upload pipeline is unavailable.
type downStorage struct{ sedocv1.StorageServiceClient }

func (downStorage) InitiateUpload(context.Context, *sedocv1.InitiateUploadRequest, ...grpc.CallOption) (*sedocv1.InitiateUploadResponse, error) {
	return nil, errors.New("storage unavailable")
}

// failFromCall uploads for real until call n, then the pipeline fails.
type failFromCall struct {
	*fakeStorageClient
	n     int
	mu    sync.Mutex
	calls int
}

func (f *failFromCall) InitiateUpload(ctx context.Context, in *sedocv1.InitiateUploadRequest, opts ...grpc.CallOption) (*sedocv1.InitiateUploadResponse, error) {
	f.mu.Lock()
	f.calls++
	c := f.calls
	f.mu.Unlock()
	if c >= f.n {
		return nil, errors.New("storage unavailable")
	}
	return f.fakeStorageClient.InitiateUpload(ctx, in, opts...)
}

// denyDelete allows everything except deleting a document.
type denyDelete struct{ allowPolicy }

func (denyDelete) CheckPermission(_ context.Context, in *sedocv1.CheckPermissionRequest, _ ...grpc.CallOption) (*sedocv1.CheckPermissionResponse, error) {
	return &sedocv1.CheckPermissionResponse{Allowed: in.GetAction() != "delete"}, nil
}

type m365Fixture struct {
	ctx    context.Context
	ictx   context.Context
	pool   *pgxpool.Pool
	s3     *pkgstorage.S3Client
	tenant uuid.UUID
	user   uuid.UUID
	wsID   uuid.UUID
	folder uuid.UUID
}

func newM365Fixture(t *testing.T) *m365Fixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	t.Cleanup(cancel)
	dsn, pgClean, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(pgClean)
	require.NoError(t, database.RunMigrations(dsn, "../../migrations"))
	cfg := database.DefaultPoolConfig()
	cfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	endpoint, access, secret, minioClean, err := testutil.NewMinIOContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(minioClean)
	s3c, err := pkgstorage.NewS3Client(endpoint, access, secret, false)
	require.NoError(t, err)
	require.NoError(t, s3c.CreateBucket(ctx, officeTestBucket, "us-east-1"))

	f := &m365Fixture{ctx: ctx, pool: pool, s3: s3c,
		tenant: uuid.Must(uuid.NewV7()), user: uuid.Must(uuid.NewV7()), wsID: uuid.Must(uuid.NewV7())}
	_, err = pool.Exec(ctx, `INSERT INTO organizations (id, name, slug, plan, primary_region)
		VALUES ($1, 'M365 Org', $2, 'standard', 'us-east-1')`, f.tenant, "m-"+f.tenant.String()[:8])
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO users (tenant_id, id, email, display_name, role, status)
		VALUES ($1, $2, $3, 'M365 User', 'admin', 'active')`, f.tenant, f.user, "u-"+f.user.String()[:8]+"@t.local")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO workspaces (tenant_id, id, name, region_pin, created_by)
		VALUES ($1, $2, 'ws', 'us-east-1', $3)`, f.tenant, f.wsID, f.user)
	require.NoError(t, err)
	f.ictx = auth.WithUser(auth.SetTenantID(ctx, f.tenant), auth.UserInfo{ID: f.user, TenantID: f.tenant, Role: "admin"})
	folder, err := f.svc(allowPolicy{}, true).CreateFolder(f.ictx, &service.CreateFolderInput{WorkspaceID: f.wsID, Name: "Inbox"})
	require.NoError(t, err)
	f.folder = folder.ID
	return f
}

// svc builds a document service; withS3 = false leaves purge unavailable.
func (f *m365Fixture) svc(policy sedocv1.PolicyServiceClient, withS3 bool) *service.DocumentService {
	s := service.New(f.pool, repository.New(f.pool), policy, zerolog.Nop())
	if withS3 {
		s.SetS3Client(f.s3)
	}
	return s
}

func (f *m365Fixture) ingest(t *testing.T, h *M365IngestHandler, includeBody bool) *httptest.ResponseRecorder {
	t.Helper()
	reqBody, _ := json.Marshal(map[string]any{
		"subject": "Q3 numbers", "from": "sender@partner.com", "to": []string{"me@corp.com"},
		"body_html":    "<p>See attached.</p>",
		"include_body": includeBody,
		"workspace_id": f.wsID.String(), "folder_id": f.folder.String(),
		"attachments": []map[string]string{
			{"name": "q3.pdf", "mime_type": "application/pdf", "content_b64": base64.StdEncoding.EncodeToString([]byte("pdf bytes " + uuid.NewString()))},
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/m365/ingest-email", bytes.NewReader(reqBody)).WithContext(f.ictx)
	rec := httptest.NewRecorder()
	h.ingestEmail(rec, req)
	return rec
}

func (f *m365Fixture) count(t *testing.T, where string) int {
	t.Helper()
	var n int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM documents WHERE tenant_id = $1 AND `+where, f.tenant).Scan(&n))
	return n
}

type rollbackResp struct {
	Error     string `json:"error"`
	Remaining []struct {
		DocumentID string `json:"document_id"`
		State      string `json:"state"`
	} `json:"remaining"`
}

func TestM365Ingest_UploadFailureNot201(t *testing.T) {
	f := newM365Fixture(t)

	// 1. Storage down: every created row is rolled back completely.
	h := &M365IngestHandler{pool: f.pool, svc: f.svc(allowPolicy{}, true), storage: downStorage{}, s3: f.s3, log: zerolog.Nop()}
	for name, includeBody := range map[string]bool{"with body": true, "attachments only": false} {
		rec := f.ingest(t, h, includeBody)
		require.Equal(t, http.StatusBadGateway, rec.Code, "%s: %s", name, rec.Body.String())
		var got rollbackResp
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		require.Contains(t, got.Error, "nothing was saved", name)
		require.Empty(t, got.Remaining, name)
		require.Zero(t, f.count(t, "true"), "%s: the rows created for this email are gone", name)
	}

	// 2. The body uploads (the parent gets a version), then the attachment
	// fails: the parent is rolled back too, version and all (DEC-8 purge).
	h.storage = &failFromCall{fakeStorageClient: &fakeStorageClient{pool: f.pool, s3: f.s3, pending: map[string]pendingUpload{}}, n: 2}
	rec := f.ingest(t, h, true)
	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "nothing was saved")
	require.Zero(t, f.count(t, "true"), "parent with its body version and the attachment are both purged")
	var versions int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM document_versions WHERE tenant_id = $1`, f.tenant).Scan(&versions))
	require.Zero(t, versions)
}

func TestM365Ingest_PartialRollbackReported(t *testing.T) {
	f := newM365Fixture(t)

	// Purge refused (unavailable here): the rows stay in the trash, and the
	// 502 names them instead of claiming nothing was saved.
	h := &M365IngestHandler{pool: f.pool, svc: f.svc(allowPolicy{}, false),
		storage: &failFromCall{fakeStorageClient: &fakeStorageClient{pool: f.pool, s3: f.s3, pending: map[string]pendingUpload{}}, n: 2},
		s3:      f.s3, log: zerolog.Nop()}
	rec := f.ingest(t, h, true)
	require.Equal(t, http.StatusBadGateway, rec.Code)
	var got rollbackResp
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.NotContains(t, got.Error, "nothing was saved")
	require.Contains(t, got.Error, "partial rollback: 2 documents remain")
	require.Len(t, got.Remaining, 2)
	for _, r := range got.Remaining {
		require.Equal(t, "trash", r.State)
		require.Equal(t, 1, f.count(t, "deleted_at IS NOT NULL AND id = '"+uuid.MustParse(r.DocumentID).String()+"'"))
	}

	// Delete refused: the rows stay live, and the 502 says so.
	h.svc = f.svc(denyDelete{}, true)
	h.storage = downStorage{}
	rec = f.ingest(t, h, true)
	require.Equal(t, http.StatusBadGateway, rec.Code)
	got = rollbackResp{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Contains(t, got.Error, "partial rollback: 1 documents remain")
	require.Len(t, got.Remaining, 1)
	require.Equal(t, "live", got.Remaining[0].State)
}

