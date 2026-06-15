//go:build integration
// +build integration

// Acceptance tests for the "Stable external key + upsert-by-external-key"
// workstream. These pin the three guarantees from the spec:
//
//  1. upsert same external_id + same bytes twice → 1 document, 1 version.
//  2. same key + changed bytes → version 2, with version 1 intact.
//  3. concurrent upserts with the same key → exactly one document.
//
// Run with: go test -tags integration ./services/document/internal/service/...
//
// Reuses the seed helpers + stubPolicy defined in the package's other
// integration tests (version_uploaded_event_test.go, document_service_test.go).
package service_test

import (
	"context"
	"sync"
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

// externalKeyFixture spins up Postgres, migrates, and seeds org + user +
// workspace + (root) folder. Returns the service, the auth-bearing context,
// the pool, and the workspace/folder ids.
func externalKeyFixture(t *testing.T) (context.Context, *service.DocumentService, *pgxpool.Pool, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)

	dsn, pgCleanup, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(pgCleanup)

	// The testcontainer Postgres connects as a BYPASSRLS superuser; opt out
	// of the boot-time RLS-posture gate (every query under test still carries
	// an explicit tenant_id predicate, so isolation holds regardless).
	poolCfg := database.DefaultPoolConfig()
	poolCfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, poolCfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	// The document service's NER/OCR migrations (000021+) ALTER
	// `document_entities`, which is owned by the intelligence service's
	// migration set (a shared-DB table applied alongside in CI / make setup).
	// Pre-create it so the single-service document migration chain runs to
	// completion in this isolated fixture. The table is otherwise untouched
	// by these tests.
	_, err = pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS document_entities (
			tenant_id    UUID NOT NULL,
			id           UUID NOT NULL DEFAULT gen_random_uuid(),
			version_id   UUID NOT NULL,
			document_id  UUID NOT NULL,
			entity_type  TEXT NOT NULL,
			entity_value TEXT NOT NULL,
			start_offset INT  NOT NULL DEFAULT 0,
			end_offset   INT  NOT NULL DEFAULT 0,
			confidence   REAL NOT NULL DEFAULT 0,
			is_pii       BOOLEAN NOT NULL DEFAULT false,
			detected_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (tenant_id, id)
		)`)
	require.NoError(t, err)

	require.NoError(t, database.RunMigrations(dsn, "../../migrations"))

	tenant := uuid.Must(uuid.NewV7())
	user := uuid.Must(uuid.NewV7())
	wsID := uuid.Must(uuid.NewV7())
	folderID := uuid.Must(uuid.NewV7())
	seedOrg(ctx, t, pool, tenant)
	seedUser(ctx, t, pool, tenant, user)
	seedWorkspaceFolder(ctx, t, pool, tenant, wsID, folderID)

	svc := service.New(pool, repository.New(pool), stubPolicy{allow: true}, zerolog.Nop())

	callCtx := auth.SetTenantID(ctx, tenant)
	callCtx = auth.WithUser(callCtx, auth.UserInfo{ID: user, TenantID: tenant, Role: "admin"})
	// Stash the tenant id on the returned ctx value bag is unnecessary —
	// callers that need it for raw SQL use the pool + the returned ids.
	return callCtx, svc, pool, tenant, wsID, folderID
}

func TestUpsertByExternalKey_SameBytesTwice_OneDocOneVersion(t *testing.T) {
	callCtx, svc, pool, tenant, wsID, folderID := externalKeyFixture(t)
	ctx := context.Background()

	const sha = "aaaa1111bbbb2222cccc3333dddd4444eeee5555ffff6666aaaa7777bbbb8888"
	seedBlobSHA(ctx, t, pool, tenant, uuid.Must(uuid.NewV7()), sha, 1024, "application/pdf")

	in := &service.UpsertByExternalKeyInput{
		ExternalID:   "INV-2024-00188",
		WorkspaceID:  wsID,
		FolderID:     folderID,
		BlobChecksum: sha,
		UpdatedBy:    callerUser(t, callCtx),
	}

	r1, err := svc.UpsertDocumentByExternalKey(callCtx, in)
	require.NoError(t, err)
	require.True(t, r1.Created, "first upsert creates the document")
	require.True(t, r1.VersionCreated)
	require.Equal(t, 1, r1.CurrentVersionNumber)

	r2, err := svc.UpsertDocumentByExternalKey(callCtx, in)
	require.NoError(t, err)
	require.False(t, r2.Created, "second upsert is a no-op create")
	require.False(t, r2.VersionCreated, "identical bytes must not append a version")
	require.Equal(t, r1.DocumentID, r2.DocumentID, "same external_id resolves to the same document")
	require.Equal(t, 1, r2.CurrentVersionNumber)

	require.Equal(t, 1, countDocs(ctx, t, pool, tenant, "INV-2024-00188"))
	require.Equal(t, 1, countVersions(ctx, t, pool, tenant, r1.DocumentID))
}

func TestUpsertByExternalKey_ChangedBytes_AppendsVersionKeepingFirst(t *testing.T) {
	callCtx, svc, pool, tenant, wsID, folderID := externalKeyFixture(t)
	ctx := context.Background()
	user := callerUser(t, callCtx)

	const shaA = "1111111111111111111111111111111111111111111111111111111111111111"
	const shaB = "2222222222222222222222222222222222222222222222222222222222222222"
	seedBlobSHA(ctx, t, pool, tenant, uuid.Must(uuid.NewV7()), shaA, 1024, "application/pdf")
	seedBlobSHA(ctx, t, pool, tenant, uuid.Must(uuid.NewV7()), shaB, 2048, "application/pdf")

	r1, err := svc.UpsertDocumentByExternalKey(callCtx, &service.UpsertByExternalKeyInput{
		ExternalID: "INV-2024-00188", WorkspaceID: wsID, FolderID: folderID,
		BlobChecksum: shaA, UpdatedBy: user,
	})
	require.NoError(t, err)
	require.True(t, r1.Created)
	require.Equal(t, 1, r1.CurrentVersionNumber)

	r2, err := svc.UpsertDocumentByExternalKey(callCtx, &service.UpsertByExternalKeyInput{
		ExternalID: "INV-2024-00188", WorkspaceID: wsID, FolderID: folderID,
		BlobChecksum: shaB, UpdatedBy: user,
	})
	require.NoError(t, err)
	require.False(t, r2.Created, "same key reuses the document")
	require.True(t, r2.VersionCreated, "changed bytes append a version")
	require.Equal(t, r1.DocumentID, r2.DocumentID)
	require.Equal(t, 2, r2.CurrentVersionNumber)

	// Two versions, version 1 intact, head points at version 2.
	require.Equal(t, 2, countVersions(ctx, t, pool, tenant, r1.DocumentID))
	require.Equal(t, 1, countVersions(ctx, t, pool, tenant, r1.DocumentID, shaA), "version 1 (bytes A) preserved")
	require.Equal(t, 1, countVersions(ctx, t, pool, tenant, r1.DocumentID, shaB), "version 2 (bytes B) appended")

	var headSHA string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT v.sha256_hash FROM documents d
		   JOIN document_versions v ON v.tenant_id = d.tenant_id AND v.id = d.current_version_id
		  WHERE d.tenant_id = $1 AND d.id = $2`, tenant, r1.DocumentID).Scan(&headSHA))
	require.Equal(t, shaB, headSHA, "head advanced to the new version")
}

