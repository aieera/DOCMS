//go:build integration
// +build integration

// Acceptance test for Workstream 7 — ListDocuments per-row ACL. A list must
// never return a document the caller can't read, even when the caller is
// authorized for the workspace/folder: individual rows may sit in private
// folders or carry document-level restrictions. ListDocuments batch-checks
// "view" on every returned row and drops the denied ones.
//
// Run with: go test -tags integration ./services/document/internal/service/...
package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/repository"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// selectivePolicy allows every CheckPermission (so test setup passes) but denies
// "view" on one named document id in BatchCheckPermission — the per-row gate.
type selectivePolicy struct{ deniedDocID string }

func (p *selectivePolicy) CheckPermission(_ context.Context, _ *sedocv1.CheckPermissionRequest, _ ...grpc.CallOption) (*sedocv1.CheckPermissionResponse, error) {
	return &sedocv1.CheckPermissionResponse{Allowed: true}, nil
}

func (p *selectivePolicy) BatchCheckPermission(_ context.Context, in *sedocv1.BatchCheckPermissionRequest, _ ...grpc.CallOption) (*sedocv1.BatchCheckPermissionResponse, error) {
	out := &sedocv1.BatchCheckPermissionResponse{}
	for _, c := range in.GetChecks() {
		out.Results = append(out.Results, &sedocv1.CheckPermissionResponse{
			Allowed: c.GetResourceId() != p.deniedDocID,
		})
	}
	return out, nil
}

func TestListDocuments_PerRowACLDropsDeniedRows(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)

	dsn, pgCleanup, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(pgCleanup)

	poolCfg := database.DefaultPoolConfig()
	poolCfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, poolCfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	_, err = pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS document_entities (
			tenant_id UUID NOT NULL, id UUID NOT NULL DEFAULT gen_random_uuid(),
			version_id UUID NOT NULL, document_id UUID NOT NULL, entity_type TEXT NOT NULL,
			entity_value TEXT NOT NULL, start_offset INT NOT NULL DEFAULT 0, end_offset INT NOT NULL DEFAULT 0,
			confidence REAL NOT NULL DEFAULT 0, is_pii BOOLEAN NOT NULL DEFAULT false,
			detected_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY (tenant_id, id))`)
	require.NoError(t, err)
	require.NoError(t, database.RunMigrations(dsn, "../../migrations"))

	tenant := uuid.Must(uuid.NewV7())
	user := uuid.Must(uuid.NewV7())
	wsID := uuid.Must(uuid.NewV7())
	folderID := uuid.Must(uuid.NewV7())
	seedOrg(ctx, t, pool, tenant)
	seedUser(ctx, t, pool, tenant, user)
	seedWorkspaceFolder(ctx, t, pool, tenant, wsID, folderID)

	policy := &selectivePolicy{}
	svc := service.New(pool, repository.New(pool), policy, zerolog.Nop())

	// Caller is a plain MEMBER (not tenant admin) so per-row filtering runs —
	// admins/owners are intentionally skipped.
	callCtx := auth.SetTenantID(ctx, tenant)
	callCtx = auth.WithUser(callCtx, auth.UserInfo{ID: user, TenantID: tenant, Role: "member"})

	docA, err := svc.CreateDocument(callCtx, &service.CreateDocumentInput{
		WorkspaceID: wsID, FolderID: folderID, Title: "Visible A", UpdatedBy: user,
	})
	require.NoError(t, err)
	docB, err := svc.CreateDocument(callCtx, &service.CreateDocumentInput{
		WorkspaceID: wsID, FolderID: folderID, Title: "Hidden B", UpdatedBy: user,
	})
	require.NoError(t, err)

	// Deny per-row "view" on B.
	policy.deniedDocID = docB.ID.String()

	page, err := svc.ListDocuments(callCtx, model.DocumentFilter{WorkspaceID: &wsID, PageSize: 50})
	require.NoError(t, err)

	ids := map[uuid.UUID]bool{}
	for i := range page.Items {
		ids[page.Items[i].ID] = true
	}
	require.True(t, ids[docA.ID], "A is viewable and must be listed")
	require.False(t, ids[docB.ID], "B is denied per-row and must NOT leak into the list")

	// Admin caller bypasses the per-row gate (sees everything).
	adminCtx := auth.SetTenantID(ctx, tenant)
	adminCtx = auth.WithUser(adminCtx, auth.UserInfo{ID: user, TenantID: tenant, Role: "admin"})
	adminPage, err := svc.ListDocuments(adminCtx, model.DocumentFilter{WorkspaceID: &wsID, PageSize: 50})
	require.NoError(t, err)
	require.Len(t, adminPage.Items, 2, "admin sees both rows (per-row gate skipped)")
}
