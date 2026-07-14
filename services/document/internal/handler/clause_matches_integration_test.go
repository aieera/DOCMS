//go:build integration
// +build integration

// ADR 0104 Phases 2/4 + approval — acceptance tests:
//  1. clause-matches endpoint returns rows joined with clause metadata.
//  2. variations groups by normalized text with counts.
//  3. approve sets approved_by/at (admin) and revoke clears them;
//     member role is 403.
//
// Harness note: this is an internal (package handler) integration test
// so the handler can be constructed directly via NewClauseMatchesHandler.
// There is no shared testPool/newTestTenant/doAuthedJSON fixture in this
// package yet — this file builds a self-contained one, following the
// proven pattern in internal/service/external_key_upsert_test.go
// (testcontainers Postgres + database.RunMigrations + raw-SQL seeding).
//
// Run with: go test -tags integration ./services/document/internal/handler/...
package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

// ---- package-level fixture state ------------------------------------------
//
// Each test calls newTestTenant(t), which spins up its own Postgres
// testcontainer (torn down via t.Cleanup) and repopulates these package
// vars. Tests in this file are not parallel, so this is safe.
var (
	testPool   *pgxpool.Pool
	testMux    *http.ServeMux
	testTenant uuid.UUID
	seedUserID uuid.UUID

	// docVersions maps a seeded document's id to the single
	// document_versions row created for it, so seedClauseWorld can
	// satisfy clause_matches' (tenant_id, version_id) FK with a real
	// version id instead of a random uuid.
	docVersions map[uuid.UUID]uuid.UUID
)

// newTestTenant creates an isolated Postgres testcontainer, runs the
// document service's migrations, seeds one organization (the tenant) and
// one admin user, and wires a ServeMux with the clause-matches routes
// registered. Returns a background context and the tenant id.
func newTestTenant(t *testing.T) (context.Context, uuid.UUID) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)

	dsn, pgCleanup, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(pgCleanup)

	// The document service's NER/OCR migrations (000021+) ALTER
	// `document_entities`, which is owned by the intelligence service's
	// migration set (a shared-DB table applied alongside in CI / make
	// setup). Pre-create it so the single-service document migration
	// chain runs to completion in this isolated fixture (same pattern
	// as internal/service/external_key_upsert_test.go).
	poolCfg := database.DefaultPoolConfig()
	poolCfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, poolCfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

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

	_, err = pool.Exec(ctx, `
		INSERT INTO organizations (id, name, slug) VALUES ($1, 'Test Org', $2)`,
		tenant, "t-"+tenant.String()[:8])
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO users (tenant_id, id, email, display_name, role)
		VALUES ($1, $2, $3, 'Test User', 'admin')`,
		tenant, user, "u-"+user.String()[:8]+"@test.local")
	require.NoError(t, err)

	mux := http.NewServeMux()
	NewClauseMatchesHandler(pool).Register(mux)

	testPool = pool
	testMux = mux
	testTenant = tenant
	seedUserID = user
	docVersions = map[uuid.UUID]uuid.UUID{}

	return ctx, tenant
}

// seedDocument inserts the parent chain a document needs (workspace, root
// folder, content blob) plus the document itself and its single
// document_versions row (version 1). The version id is stashed in
// docVersions so seedClauseWorld can point clause_matches at a real FK
// target.
func seedDocument(ctx context.Context, t *testing.T, tenant uuid.UUID, title string) uuid.UUID {
	t.Helper()
	wsID := uuid.Must(uuid.NewV7())
	folderID := uuid.Must(uuid.NewV7())
	docID := uuid.Must(uuid.NewV7())
	blobID := uuid.Must(uuid.NewV7())
	versionID := uuid.Must(uuid.NewV7())

	_, err := testPool.Exec(ctx, `
		INSERT INTO workspaces (tenant_id, id, name) VALUES ($1, $2, 'ws')`,
		tenant, wsID)
	require.NoError(t, err)
	_, err = testPool.Exec(ctx, `
		INSERT INTO folders (tenant_id, id, workspace_id, name, path, depth)
		VALUES ($1, $2, $3, 'root', 'root', 0)`,
		tenant, folderID, wsID)
	require.NoError(t, err)
	_, err = testPool.Exec(ctx, `
		INSERT INTO documents
			(tenant_id, id, workspace_id, folder_id, title, description, region_pin, sha256_hash, mime_type)
		VALUES ($1, $2, $3, $4, $5, '', 'us-east-1', '', '')`,
		tenant, docID, wsID, folderID, title)
	require.NoError(t, err)
	// blobID is a UUIDv7 (time-ordered): its first bytes are a millisecond
	// timestamp, so truncating it for a "unique" hash risks collisions
	// when two blobs are seeded within the same test (as seedClauseWorld
	// does for docA/docB). Use the full UUID to keep sha256_hash unique.
	sha := "sha-" + blobID.String()
	_, err = testPool.Exec(ctx, `
		INSERT INTO content_blobs
			(id, tenant_id, sha256_hash, storage_region, storage_bucket, storage_key,
			 storage_class, size_bytes, mime_type)
		VALUES ($1, $2, $3, 'us-east-1', 'test-bucket', $4, 'hot', $5, $6)`,
		blobID, tenant, sha, "k/"+blobID.String(), 1024, "application/pdf")
	require.NoError(t, err)
	_, err = testPool.Exec(ctx, `
		INSERT INTO document_versions
			(tenant_id, id, document_id, version_number, content_blob_id, size_bytes, mime_type, sha256_hash)
		VALUES ($1, $2, $3, 1, $4, 1024, 'application/pdf', $5)`,
		tenant, versionID, docID, blobID, sha)
	require.NoError(t, err)

	docVersions[docID] = versionID
	return docID
}

// seedClauseWorld creates a clause + two documents + three matches
// (two sharing normalized text).
func seedClauseWorld(ctx context.Context, t *testing.T, tenant uuid.UUID) (clauseID, docA, docB uuid.UUID) {
	t.Helper()
	clauseID = uuid.New()
	docA = seedDocument(ctx, t, tenant, "Contract A")
	docB = seedDocument(ctx, t, tenant, "Contract B")
	err := database.WithTenantTx(ctx, testPool, tenant, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO clauses (tenant_id, id, name, body_text, created_by)
			VALUES ($1, $2, 'Confidentiality', 'Each party shall keep confidential…', $3)`,
			tenant, clauseID, seedUserID); err != nil {
			return err
		}
		rows := []struct {
			docID uuid.UUID
			text  string
			sim   float32
		}{
			{docA, "Each party shall keep  Confidential…", 0.92},
			{docB, "each party SHALL keep confidential…", 0.88},
			{docB, "wholly different wording", 0.83},
		}
		for _, r := range rows {
			if _, err := tx.Exec(ctx, `
				INSERT INTO clause_matches
					(tenant_id, document_id, version_id, clause_id, similarity, matched_text)
				VALUES ($1, $2, $3, $4, $5, $6)`,
				tenant, r.docID, docVersions[r.docID], clauseID, r.sim, r.text); err != nil {
				return err
			}
		}
		return nil
	})
	require.NoError(t, err)
	return
}

