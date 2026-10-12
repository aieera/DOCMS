//go:build integration && prodposture
// +build integration,prodposture

// H-26 under the production posture: /storage/uploads/{id}/complete looked
// the produced blob up on the raw pool, outside the tenant GUC, so under the
// NOBYPASSRLS dms_app role it always answered content_blob_id "" and the
// client could not link the upload to a document.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/testutil"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
)

func TestProdPosture_StorageProxyCompleteReturnsBlobID(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	db := testutil.NewProdPostureDB(ctx, t, "../../migrations")

	tenant := uuid.Must(uuid.NewV7())
	user := uuid.Must(uuid.NewV7())
	blobID := uuid.New()
	sessionID := uuid.New()
	const sha = "8b8b8b8b8b8b8b8b8b8b8b8b8b8b8b8b8b8b8b8b8b8b8b8b8b8b8b8b8b8b8b8b"
	key := tenant.String() + "/2026/10/" + sessionID.String() + "/f.pdf"

	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, name, slug, plan, primary_region)
		  VALUES ($1, 'Posture Org', $2, 'standard', 'us-east-1')`, []any{tenant, "pp-" + tenant.String()[:8]}},
		{`INSERT INTO users (tenant_id, id, email, display_name, role, status)
		  VALUES ($1, $2, $3, 'Posture User', 'member', 'active')`, []any{tenant, user, "pp-" + user.String()[:8] + "@test.local"}},
		{`INSERT INTO content_blobs (id, tenant_id, sha256_hash, storage_region, storage_bucket, storage_key, size_bytes, mime_type)
		  VALUES ($1, $2, $3, 'us-east-1', 'dms-us-east-1-hot', $4, 2048, 'application/pdf')`, []any{blobID, tenant, sha, key}},
		{`INSERT INTO upload_sessions (tenant_id, id, filename, total_size, mime_type, upload_type,
		                               storage_region, status, created_by, completed_at, expires_at, content_blob_id)
		  VALUES ($1, $2, 'f.pdf', 2048, 'application/pdf', 'single', 'us-east-1', 'completed', $3,
		          now(), now() + interval '1 hour', $4)`, []any{tenant, sessionID, user, blobID}},
	} {
		_, err := db.Super.Exec(ctx, q.sql, q.args...)
		require.NoError(t, err)
	}

	stub := &proxyStorageStub{complete: &sedocv1.CompleteUploadResponse{
		StorageBucket: "dms-us-east-1-hot", StorageKey: key, SizeBytes: 2048, ChecksumSha256: sha,
	}}
	p := NewStorageProxy(stub, db.App, nil)
	rec := serveProxy(t, p, ctx, auth.UserInfo{ID: user, TenantID: tenant, Role: "member"},
		http.MethodPost, "/api/v1/storage/uploads/"+sessionID.String()+"/complete", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Equal(t, blobID.String(), out["content_blob_id"], "complete must hand back the blob id under NOBYPASSRLS")
}
