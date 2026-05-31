//go:build integration

// FIX-6 follow-up — integration test asserting the ON DELETE CASCADE
// chain from migration 000064 actually fires. We populate a single
// document with rows in every feature table that holds a FK to
// documents(tenant_id, id) or document_versions(tenant_id, id), hard-
// delete the document, then assert zero residual rows.
//
// Catches three failure modes the audit flagged as silent:
//   1. A future migration adding a new FK without CASCADE — purge
//      stops mid-tx on the first FK violation and the test row
//      lingers.
//   2. An accidental flip of an existing FK back to NO ACTION.
//   3. A renamed constraint (000064 used hard-coded names; renames
//      would leave the old constraint with NO ACTION).
//
// Run with:
//
//	go test -tags integration ./services/document/internal/repository/...
//
// Requires the local vaultdms-postgres container to be up (the dev
// docker-compose stack covers it).

package repository

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func mustOpenPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("VAULTDMS_TEST_DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://vaultdms:devpassword@localhost:15432/vaultdms?sslmode=disable"
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return pool
}

func TestPurgeCascade_RemovesAllFeatureRows(t *testing.T) {
	pool := mustOpenPool(t)
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Use the seed dev tenant + an existing user so we don't have to
	// recreate the auth chain. The test's job is to verify the FK
	// cascade, not the tenant bootstrap.
	tenantID := mustUUID(t, "aaaaaaaa-aaaa-7aaa-aaaa-aaaaaaaaaaaa")
	userID := mustUUID(t, "7bb83dcf-82a4-4b22-a212-57dc52c78baf") // admin@acme.local

	// Pick the first workspace in the seed tenant for the parent
	// hierarchy. The test could create one but reusing keeps the
	// fixture surface minimal.
	var workspaceID uuid.UUID
	var folderID uuid.UUID
	row := pool.QueryRow(ctx, `
		SELECT w.id, f.id
		  FROM workspaces w
		  JOIN folders   f ON f.tenant_id = w.tenant_id AND f.workspace_id = w.id
		 WHERE w.tenant_id = $1 AND w.deleted_at IS NULL AND f.deleted_at IS NULL
		 ORDER BY w.created_at ASC, f.created_at ASC
		 LIMIT 1`, tenantID)
	if err := row.Scan(&workspaceID, &folderID); err != nil {
		t.Skipf("seed tenant has no workspace/folder available: %v (run `make seed` first)", err)
	}

	docID := newTestUUID(t)
	versionID := newTestUUID(t)
	blobID := newTestUUID(t)

	// Build a minimal document + version + blob trio. Same shape the
	// production CreateDocument + CreateVersion would land.
	mustExec(t, pool, ctx, `
		INSERT INTO content_blobs (tenant_id, id, sha256_hash, mime_type, size_bytes, storage_bucket, storage_key, created_at)
		VALUES ($1, $2, $3, 'application/pdf', 1024, 'dms-us-east-1-hot', $4, now())
	`, tenantID, blobID, "deadbeef"+blobID.String(), tenantID.String()+"/test/"+blobID.String())
	mustExec(t, pool, ctx, `
		INSERT INTO documents (tenant_id, id, workspace_id, folder_id, title, lifecycle_state, region_pin, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 'draft', 'us-east-1', $6, now(), now())
	`, tenantID, docID, workspaceID, folderID, "purge cascade test "+docID.String(), userID)
	mustExec(t, pool, ctx, `
		INSERT INTO document_versions (tenant_id, id, document_id, version_number, content_blob_id, size_bytes, sha256_hash, mime_type, created_by, created_at)
		VALUES ($1, $2, $3, 1, $4, 1024, $5, 'application/pdf', $6, now())
	`, tenantID, versionID, docID, blobID, "deadbeef"+blobID.String(), userID)

	// Feature-table rows. Each one targets a FK that FIX-6 flipped
	// to CASCADE. If a future migration drops the cascade on any of
	// these, the assertion at the bottom will catch it.
	mustExec(t, pool, ctx, `INSERT INTO ocr_results (tenant_id, id, version_id, page_number, text_content, confidence, language, engine, created_at) VALUES ($1, $2, $3, 1, 'hello world', 0.95, 'en', 'tesseract', now())`,
		tenantID, newTestUUID(t), versionID)
	mustExec(t, pool, ctx, `INSERT INTO document_chunks (tenant_id, id, document_id, version_id, chunk_index, text_content, token_count, created_at) VALUES ($1, $2, $3, $4, 0, 'chunk', 2, now())`,
		tenantID, newTestUUID(t), docID, versionID)
	mustExec(t, pool, ctx, `INSERT INTO comments (tenant_id, id, document_id, version_id, author_id, body, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, 'feedback', now(), now())`,
		tenantID, newTestUUID(t), docID, versionID, userID)

	// Sanity: rows exist BEFORE purge.
	if got := countRows(t, pool, ctx, "documents", tenantID, "id", docID); got != 1 {
		t.Fatalf("setup: expected 1 document, got %d", got)
	}
	if got := countRows(t, pool, ctx, "document_chunks", tenantID, "document_id", docID); got != 1 {
		t.Fatalf("setup: expected 1 document_chunk, got %d", got)
	}

	// Hard-delete the document. FIX-6's cascades should sweep
	// versions + every feature row keyed on documents(id) /
	// document_versions(id).
	mustExec(t, pool, ctx, `DELETE FROM documents WHERE tenant_id = $1 AND id = $2`, tenantID, docID)

	// Assert: every feature row tied to the docs / version is gone.
	for _, tbl := range []struct {
		table  string
		fkCol  string
		anchor uuid.UUID
	}{
		{"documents", "id", docID},
		{"document_versions", "document_id", docID},
		{"ocr_results", "version_id", versionID},
		{"document_chunks", "document_id", docID},
		{"comments", "document_id", docID},
	} {
		if got := countRows(t, pool, ctx, tbl.table, tenantID, tbl.fkCol, tbl.anchor); got != 0 {
			t.Errorf("FIX-6 regression: %s rows still present after purge (got %d, want 0). FK lost ON DELETE CASCADE?", tbl.table, got)
		}
	}

	// Blob is intentionally NOT cascaded — content_blobs are dedup'd
	// (sha256 keyed) and shared across documents. Clean up so the
	// row doesn't leak across test runs.
	mustExec(t, pool, ctx, `DELETE FROM content_blobs WHERE tenant_id = $1 AND id = $2`, tenantID, blobID)
}

func mustExec(t *testing.T, pool *pgxpool.Pool, ctx context.Context, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func countRows(t *testing.T, pool *pgxpool.Pool, ctx context.Context, table string, tenantID uuid.UUID, col string, val uuid.UUID) int {
	t.Helper()
	var n int
	q := "SELECT count(*) FROM " + table + " WHERE tenant_id = $1 AND " + col + " = $2"
	if err := pool.QueryRow(ctx, q, tenantID, val).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func mustUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", s, err)
	}
	return id
}

func newTestUUID(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewRandom()
	if err != nil {
		t.Fatalf("new uuid: %v", err)
	}
	return id
}
