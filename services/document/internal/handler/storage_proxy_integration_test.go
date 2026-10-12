//go:build integration

// Storage REST proxy over real Postgres + the real DocumentService, with the
// storage gRPC surface stubbed (proxyStorageStub).
package handler

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/document/internal/repository"
	"github.com/aieera/sedoc/services/document/internal/service"
)

type proxyHarness struct {
	ctx      context.Context
	pool     *pgxpool.Pool
	svc      *service.DocumentService
	tenant   uuid.UUID
	caller   auth.UserInfo
	wsID     uuid.UUID
	folderID uuid.UUID
}

func newProxyHarness(t *testing.T) *proxyHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)
	dsn, clean, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(clean)
	require.NoError(t, database.RunMigrations(dsn, "../../migrations"))
	cfg := database.DefaultPoolConfig()
	cfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	h := &proxyHarness{ctx: ctx, pool: pool, tenant: uuid.Must(uuid.NewV7())}
	h.svc = service.New(pool, repository.New(pool), allowPolicy{}, zerolog.Nop())
	user := uuid.Must(uuid.NewV7())
	h.caller = auth.UserInfo{ID: user, TenantID: h.tenant, Role: "admin"}
	_, err = pool.Exec(ctx, `
		INSERT INTO organizations (id, name, slug, plan, primary_region)
		VALUES ($1, 'Proxy Org', $2, 'standard', 'us-east-1')`, h.tenant, "px-"+h.tenant.String()[:8])
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO users (tenant_id, id, email, display_name, role, status)
		VALUES ($1, $2, $3, 'Proxy User', 'admin', 'active')`, h.tenant, user, "px-"+user.String()[:8]+"@test.local")
	require.NoError(t, err)
	h.wsID = h.seedWorkspace(t, "us-east-1")
	folder, err := h.svc.CreateFolder(h.callCtx(), &service.CreateFolderInput{WorkspaceID: h.wsID, Name: "Root"})
	require.NoError(t, err)
	h.folderID = folder.ID
	return h
}

func (h *proxyHarness) callCtx() context.Context {
	return auth.WithUser(auth.SetTenantID(h.ctx, h.tenant), h.caller)
}

func (h *proxyHarness) seedWorkspace(t *testing.T, region string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	_, err := h.pool.Exec(h.ctx, `
		INSERT INTO workspaces (tenant_id, id, name, region_pin, created_by)
		VALUES ($1, $2, 'ws', $3, $4)`, h.tenant, id, region, h.caller.ID)
	require.NoError(t, err)
	return id
}

// docWithVersion creates a document with one version over a freshly
// "uploaded" blob (content_blobs row + the completed session that made it,
// laid out under the storage service's key scheme).
func (h *proxyHarness) docWithVersion(t *testing.T, title string) (docID, versionID uuid.UUID) {
	t.Helper()
	doc, err := h.svc.CreateDocument(h.callCtx(), &service.CreateDocumentInput{
		UpdatedBy: h.caller.ID, WorkspaceID: h.wsID, FolderID: h.folderID, Title: title,
	})
	require.NoError(t, err)
	session, blob := uuid.New(), uuid.New()
	key := fmt.Sprintf("%s/2026/10/%s/%s.pdf", h.tenant, session, title)
	_, err = h.pool.Exec(h.ctx, `
		INSERT INTO content_blobs (id, tenant_id, sha256_hash, storage_region, storage_bucket, storage_key, size_bytes, mime_type)
		VALUES ($1, $2, $3, 'us-east-1', 'dms-us-east-1-hot', $4, 512, 'application/pdf')`,
		blob, h.tenant, fmt.Sprintf("%064x", blob[:]), key)
	require.NoError(t, err)
	_, err = h.pool.Exec(h.ctx, `
		INSERT INTO upload_sessions (tenant_id, id, filename, total_size, mime_type, upload_type,
		                             storage_region, status, created_by, completed_at, expires_at, content_blob_id)
		VALUES ($1, $2, 'f.pdf', 512, 'application/pdf', 'single', 'us-east-1', 'completed', $3,
		        now(), now() + interval '1 hour', $4)`, h.tenant, session, h.caller.ID, blob)
	require.NoError(t, err)
	v, err := h.svc.CreateVersion(h.callCtx(), &service.CreateVersionInput{DocumentID: doc.ID, ContentBlobID: blob})
	require.NoError(t, err)
	return doc.ID, v.ID
}

// M-2: the proxy checked view on the document in the URL but resolved the
// version by id alone, so a caller with view on A could fetch B's bytes via
// /downloads/{A}/{versionOfB}.
func TestStorageProxyDownload_VersionOfOtherDocument404(t *testing.T) {
	h := newProxyHarness(t)
	docA, _ := h.docWithVersion(t, "a")
	docB, versionB := h.docWithVersion(t, "b")
	stub := &proxyStorageStub{}
	p := NewStorageProxy(stub, h.pool, h.svc)

	rec := serveProxy(t, p, h.ctx, h.caller, http.MethodGet,
		fmt.Sprintf("/api/v1/storage/downloads/%s/%s", docA, versionB), "")
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	require.Empty(t, stub.downloads, "no presigned URL may be minted for another document's version")

	rec = serveProxy(t, p, h.ctx, h.caller, http.MethodGet,
		fmt.Sprintf("/api/v1/storage/downloads/%s/%s", docB, versionB), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Len(t, stub.downloads, 1)
}

// M-7 / DEC-11: initiate forwarded the client's region_pin, so bytes could
// land outside the target document's residency. The proxy now derives the
// region from the target (document > folder > workspace) and ignores the body.
func TestStorageProxyInitiate_IgnoresBodyRegion(t *testing.T) {
	h := newProxyHarness(t)
	euWS := h.seedWorkspace(t, "eu-west-1")
	euFolder, err := h.svc.CreateFolder(h.callCtx(), &service.CreateFolderInput{WorkspaceID: euWS, Name: "EU"})
	require.NoError(t, err)
	euDoc, err := h.svc.CreateDocument(h.callCtx(), &service.CreateDocumentInput{
		UpdatedBy: h.caller.ID, WorkspaceID: euWS, FolderID: euFolder.ID, Title: "eu doc",
	})
	require.NoError(t, err)
	stub := &proxyStorageStub{}
	p := NewStorageProxy(stub, h.pool, h.svc)

	for _, body := range []string{
		fmt.Sprintf(`{"region_pin":"us-east-1","filename":"a.pdf","size_bytes":1,"folder_id":%q}`, euFolder.ID),
		fmt.Sprintf(`{"region_pin":"us-east-1","filename":"a.pdf","size_bytes":1,"workspace_id":%q}`, euWS),
		fmt.Sprintf(`{"region_pin":"us-east-1","filename":"a.pdf","size_bytes":1,"document_id":%q}`, euDoc.ID),
	} {
		rec := serveProxy(t, p, h.ctx, h.caller, http.MethodPost, "/api/v1/storage/uploads/initiate", body)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}
	require.Len(t, stub.initiated, 3)
	for _, req := range stub.initiated {
		require.Equal(t, "eu-west-1", req.GetRegionPin(), "region comes from the target, not the body")
	}
}
