//go:build integration

// Proves the ADR 0112 completion: the m365 (Outlook) ingest endpoint now
// PERSISTS the email body + each attachment as real content blobs + document
// versions — not just metadata rows. Drives the endpoint end-to-end against a
// real Postgres + real MinIO, with the storage-service boundary stood in by
// the same fakeStorageClient the office-save test uses (InitiateUpload →
// direct S3 put → CompleteUpload inserts a content_blobs row). Reuses
// allowPolicy / fakeStorageClient / officeTestBucket from
// office_save_integration_test.go (same package + build tag).
//
// Run: go test -tags integration -run TestM365Ingest ./internal/handler/
package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
	pkgstorage "github.com/aieera/sedoc/pkg/storage"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/document/internal/repository"
	"github.com/aieera/sedoc/services/document/internal/service"
)

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
	require.NoError(t, s3c.CreateBucket(ctx, officeTestBucket, "us-east-1"))

	repos := repository.New(pool)
	svc := service.New(pool, repos, allowPolicy{}, zerolog.Nop())

	// Tenant / user / workspace / folder.
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

	// Handler wired with the real S3 client + the fake storage service.
	fake := &fakeStorageClient{pool: pool, s3: s3c, pending: map[string]pendingUpload{}}
	h := NewM365IngestHandler(pool, svc, fake, s3c, zerolog.Nop())

	// Ingest an email with a body + one attachment.
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
	require.False(t, resp.Pending, "storage is wired → blobs persisted inline, not pending")

	// The parent email document must carry a version linking a real blob.
	parentID := uuid.MustParse(resp.DocumentID)
	require.Equal(t, 1, versionCountFor(t, ctx, pool, tenant, parentID), "email body persisted as a version")

	// The attachment document must carry a version too.
	childID := uuid.MustParse(resp.AttachmentDocumentIDs[0])
	require.Equal(t, 1, versionCountFor(t, ctx, pool, tenant, childID), "attachment persisted as a version")

	// Two distinct content blobs were written (body + attachment).
	var blobCount int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM content_blobs WHERE tenant_id = $1`, tenant).Scan(&blobCount))
	require.Equal(t, 2, blobCount, "email body + attachment stored as content blobs")

	// The version's blob is downloadable — the object really landed in MinIO.
	var bucket, key string
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT b.storage_bucket, b.storage_key
		FROM document_versions v JOIN content_blobs b ON b.id = v.content_blob_id
		WHERE v.tenant_id = $1 AND v.document_id = $2`, tenant, childID).Scan(&bucket, &key))
	info, gerr := s3c.GetObjectInfo(ctx, bucket, key)
	require.NoError(t, gerr, "attachment object must exist in object storage")
	require.Equal(t, int64(len(attachment)), info.Size)
}

func versionCountFor(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenant, docID uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM document_versions WHERE tenant_id=$1 AND document_id=$2`,
		tenant, docID).Scan(&n))
	return n
}