func TestUpsertByExternalKey_ConcurrentSameKey_ExactlyOneDoc(t *testing.T) {
	callCtx, svc, pool, tenant, wsID, folderID := externalKeyFixture(t)
	ctx := context.Background()
	user := callerUser(t, callCtx)

	const sha = "9999888877776666555544443333222211110000ffffeeeeddddccccbbbbaaaa"
	seedBlobSHA(ctx, t, pool, tenant, uuid.Must(uuid.NewV7()), sha, 1024, "application/pdf")

	const goroutines = 8
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		errs    []error
		created int
	)
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release all at once to maximize contention
			res, err := svc.UpsertDocumentByExternalKey(callCtx, &service.UpsertByExternalKeyInput{
				ExternalID: "INV-2024-00188", WorkspaceID: wsID, FolderID: folderID,
				BlobChecksum: sha, UpdatedBy: user,
			})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			if res.Created {
				created++
			}
		}()
	}
	close(start)
	wg.Wait()

	require.Empty(t, errs, "no upsert should error under contention")
	require.Equal(t, 1, created, "exactly one goroutine creates the document; the rest no-op")
	require.Equal(t, 1, countDocs(ctx, t, pool, tenant, "INV-2024-00188"), "no duplicate document for the key")
}

func TestGetDocumentByExternalKey_ResolvesIds(t *testing.T) {
	callCtx, svc, pool, tenant, wsID, folderID := externalKeyFixture(t)
	ctx := context.Background()
	user := callerUser(t, callCtx)

	const sha = "abcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcab"
	seedBlobSHA(ctx, t, pool, tenant, uuid.Must(uuid.NewV7()), sha, 512, "application/pdf")

	up, err := svc.UpsertDocumentByExternalKey(callCtx, &service.UpsertByExternalKeyInput{
		ExternalID: "PO-77", WorkspaceID: wsID, FolderID: folderID,
		BlobChecksum: sha, UpdatedBy: user,
	})
	require.NoError(t, err)

	docID, curVer, err := svc.GetDocumentByExternalKey(callCtx, "PO-77")
	require.NoError(t, err)
	require.Equal(t, up.DocumentID, docID)
	require.Equal(t, up.CurrentVersionID, curVer)

	_, _, err = svc.GetDocumentByExternalKey(callCtx, "DOES-NOT-EXIST")
	require.Error(t, err, "unknown key must not resolve")
}

