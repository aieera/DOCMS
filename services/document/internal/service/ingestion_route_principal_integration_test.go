//go:build integration

// D-14 / DEC-13: the ingest-route commit replays the uploader under their
// REAL role (read from the users table, never a hard-coded "admin") and
// needs `edit` on the matched document. When the uploader may not edit the
// document the OCR'd key matched, the item is parked in the review queue
// instead of being appended as a new version — and so is an item whose
// matched document is under a legal hold.
package service_test

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/aieera/sedoc/pkg/auth"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
	"github.com/aieera/sedoc/services/document/internal/compliance"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/repository"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// rolePolicy models OPA's owner/admin rule: `edit` on a guarded resource is
// allowed only when the forwarded user_role is admin or owner; everything
// else is allowed. It records the roles it was asked about.
type rolePolicy struct {
	guarded map[string]bool
	mu      *sync.Mutex
	roles   *[]string
}

func (p rolePolicy) decide(in *sedocv1.CheckPermissionRequest) bool {
	role := in.GetContext().GetFields()["user_role"].GetStringValue()
	if p.guarded[in.GetResourceId()] {
		p.mu.Lock()
		*p.roles = append(*p.roles, role)
		p.mu.Unlock()
		return role == "admin" || role == "owner"
	}
	return true
}

func (p rolePolicy) CheckPermission(_ context.Context, in *sedocv1.CheckPermissionRequest, _ ...grpc.CallOption) (*sedocv1.CheckPermissionResponse, error) {
	return &sedocv1.CheckPermissionResponse{Allowed: p.decide(in)}, nil
}

func (p rolePolicy) BatchCheckPermission(_ context.Context, in *sedocv1.BatchCheckPermissionRequest, _ ...grpc.CallOption) (*sedocv1.BatchCheckPermissionResponse, error) {
	out := &sedocv1.BatchCheckPermissionResponse{}
	for _, c := range in.GetChecks() {
		out.Results = append(out.Results, &sedocv1.CheckPermissionResponse{Allowed: p.decide(c)})
	}
	return out, nil
}

