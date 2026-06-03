//go:build integration
// +build integration

// Run with: go test -tags integration ./services/document/internal/service/...
//
// This file exercises the service against a real Postgres (via testcontainers)
// and a stubbed Policy client. It is the template for the rest of the service
// test pyramid:
//
//   - model/*.go           : pure-Go unit tests (see lifecycle_test.go)
//   - service/*_test.go    : integration w/ real DB + mocked Policy  (this file)
//   - repository/*_test.go : low-level SQL integration (same testcontainer;
//                             assert RLS, cursor pagination, ltree moves)
package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	vaultdmsv1 "github.com/aieera/sedoc/proto/gen/go/vaultdms/v1"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/repository"
	"github.com/aieera/sedoc/services/document/internal/service"

	"github.com/rs/zerolog"
)

// ---- Test harness ---------------------------------------------------------

type stubPolicy struct {
	allow bool
}

func (s stubPolicy) CheckPermission(_ context.Context, _ *vaultdmsv1.CheckPermissionRequest, _ ...grpc.CallOption) (*vaultdmsv1.CheckPermissionResponse, error) {
	return &vaultdmsv1.CheckPermissionResponse{Allowed: s.allow}, nil
}
func (s stubPolicy) BatchCheckPermission(_ context.Context, in *vaultdmsv1.BatchCheckPermissionRequest, _ ...grpc.CallOption) (*vaultdmsv1.BatchCheckPermissionResponse, error) {
	out := &vaultdmsv1.BatchCheckPermissionResponse{}
	for range in.GetChecks() {
		out.Results = append(out.Results, &vaultdmsv1.CheckPermissionResponse{Allowed: s.allow})
	}
	return out, nil
}

type harness struct {
	ctx    context.Context
	svc    *service.DocumentService
	repos  *repository.Repositories
	tenant uuid.UUID
	user   uuid.UUID
}

func newHarness(t *testing.T, allow bool) *harness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)

	dsn, cleanup, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(cleanup)

	// Apply Phase D + Phase 5 migrations.
	require.NoError(t, database.RunMigrations(dsn, "../../migrations"))

	pool, err := database.NewPool(ctx, dsn, database.DefaultPoolConfig())
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })

	repos := repository.New(pool)
	svc := service.New(pool, repos, stubPolicy{allow: allow}, zerolog.Nop())

	tenant := uuid.Must(uuid.NewV7())
	user := uuid.Must(uuid.NewV7())
	ctx = auth.SetTenantID(ctx, tenant)
	ctx = auth.WithUser(ctx, auth.UserInfo{ID: user, TenantID: tenant, Role: "admin"})

	return &harness{ctx: ctx, svc: svc, repos: repos, tenant: tenant, user: user}
}

// ---- Representative tests (replicate this pattern for the other 20+ methods)

func TestCreateDocument_Success(t *testing.T) {
	h := newHarness(t, true)

	// Seed a workspace + root folder directly.
	wsID := uuid.Must(uuid.NewV7())
	folder := mustCreateFolder(t, h, wsID, "Root")

	doc, err := h.svc.CreateDocument(h.ctx, &service.CreateDocumentInput{
		WorkspaceID: wsID,
		FolderID:    folder.ID,
		Title:       "Meeting notes 2026-04-15",
		RegionPin:   "us-east-1",
		Tags:        []string{"notes", "q2"},
	})
	require.NoError(t, err)
	require.Equal(t, model.StateDraft, doc.LifecycleState)
	require.Equal(t, "us-east-1", doc.RegionPin)
}

func TestCreateDocument_Forbidden(t *testing.T) {
	h := newHarness(t, false) // policy denies everything
	_, err := h.svc.CreateDocument(h.ctx, &service.CreateDocumentInput{
		WorkspaceID: uuid.Must(uuid.NewV7()),
		FolderID:    uuid.Must(uuid.NewV7()),
		Title:       "x",
		RegionPin:   "us-east-1",
	})
	require.Error(t, err)
}

func TestDeleteDocument_BlockedByLegalHold(t *testing.T) {
	h := newHarness(t, true)
	doc := mustCreateDocumentWith(t, h, model.StateLegalHold)
	err := h.svc.DeleteDocument(h.ctx, doc.ID)
	require.Error(t, err)
}

func TestTenantIsolation_RLS(t *testing.T) {
	h := newHarness(t, true)
	// Create a doc as tenant A.
	doc := mustCreateDocumentWith(t, h, model.StateActive)

	// Switch to tenant B and attempt to load it.
	other := uuid.Must(uuid.NewV7())
	ctxB := auth.SetTenantID(context.Background(), other)
	ctxB = auth.WithUser(ctxB, auth.UserInfo{ID: h.user, TenantID: other, Role: "admin"})

	_, _, err := h.svc.GetDocument(ctxB, doc.ID)
	require.Error(t, err, "tenant B must not see tenant A's document")
}

// ---- Helpers --------------------------------------------------------------

func mustCreateFolder(t *testing.T, h *harness, wsID uuid.UUID, name string) *model.Folder {
	t.Helper()
	f, err := h.svc.CreateFolder(h.ctx, &service.CreateFolderInput{
		WorkspaceID: wsID,
		Name:        name,
	})
	require.NoError(t, err)
	return f
}

func mustCreateDocumentWith(t *testing.T, h *harness, state model.LifecycleState) *model.Document {
	t.Helper()
	wsID := uuid.Must(uuid.NewV7())
	f := mustCreateFolder(t, h, wsID, "R")
	doc, err := h.svc.CreateDocument(h.ctx, &service.CreateDocumentInput{
		WorkspaceID: wsID,
		FolderID:    f.ID,
		Title:       "t",
		RegionPin:   "us-east-1",
	})
	require.NoError(t, err)
	if state != model.StateDraft {
		// Direct repo poke: bypass the state machine so we can create docs in
		// arbitrary starting states for tests.
		err := database.WithTenantTx(h.ctx, h.repos.Pool, h.tenant, func(tx pgx.Tx) error {
			return h.repos.Documents.UpdateLifecycleState(h.ctx, tx, h.tenant, doc.ID, state)
		})
		require.NoError(t, err)
		doc.LifecycleState = state
	}
	return doc
}