// ---- local seed + assert helpers -----------------------------------------

func seedWorkspaceFolder(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tenant, wsID, folderID uuid.UUID) {
	t.Helper()
	_, err := pool.Exec(ctx, `INSERT INTO workspaces (tenant_id, id, name) VALUES ($1, $2, 'ws')`, tenant, wsID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO folders (tenant_id, id, workspace_id, name, path, depth)
		VALUES ($1, $2, $3, 'root', 'root', 0)`, tenant, folderID, wsID)
	require.NoError(t, err)
}

func seedBlobSHA(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tenant, id uuid.UUID, sha string, size int64, mime string) {
	t.Helper()
	_, err := pool.Exec(ctx, `
		INSERT INTO content_blobs
		  (id, tenant_id, sha256_hash, storage_region, storage_bucket, storage_key,
		   storage_class, size_bytes, mime_type)
		VALUES ($1, $2, $3, 'us-east-1', 'vaultdms-us-east-1', $4, 'hot', $5, $6)`,
		id, tenant, sha, "k/"+id.String(), size, mime)
	require.NoError(t, err)
}

func countDocs(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tenant uuid.UUID, externalID string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM documents WHERE tenant_id = $1 AND external_id = $2 AND deleted_at IS NULL`,
		tenant, externalID).Scan(&n))
	return n
}

// countVersions counts versions for a document, optionally filtered to a sha.
func countVersions(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tenant, docID uuid.UUID, sha ...string) int {
	t.Helper()
	var n int
	if len(sha) > 0 {
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT count(*) FROM document_versions WHERE tenant_id = $1 AND document_id = $2 AND sha256_hash = $3`,
			tenant, docID, sha[0]).Scan(&n))
		return n
	}
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM document_versions WHERE tenant_id = $1 AND document_id = $2`,
		tenant, docID).Scan(&n))
	return n
}

func callerUser(t *testing.T, ctx context.Context) uuid.UUID {
	t.Helper()
	u, err := auth.GetUserID(ctx)
	require.NoError(t, err)
	return u
}