// seedMemberWithBlob adds an active member and a blob that member uploaded.
func seedMemberWithBlob(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tenant uuid.UUID, sha string) uuid.UUID {
	t.Helper()
	member := uuid.Must(uuid.NewV7())
	_, err := pool.Exec(ctx, `
		INSERT INTO users (tenant_id, id, email, display_name, role, status)
		VALUES ($1, $2, $3, 'Member', 'member', 'active')`, tenant, member, "m-"+member.String()+"@test.local")
	require.NoError(t, err)
	blob := uuid.Must(uuid.NewV7())
	_, err = pool.Exec(ctx, `
		INSERT INTO content_blobs (id, tenant_id, sha256_hash, storage_region, storage_bucket, storage_key,
		                           storage_class, size_bytes, mime_type)
		VALUES ($1, $2, $3, 'us-east-1', 'vaultdms-us-east-1', $4, 'hot', 2048, 'application/pdf')`,
		blob, tenant, sha, "k/"+blob.String())
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO upload_sessions (tenant_id, id, filename, total_size, upload_type,
		                             status, created_by, completed_at, expires_at, content_blob_id)
		VALUES ($1, gen_random_uuid(), 'scan.pdf', 2048, 'single', 'completed', $2,
		        now(), now() + interval '1 hour', $3)`, tenant, member, blob)
	require.NoError(t, err)
	return member
}

func TestIngestRouteCommit_NoEditOnMatchedDocGoesToReview(t *testing.T) {
	callCtx, svc, pool, tenant, wsID, folderID := externalKeyFixture(t)
	ctx := context.Background()

	// An existing document keyed INV-ROLE-1, owned by the admin.
	const shaV1 = "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1"
	seedBlobSHA(ctx, t, pool, tenant, uuid.Must(uuid.NewV7()), shaV1, 1024, "application/pdf")
	doc, err := svc.UpsertDocumentByExternalKey(callCtx, &service.UpsertByExternalKeyInput{
		ExternalID: "INV-ROLE-1", WorkspaceID: wsID, FolderID: folderID,
		BlobChecksum: shaV1, UpdatedBy: callerUser(t, callCtx),
	})
	require.NoError(t, err)

	// A member (who may not edit that document) scans a page whose OCR'd key
	// matches it, with high confidence.
	const shaScan = "b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2"
	member := seedMemberWithBlob(ctx, t, pool, tenant, shaScan)
	var mu sync.Mutex
	var roles []string
	guarded := serviceWithPolicy(pool, rolePolicy{guarded: map[string]bool{doc.DocumentID.String(): true}, mu: &mu, roles: &roles})
	memberCtx := auth.WithUser(auth.SetTenantID(ctx, tenant), auth.UserInfo{ID: member, TenantID: tenant, Role: "member"})
	item, err := guarded.CreateIngestionItem(memberCtx, &service.CreateIngestionInput{
		WorkspaceID: wsID, FolderID: folderID, TargetCustomerRef: "cust-R",
		BlobChecksum: shaScan, DocumentClass: "invoice",
	})
	require.NoError(t, err)
	markIngestionProcessed(ctx, t, pool, tenant, item.ID, "INV-ROLE-1", 0.97)

	outcome, _, reviewID := routeOutcome(t, guarded, tenant, item.ID)
	require.Equal(t, "needs_review", outcome, "no edit on the matched document: park for review, do not append")
	require.NotEmpty(t, reviewID)
	require.Equal(t, 1, countVersions(ctx, t, pool, tenant, doc.DocumentID), "matched document untouched")
	require.Equal(t, string(model.IngestNeedsReview), ingestionStatus(ctx, t, pool, tenant, item.ID))

	var suggested *uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT suggested_match_document_id FROM review_queue WHERE tenant_id = $1 AND id = $2`,
		tenant, uuid.MustParse(reviewID)).Scan(&suggested))
	require.NotNil(t, suggested)
	require.Equal(t, doc.DocumentID, *suggested)

	// A matched document under a legal hold (compliance binding) is also
	// parked for review instead of erroring the ingest activity.
	const shaHeldV1 = "c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3"
	seedBlobSHA(ctx, t, pool, tenant, uuid.Must(uuid.NewV7()), shaHeldV1, 1024, "application/pdf")
	heldDoc, err := svc.UpsertDocumentByExternalKey(callCtx, &service.UpsertByExternalKeyInput{
		ExternalID: "INV-HOLD-1", WorkspaceID: wsID, FolderID: folderID,
		BlobChecksum: shaHeldV1, UpdatedBy: callerUser(t, callCtx),
	})
	require.NoError(t, err)
	_, err = compliance.NewHoldsService(pool).Create(callCtx, tenant, callerUser(t, callCtx), compliance.CreateInput{
		Name: "Matter INV-HOLD", DocumentIDs: []uuid.UUID{heldDoc.DocumentID},
	})
	require.NoError(t, err)
	const shaHeldScan = "d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4"
	scanner := seedMemberWithBlob(ctx, t, pool, tenant, shaHeldScan)
	scannerCtx := auth.WithUser(auth.SetTenantID(ctx, tenant), auth.UserInfo{ID: scanner, TenantID: tenant, Role: "member"})
	heldItem, err := svc.CreateIngestionItem(scannerCtx, &service.CreateIngestionInput{
		WorkspaceID: wsID, FolderID: folderID, TargetCustomerRef: "cust-H",
		BlobChecksum: shaHeldScan, DocumentClass: "invoice",
	})
	require.NoError(t, err)
	markIngestionProcessed(ctx, t, pool, tenant, heldItem.ID, "INV-HOLD-1", 0.97)
	outcome, _, heldReview := routeOutcome(t, svc, tenant, heldItem.ID)
	require.Equal(t, "needs_review", outcome, "matched document under legal hold: park for review")
	require.NotEmpty(t, heldReview)
	require.Equal(t, 1, countVersions(ctx, t, pool, tenant, heldDoc.DocumentID), "held document untouched")

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, roles, "the matched document's edit gate was evaluated")
	for _, r := range roles {
		require.Equal(t, "member", r, "the commit replays the uploader's real role, never admin")
	}
}

func serviceWithPolicy(pool *pgxpool.Pool, policy service.PermissionChecker) *service.DocumentService {
	return service.New(pool, repository.New(pool), policy, zerolog.Nop())
}
