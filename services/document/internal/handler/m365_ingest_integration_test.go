//go:build integration

// Proves the ADR 0112 completion: the m365 (Outlook) ingest endpoint now
// PERSISTS the email body + each attachment as real content blobs + document
// versions — not just metadata rows. Drives the endpoint end-to-end against a
// real Postgres + real MinIO, with the storage service stood in by a fake gRPC
// client that mirrors the real InitiateUpload → S3 put → CompleteUpload flow
// (CompleteUpload writes the content_blobs row after verifying the object
// landed). Self-contained (own fakes) so it doesn't couple to other tests.
//
// Run: go test -tags integration -run TestM365Ingest ./internal/handler/
package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
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
	"google.golang.org/grpc/metadata"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
	pkgstorage "github.com/aieera/sedoc/pkg/storage"
	"github.com/aieera/sedoc/pkg/testutil"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
	"github.com/aieera/sedoc/services/document/internal/repository"
	"github.com/aieera/sedoc/services/document/internal/service"
)

const m365TestBucket = "sedoc-m365-test"

// m365AllowPolicy permits every permission check (the ingest actor is the
// admin who created the docs).
type m365AllowPolicy struct{}

func (m365AllowPolicy) CheckPermission(context.Context, *sedocv1.CheckPermissionRequest, ...grpc.CallOption) (*sedocv1.CheckPermissionResponse, error) {
	return &sedocv1.CheckPermissionResponse{Allowed: true}, nil
}
func (m365AllowPolicy) BatchCheckPermission(_ context.Context, in *sedocv1.BatchCheckPermissionRequest, _ ...grpc.CallOption) (*sedocv1.BatchCheckPermissionResponse, error) {
	res := &sedocv1.BatchCheckPermissionResponse{}
	for range in.GetChecks() {
		res.Results = append(res.Results, &sedocv1.CheckPermissionResponse{Allowed: true})
	}
	return res, nil
}

type m365PendingUpload struct {
	tenant uuid.UUID
	bucket string
	key    string
	mime   string
}

// m365FakeStorage stands in for the storage gRPC service: InitiateUpload hands
// out real coordinates; CompleteUpload verifies the object exists in MinIO and
// writes the content_blobs row (envelope encryption is out of scope for the
// fake — the real service handles it). Only these two methods are called.
type m365FakeStorage struct {
	sedocv1.StorageServiceClient // embedded; other methods unused (nil)
	pool                         *pgxpool.Pool
	s3                           *pkgstorage.S3Client
	mu                           sync.Mutex
	pending                      map[string]m365PendingUpload
}

func (f *m365FakeStorage) InitiateUpload(ctx context.Context, in *sedocv1.InitiateUploadRequest, _ ...grpc.CallOption) (*sedocv1.InitiateUploadResponse, error) {
	tenant := m365TenantFromMD(ctx)
	if tenant == uuid.Nil {
		return nil, fmt.Errorf("no x-tenant-id metadata")
	}
	uploadID := uuid.New()
	key := fmt.Sprintf("%s/%s/%s", tenant, uploadID, in.GetFilename())
	f.mu.Lock()
	f.pending[uploadID.String()] = m365PendingUpload{tenant: tenant, bucket: m365TestBucket, key: key, mime: in.GetMimeType()}
	f.mu.Unlock()
	return &sedocv1.InitiateUploadResponse{UploadId: uploadID.String(), StorageBucket: m365TestBucket, StorageKey: key}, nil
}

