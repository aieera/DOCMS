//go:build integration
// +build integration

// Acceptance test for Workstream 7 — bulk import parallelism + idempotency.
// Pins: a backfill of many documents runs through the bounded worker pool and is
// re-runnable safely (external_id dedupe across runs + duplicate-in-batch
// convergence), never producing duplicates.
//
// Run with: go test -tags integration ./services/document/internal/bulk/...
package bulk

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
	"github.com/aieera/sedoc/services/document/internal/repository"
)

func bulkFixture(t *testing.T) (context.Context, *Service, *pgxpool.Pool, uuid.UUID) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)

	dsn, pgCleanup, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(pgCleanup)

	cfg := database.DefaultPoolConfig()
	cfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, cfg)
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
	_, err = pool.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1,'t',$2)`,
		tenant, "t-"+tenant.String()[:8])
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO users (tenant_id, id, email, display_name, role)
		VALUES ($1,$2,$3,'Bulk User','admin')`, tenant, user, fmt.Sprintf("u-%s@t.local", user.String()[:8]))
	require.NoError(t, err)

	svc := NewService(pool, repository.New(pool), NewRepo(pool), nil, zerolog.Nop())
	svc.SetConcurrency(8)

	ctx = auth.SetTenantID(ctx, tenant)
	ctx = auth.WithUser(ctx, auth.UserInfo{ID: user, TenantID: tenant, Role: "admin"})
	return ctx, svc, pool, tenant
}

func countDocsLike(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tenant uuid.UUID, like string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM documents WHERE tenant_id=$1 AND external_id LIKE $2 AND deleted_at IS NULL`,
		tenant, like).Scan(&n))
	return n
}

func docBatch(reqID string, n int) *sedocv1.BulkImportRequest {
	items := []*sedocv1.BulkItem{{
		Resource: &sedocv1.BulkItem_Workspace{Workspace: &sedocv1.BulkWorkspace{ExternalId: "W-1", Name: "WS"}},
	}}
	for i := 0; i < n; i++ {
		items = append(items, &sedocv1.BulkItem{
			Resource: &sedocv1.BulkItem_Document{Document: &sedocv1.BulkDocument{
				ExternalId: fmt.Sprintf("DOC-%03d", i), Title: fmt.Sprintf("Doc %d", i),
				WorkspaceExternalId: "W-1",
			}},
		})
	}
	return &sedocv1.BulkImportRequest{RequestId: reqID, Items: items}
}

func TestBulkImport_ParallelAndReRunnable(t *testing.T) {
	ctx, svc, pool, tenant := bulkFixture(t)
	const n = 20 // > concurrency (8) so the worker pool is exercised

	// First run — parallel processing creates n documents.
	resp, err := svc.ProcessBatch(ctx, tenant, docBatch(uuid.NewString(), n))
	require.NoError(t, err)
	require.Len(t, resp.GetResults(), n+1) // workspace + n docs
	for _, r := range resp.GetResults() {
		require.True(t, r.GetSuccess(), "item %s failed: %s", r.GetExternalId(), r.GetError())
	}
	require.Equal(t, n, countDocsLike(ctx, t, pool, tenant, "DOC-%"))

	// Re-run the SAME items under a NEW request_id (so it isn't a cached replay).
	// external_id resolution must dedupe — no new documents.
	resp2, err := svc.ProcessBatch(ctx, tenant, docBatch(uuid.NewString(), n))
	require.NoError(t, err)
	for _, r := range resp2.GetResults() {
		require.True(t, r.GetSuccess(), "re-run item %s failed: %s", r.GetExternalId(), r.GetError())
	}
	require.Equal(t, n, countDocsLike(ctx, t, pool, tenant, "DOC-%"), "re-run must not duplicate")
}

func TestBulkImport_DuplicateExternalIDInBatchConverges(t *testing.T) {
	ctx, svc, pool, tenant := bulkFixture(t)

	// A workspace + two documents sharing one external_id, processed in parallel.
	// InsertUpsert must converge them onto a single row rather than the loser
	// hitting a unique violation.
	items := []*sedocv1.BulkItem{
		{Resource: &sedocv1.BulkItem_Workspace{Workspace: &sedocv1.BulkWorkspace{ExternalId: "W-1", Name: "WS"}}},
		{Resource: &sedocv1.BulkItem_Document{Document: &sedocv1.BulkDocument{ExternalId: "DUP-1", Title: "A", WorkspaceExternalId: "W-1"}}},
		{Resource: &sedocv1.BulkItem_Document{Document: &sedocv1.BulkDocument{ExternalId: "DUP-1", Title: "B", WorkspaceExternalId: "W-1"}}},
	}
	resp, err := svc.ProcessBatch(ctx, tenant, &sedocv1.BulkImportRequest{RequestId: uuid.NewString(), Items: items})
	require.NoError(t, err)
	for _, r := range resp.GetResults() {
		require.True(t, r.GetSuccess(), "item %s failed: %s", r.GetExternalId(), r.GetError())
	}
	require.Equal(t, 1, countDocsLike(ctx, t, pool, tenant, "DUP-%"), "duplicate external_id converges to one document")
}
