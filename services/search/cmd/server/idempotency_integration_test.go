//go:build integration

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // migrate driver
	_ "github.com/golang-migrate/migrate/v4/source/file"       // migrate source
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/search/internal/handler"
	"github.com/aieera/sedoc/services/search/internal/repository"
	"github.com/aieera/sedoc/services/search/internal/service"
)

// R-3 side note: search mounts the idempotency middleware on the saved-
// search mutations, but only the document service migrates the
// idempotency_keys table. Search connects to the same database as every
// other service (compose x-go-env SEDOC_DATABASE_URL, Helm
// global.database), so the table document migration 000066 creates is the
// store search uses. This drives the real wiring against that shared
// schema: a retried create with the same Idempotency-Key replays the first
// response and creates one row.
func TestSearchIdempotency_Store(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	t.Setenv("SEDOC_GATEWAY_SECRET", testGatewaySecret)
	t.Setenv("SEDOC_INTERNAL_API_KEY", testInternalKey)

	dsn, cleanup, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	m, err := migrate.New("file://../../../document/migrations", dsn)
	require.NoError(t, err)
	require.NoError(t, m.Steps(1), "document 000001 (organizations)")
	_, _ = m.Close()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	ddl, err := os.ReadFile("../../../document/migrations/000066_idempotency_keys.up.sql")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, string(ddl))
	require.NoError(t, err, "document 000066 (idempotency_keys) in the shared database")
	require.NoError(t, database.RunServiceMigrations(dsn, "../../migrations", "search"))

	tenant, user := uuid.New(), uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1, 't', $2)`, tenant, tenant.String())
	require.NoError(t, err)

	mux := http.NewServeMux()
	svc := service.New(service.Config{Repo: repository.New(pool), Logger: zerolog.Nop()})
	handler.New(svc, nil, zerolog.Nop()).Register(mux)
	h := newHTTPHandler(pool, mux)

	create := func() *httptest.ResponseRecorder {
		r := signedRequest(http.MethodPost, "/api/v1/saved-searches", `{"name":"n","query":"q"}`)
		r.Header.Set("X-Internal-Service-Key", testInternalKey)
		r.Header.Set("X-Auth-Tenant-ID", tenant.String())
		r.Header.Set("X-User-ID", user.String())
		r.Header.Set("Idempotency-Key", "erp-retry-1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	first := create()
	require.Equal(t, http.StatusCreated, first.Code, first.Body.String())
	second := create()
	require.Equal(t, http.StatusCreated, second.Code)
	require.Equal(t, "true", second.Header().Get("Idempotency-Replayed"))
	var a, b struct{ ID string }
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &a))
	require.NoError(t, json.Unmarshal(second.Body.Bytes(), &b))
	require.Equal(t, a.ID, b.ID)

	var rows, keys int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM saved_searches WHERE tenant_id = $1`, tenant).Scan(&rows))
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM idempotency_keys WHERE tenant_id = $1 AND status = 'completed'`, tenant).Scan(&keys))
	require.Equal(t, 1, rows)
	require.Equal(t, 1, keys)
}
