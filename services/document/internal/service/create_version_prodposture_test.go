//go:build integration && prodposture
// +build integration,prodposture

// H-26 under the production posture: the service connects as dms_app
// (NOBYPASSRLS) with the RLS gate armed. CreateVersion used to read the
// content_blobs row on the raw pool — outside the tenant GUC — so linking a
// freshly uploaded blob failed with "no rows" in production while the dev
// superuser masked it.
package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/document/internal/repository"
	"github.com/aieera/sedoc/services/document/internal/service"
)

func TestProdPosture_CreateVersionLinksBlob(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	db := testutil.NewProdPostureDB(ctx, t, "../../migrations")

	tenant := uuid.Must(uuid.NewV7())
	user := uuid.Must(uuid.NewV7())
	wsID := uuid.Must(uuid.NewV7())
	folderID := uuid.Must(uuid.NewV7())
	blobID := uuid.New()
	sessionID := uuid.New()
	const sha = "9a9a9a9a9a9a9a9a9a9a9a9a9a9a9a9a9a9a9a9a9a9a9a9a9a9a9a9a9a9a9a9a"

	// Seeding is ops-tooling work: superuser pool.
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, name, slug, plan, primary_region)
		  VALUES ($1, 'Posture Org', $2, 'standard', 'us-east-1')`, []any{tenant, "pp-" + tenant.String()[:8]}},
		{`INSERT INTO users (tenant_id, id, email, display_name, role, status)
		  VALUES ($1, $2, $3, 'Posture User', 'admin', 'active')`, []any{tenant, user, "pp-" + user.String()[:8] + "@test.local"}},
		{`INSERT INTO workspaces (tenant_id, id, name, region_pin) VALUES ($1, $2, 'ws', 'us-east-1')`, []any{tenant, wsID}},
		{`INSERT INTO folders (tenant_id, id, workspace_id, name, path, depth)
		  VALUES ($1, $2, $3, 'root', 'root', 0)`, []any{tenant, folderID, wsID}},
		{`INSERT INTO content_blobs (id, tenant_id, sha256_hash, storage_region, storage_bucket, storage_key, size_bytes, mime_type)
		  VALUES ($1, $2, $3, 'us-east-1', 'dms-us-east-1-hot', $4, 2048, 'application/pdf')`,
			[]any{blobID, tenant, sha, tenant.String() + "/2026/10/" + sessionID.String() + "/f.pdf"}},
		{`INSERT INTO upload_sessions (tenant_id, id, filename, total_size, mime_type, upload_type,
		                               storage_region, status, created_by, completed_at, expires_at, content_blob_id)
		  VALUES ($1, $2, 'f.pdf', 2048, 'application/pdf', 'single', 'us-east-1', 'completed', $3,
		          now(), now() + interval '1 hour', $4)`, []any{tenant, sessionID, user, blobID}},
	} {
		_, err := db.Super.Exec(ctx, q.sql, q.args...)
		require.NoError(t, err)
	}

	svc := service.New(db.App, repository.New(db.App), stubPolicy{allow: true}, zerolog.Nop())
	callCtx := auth.WithUser(auth.SetTenantID(ctx, tenant), auth.UserInfo{ID: user, TenantID: tenant, Role: "admin"})

	doc, err := svc.CreateDocument(callCtx, &service.CreateDocumentInput{
		UpdatedBy: user, WorkspaceID: wsID, FolderID: folderID, Title: "posture",
	})
	require.NoError(t, err)

	v, err := svc.CreateVersion(callCtx, &service.CreateVersionInput{DocumentID: doc.ID, ContentBlobID: blobID})
	require.NoError(t, err, "linking an uploaded blob must work under NOBYPASSRLS")
	require.Equal(t, int64(2048), v.SizeBytes, "size comes from the blob row")
	require.Equal(t, sha, v.SHA256Hash)
	require.Equal(t, "application/pdf", v.MimeType)

	var head uuid.UUID
	require.NoError(t, db.Super.QueryRow(ctx,
		`SELECT current_version_id FROM documents WHERE tenant_id = $1 AND id = $2`, tenant, doc.ID).Scan(&head))
	require.Equal(t, v.ID, head)
}
