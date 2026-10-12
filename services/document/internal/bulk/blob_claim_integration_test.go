//go:build integration

// DEC-12 / DEC-7 on bulk import: linkBlobVersion linked ANY tenant blob id
// the import named (bytes the importer never uploaded and may not read) and
// appended versions without the document row lock or the legal-hold gate.
// It now goes through the document service's claimBlobTx and assertNotHeld
// after repository.LockDocument, like CreateVersion.
package bulk

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/aieera/sedoc/pkg/auth"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
	"github.com/aieera/sedoc/services/document/internal/compliance"
	"github.com/aieera/sedoc/services/document/internal/repository"
	"github.com/aieera/sedoc/services/document/internal/service"
)

type allowAll struct{}

func (allowAll) CheckPermission(context.Context, *sedocv1.CheckPermissionRequest, ...grpc.CallOption) (*sedocv1.CheckPermissionResponse, error) {
	return &sedocv1.CheckPermissionResponse{Allowed: true}, nil
}

func (allowAll) BatchCheckPermission(_ context.Context, in *sedocv1.BatchCheckPermissionRequest, _ ...grpc.CallOption) (*sedocv1.BatchCheckPermissionResponse, error) {
	out := &sedocv1.BatchCheckPermissionResponse{}
	for range in.GetChecks() {
		out.Results = append(out.Results, &sedocv1.CheckPermissionResponse{Allowed: true})
	}
	return out, nil
}

// seedUploaded inserts a blob plus the completed upload session by uploader.
func seedUploaded(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tenant, uploader uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	sum := sha256.Sum256([]byte(id.String()))
	_, err := pool.Exec(ctx, `
		INSERT INTO content_blobs (id, tenant_id, sha256_hash, storage_region, storage_bucket, storage_key, size_bytes, mime_type)
		VALUES ($1, $2, $3, 'us-east-1', 'b', $4, 10, 'application/pdf')`, id, tenant, hex.EncodeToString(sum[:]), "k/"+id.String())
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO upload_sessions (tenant_id, id, filename, total_size, upload_type, status, created_by,
		                             completed_at, expires_at, content_blob_id)
		VALUES ($1, gen_random_uuid(), 'f.pdf', 10, 'single', 'completed', $2, now(), now() + interval '1 hour', $3)`,
		tenant, uploader, id)
	require.NoError(t, err)
	return id
}

func TestBulkImport_ForeignBlobRejected(t *testing.T) {
	ctx, svc, pool, tenant := bulkFixture(t)
	svc.SetIntegrityGuard(service.New(pool, repository.New(pool), allowAll{}, zerolog.Nop()))
	caller, err := auth.GetUserID(ctx)
	require.NoError(t, err)
	other := uuid.Must(uuid.NewV7())
	_, err = pool.Exec(ctx, `INSERT INTO users (tenant_id, id, email, display_name, role)
		VALUES ($1,$2,$3,'Other','member')`, tenant, other, fmt.Sprintf("o-%s@t.local", other.String()[:8]))
	require.NoError(t, err)

	importDoc := func(extID string, blob uuid.UUID) *sedocv1.BulkItemResult {
		t.Helper()
		resp, err := svc.ProcessBatch(ctx, tenant, &sedocv1.BulkImportRequest{RequestId: uuid.NewString(), Items: []*sedocv1.BulkItem{
			{Resource: &sedocv1.BulkItem_Workspace{Workspace: &sedocv1.BulkWorkspace{ExternalId: "W-1", Name: "WS"}}},
			{Resource: &sedocv1.BulkItem_Document{Document: &sedocv1.BulkDocument{
				ExternalId: extID, Title: extID, WorkspaceExternalId: "W-1", ContentBlobId: blob.String(),
			}}},
		}})
		require.NoError(t, err)
		return resp.GetResults()[1]
	}
	versionsOf := func(blob uuid.UUID) int {
		var n int
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT count(*) FROM document_versions WHERE tenant_id = $1 AND content_blob_id = $2`, tenant, blob).Scan(&n))
		return n
	}

	// Someone else's upload is not linkable — and the document is not
	// created half-way either.
	foreign := seedUploaded(ctx, t, pool, tenant, other)
	res := importDoc("IMP-1", foreign)
	require.False(t, res.GetSuccess())
	require.Zero(t, versionsOf(foreign))
	require.Zero(t, countDocsLike(ctx, t, pool, tenant, "IMP-1"))

	// The importer's own upload is.
	own := seedUploaded(ctx, t, pool, tenant, caller)
	res = importDoc("IMP-2", own)
	require.True(t, res.GetSuccess(), res.GetError())
	require.Equal(t, 1, versionsOf(own))

	// A document under a compliance hold takes no new version by re-import.
	docID := uuid.MustParse(res.GetInternalId())
	_, err = compliance.NewHoldsService(pool).Create(ctx, tenant, caller, compliance.CreateInput{
		Name: "Matter", DocumentIDs: []uuid.UUID{docID},
	})
	require.NoError(t, err)
	next := seedUploaded(ctx, t, pool, tenant, caller)
	res = importDoc("IMP-2", next)
	require.False(t, res.GetSuccess(), "a held document must not be re-versioned by import")
	require.Zero(t, versionsOf(next))
}