func (f *m365FakeStorage) CompleteUpload(ctx context.Context, in *sedocv1.CompleteUploadRequest, _ ...grpc.CallOption) (*sedocv1.CompleteUploadResponse, error) {
	f.mu.Lock()
	p, ok := f.pending[in.GetUploadId()]
	delete(f.pending, in.GetUploadId())
	f.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("unknown upload id %s", in.GetUploadId())
	}
	info, err := f.s3.GetObjectInfo(ctx, p.bucket, p.key)
	if err != nil {
		return nil, fmt.Errorf("object not at %s/%s: %w", p.bucket, p.key, err)
	}
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO content_blobs (id, tenant_id, sha256_hash, storage_region, storage_bucket, storage_key, size_bytes, mime_type)
		VALUES ($1, $2, $3, 'us-east-1', $4, $5, $6, $7)
		ON CONFLICT (tenant_id, sha256_hash) DO NOTHING`,
		uuid.New(), p.tenant, in.GetChecksumSha256(), p.bucket, p.key, info.Size, p.mime); err != nil {
		return nil, fmt.Errorf("insert content_blob: %w", err)
	}
	return &sedocv1.CompleteUploadResponse{StorageBucket: p.bucket, StorageKey: p.key, SizeBytes: info.Size, ChecksumSha256: in.GetChecksumSha256()}, nil
}

func m365TenantFromMD(ctx context.Context) uuid.UUID {
	md, _ := metadata.FromOutgoingContext(ctx)
	if vs := md.Get("x-tenant-id"); len(vs) == 1 {
		if id, err := uuid.Parse(vs[0]); err == nil {
			return id
		}
	}
	return uuid.Nil
}

func TestM365Ingest_PersistsBlobsAndVersions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	t.Cleanup(cancel)

	dsn, pgClean, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(pgClean)
	require.NoError(t, database.RunMigrations(dsn, "../../migrations"))
	poolCfg := database.DefaultPoolConfig()
	poolCfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, poolCfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	endpoint, access, secret, minioClean, err := testutil.NewMinIOContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(minioClean)
	s3c, err := pkgstorage.NewS3Client(endpoint, access, secret, false)
	require.NoError(t, err)
	require.NoError(t, s3c.CreateBucket(ctx, m365TestBucket, "us-east-1"))

	repos := repository.New(pool)
	svc := service.New(pool, repos, m365AllowPolicy{}, zerolog.Nop())

	tenant := uuid.Must(uuid.NewV7())
	user := uuid.Must(uuid.NewV7())
	_, err = pool.Exec(ctx, `INSERT INTO organizations (id, name, slug, plan, primary_region)
		VALUES ($1, 'M365 Org', $2, 'standard', 'us-east-1')`, tenant, "m-"+tenant.String()[:8])
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO users (tenant_id, id, email, display_name, role, status)
		VALUES ($1, $2, $3, 'M365 User', 'admin', 'active')`, tenant, user, "u-"+user.String()[:8]+"@t.local")
	require.NoError(t, err)

	ictx := auth.WithUser(auth.SetTenantID(ctx, tenant), auth.UserInfo{ID: user, TenantID: tenant, Role: "admin"})
	wsID := uuid.Must(uuid.NewV7())
	_, err = pool.Exec(ctx, `INSERT INTO workspaces (tenant_id, id, name, region_pin, created_by)
		VALUES ($1, $2, 'ws', 'us-east-1', $3)`, tenant, wsID, user)
	require.NoError(t, err)
	folder, err := svc.CreateFolder(ictx, &service.CreateFolderInput{WorkspaceID: wsID, Name: "Root"})
	require.NoError(t, err)

	fake := &m365FakeStorage{pool: pool, s3: s3c, pending: map[string]m365PendingUpload{}}
	h := NewM365IngestHandler(pool, svc, fake, s3c, zerolog.Nop())

	attachment := []byte("PDF-ish attachment bytes for the m365 ingest test")
	reqBody, _ := json.Marshal(map[string]any{
		"subject":      "Q3 numbers",
		"from":         "sender@partner.com",
		"to":           []string{"me@corp.com"},
		"body_html":    "<html><body><h1>Q3</h1><p>See attached.</p></body></html>",
		"workspace_id": wsID.String(),
		"folder_id":    folder.ID.String(),
		"attachments": []map[string]string{
			{"name": "q3.pdf", "mime_type": "application/pdf", "content_b64": base64.StdEncoding.EncodeToString(attachment)},
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/m365/ingest-email", bytes.NewReader(reqBody)).WithContext(ictx)
	rec := httptest.NewRecorder()
	h.ingestEmail(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var resp m365IngestResp
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.DocumentID, "parent email document created")
	require.Len(t, resp.AttachmentDocumentIDs, 1, "one attachment document")
	require.False(t, resp.Pending, "storage wired → blobs persisted inline, not pending")

	parentID := uuid.MustParse(resp.DocumentID)
	require.Equal(t, 1, m365VersionCount(t, ctx, pool, tenant, parentID), "email body persisted as a version")
	childID := uuid.MustParse(resp.AttachmentDocumentIDs[0])
	require.Equal(t, 1, m365VersionCount(t, ctx, pool, tenant, childID), "attachment persisted as a version")

	var blobCount int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM content_blobs WHERE tenant_id = $1`, tenant).Scan(&blobCount))
	require.Equal(t, 2, blobCount, "email body + attachment stored as content blobs")

	// The attachment version's blob really landed in object storage.
	var bucket, key string
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT b.storage_bucket, b.storage_key
		FROM document_versions v JOIN content_blobs b ON b.id = v.content_blob_id
		WHERE v.tenant_id = $1 AND v.document_id = $2`, tenant, childID).Scan(&bucket, &key))
	info, gerr := s3c.GetObjectInfo(ctx, bucket, key)
	require.NoError(t, gerr, "attachment object must exist in object storage")
	require.Equal(t, int64(len(attachment)), info.Size)
}

func m365VersionCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenant, docID uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM document_versions WHERE tenant_id=$1 AND document_id=$2`, tenant, docID).Scan(&n))
	return n
}