// doAuthedJSON issues a request against the package-level testMux with a
// tenant + user context matching the currently seeded fixture (see
// newTestTenant), using role for the acting user's auth.UserInfo.Role.
func doAuthedJSON(t *testing.T, method, path string, body io.Reader, role string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	ctx := auth.SetTenantID(req.Context(), testTenant)
	ctx = auth.WithUser(ctx, auth.UserInfo{ID: seedUserID, TenantID: testTenant, Role: role})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	testMux.ServeHTTP(rec, req)
	return rec
}

func decodeMap(t *testing.T, resp *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &m))
	return m
}

// ---- tests ------------------------------------------------------------

func TestClauseMatches_ListForDocument(t *testing.T) {
	ctx, tenant := newTestTenant(t)
	clauseID, docA, _ := seedClauseWorld(ctx, t, tenant)

	resp := doAuthedJSON(t, "GET", "/api/v1/documents/"+docA.String()+"/clause-matches", nil, "owner")
	require.Equal(t, 200, resp.Code)
	body := decodeMap(t, resp)
	matches := body["matches"].([]any)
	require.Len(t, matches, 1)
	m := matches[0].(map[string]any)
	require.Equal(t, clauseID.String(), m["clause_id"])
	require.Equal(t, "Confidentiality", m["clause_name"])
	require.Equal(t, false, m["approved"])
}

func TestClauseVariations_GroupsNormalizedText(t *testing.T) {
	ctx, tenant := newTestTenant(t)
	clauseID, _, _ := seedClauseWorld(ctx, t, tenant)

	resp := doAuthedJSON(t, "GET", "/api/v1/clauses/"+clauseID.String()+"/variations", nil, "owner")
	require.Equal(t, 200, resp.Code)
	body := decodeMap(t, resp)
	variations := body["variations"].([]any)
	// Two matched_texts normalize identically; the third differs → 2 groups.
	require.Len(t, variations, 2)
	top := variations[0].(map[string]any)
	require.EqualValues(t, 2, top["occurrences"])
	require.EqualValues(t, 2, body["total_documents"])
}

func TestClauseApprove_SetsAndRevokes(t *testing.T) {
	ctx, tenant := newTestTenant(t)
	clauseID, _, _ := seedClauseWorld(ctx, t, tenant)
	_ = ctx

	resp := doAuthedJSON(t, "POST", "/api/v1/clauses/"+clauseID.String()+"/approve", nil, "owner")
	require.Equal(t, 200, resp.Code)
	body := decodeMap(t, resp)
	require.NotEmpty(t, body["approved_at"])

	resp = doAuthedJSON(t, "DELETE", "/api/v1/clauses/"+clauseID.String()+"/approve", nil, "owner")
	require.Equal(t, 200, resp.Code)

	resp = doAuthedJSON(t, "POST", "/api/v1/clauses/"+clauseID.String()+"/approve", nil, "member")
	require.Equal(t, 403, resp.Code)
}
