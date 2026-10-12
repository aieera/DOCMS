//go:build integration
// +build integration

// D-2 — watermarked page/thumbnail/status render IDOR.
//
// The /wm/* routes authorize the DOCUMENT in the URL (EnsureCanViewDocument)
// but hand the URL's version id to the preview service unchecked. Version
// ids are only tenant-unique, so a viewer of document A who learns a
// version id of document B (which they cannot open) got B's rendered pages
// through A's route. The download route already constrains the version to
// the document in SQL; the preamble now does so for every /wm/* route.
package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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
	"github.com/aieera/sedoc/services/document/internal/watermark"
)

// seedDocWithVersion inserts workspace → folder → document → blob → version
// (as head) for tenant and returns (docID, versionID).
func seedDocWithVersion(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tenant, user uuid.UUID, title string) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ws, folder, doc := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	blob, ver := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	stmts := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO workspaces (tenant_id, id, name, region_pin, created_by) VALUES ($1, $2, 'ws', 'us-east-1', $3)`,
			[]any{tenant, ws, user}},
		{`INSERT INTO folders (tenant_id, id, workspace_id, name, path, depth) VALUES ($1, $2, $3, 'root', 'root', 0)`,
			[]any{tenant, folder, ws}},
		{`INSERT INTO documents (tenant_id, id, workspace_id, folder_id, title, region_pin, created_by)
		  VALUES ($1, $2, $3, $4, $5, 'us-east-1', $6)`,
			[]any{tenant, doc, ws, folder, title, user}},
		{`INSERT INTO content_blobs (id, tenant_id, sha256_hash, storage_region, storage_bucket, storage_key, size_bytes, mime_type)
		  VALUES ($1, $2, $3, 'us-east-1', 'wm-bucket', $4, 10, 'application/pdf')`,
			[]any{blob, tenant, "sha-" + blob.String(), "k/" + blob.String()}},
		{`INSERT INTO document_versions (tenant_id, id, document_id, version_number, content_blob_id, size_bytes, mime_type, sha256_hash)
		  VALUES ($1, $2, $3, 1, $4, 10, 'application/pdf', $5)`,
			[]any{tenant, ver, doc, blob, "sha-" + blob.String()}},
		{`UPDATE documents SET current_version_id = $3 WHERE tenant_id = $1 AND id = $2`,
			[]any{tenant, doc, ver}},
	}
	for _, s := range stmts {
		_, err := pool.Exec(ctx, s.sql, s.args...)
		require.NoError(t, err, s.sql)
	}
	return doc, ver
}

func TestWatermarkStamp_ForeignVersion404(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)

	dsn, clean, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(clean)
	require.NoError(t, database.RunMigrations(dsn, "../../migrations"))
	cfg := database.DefaultPoolConfig()
	cfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	tenant, user := uuid.New(), uuid.Must(uuid.NewV7())
	_, err = pool.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1, 'wm', $2)`, tenant, "wm-"+tenant.String()[:8])
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO users (tenant_id, id, email, display_name, role, status)
		VALUES ($1, $2, 'viewer@test.local', 'Viewer', 'member', 'active')`, tenant, user)
	require.NoError(t, err)

	docA, verA := seedDocWithVersion(ctx, t, pool, tenant, user, "A — viewer may open")
	_, verB := seedDocWithVersion(ctx, t, pool, tenant, user, "B — someone else's")

	// Preview stub: counts every call. A foreign-version request must never
	// reach it.
	var previewCalls atomic.Int32
	preview := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		previewCalls.Add(1)
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ready","page_count":1}`))
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("\x89PNG\r\n\x1a\nfake"))
	}))
	t.Cleanup(preview.Close)

	svc := service.New(pool, repository.New(pool), allowPolicy{}, zerolog.Nop())
	h := NewWatermarkHandler(svc, watermark.NewPreviewClient(preview.URL, "svc-key"), pool, nil, nil, zerolog.Nop())
	mux := http.NewServeMux()
	h.Register(mux)

	do := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req = req.WithContext(auth.WithUser(auth.SetTenantID(req.Context(), tenant),
			auth.UserInfo{ID: user, TenantID: tenant, Role: "member", Email: "viewer@test.local"}))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	base := "/api/v1/documents/" + docA.String() + "/versions/"

	// Control: A's own version renders.
	rec := do(base + verA.String() + "/wm/thumbnail")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.EqualValues(t, 1, previewCalls.Load())

	// Foreign version through A's route: 404 on every render surface, and
	// the preview service is never asked.
	before := previewCalls.Load()
	for _, suffix := range []string{"/wm/thumbnail", "/wm/pages/1", "/wm/status"} {
		rec := do(base + verB.String() + suffix)
		require.Equal(t, http.StatusNotFound, rec.Code, "%s with a foreign version must 404, got body %s", suffix, rec.Body.String())
	}
	require.Equal(t, before, previewCalls.Load(), "foreign-version requests must not reach the preview service")

	// A random (non-existent) version id is also 404.
	rec = do(base + uuid.NewString() + "/wm/thumbnail")
	require.Equal(t, http.StatusNotFound, rec.Code)
}
