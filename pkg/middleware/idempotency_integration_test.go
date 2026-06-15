//go:build integration
// +build integration

// Integration test for the reserve → replay → status-code contract of the
// Idempotency middleware against a real Postgres. This is the guarantee every
// mutating route wired in Workstream 5 inherits: replaying a create with the
// same Idempotency-Key returns the cached response and runs the handler ONCE
// (so folder/document/etc. creation is duplicate-safe).
//
// Run with: go test -tags integration ./pkg/middleware/...
package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

func idempotencyFixture(t *testing.T) (*pgxpool.Pool, uuid.UUID) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)

	dsn, cleanup, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(cleanup)

	cfg := database.DefaultPoolConfig()
	cfg.SkipRLSPostureCheck = true // testcontainer connects as a BYPASSRLS superuser
	pool, err := database.NewPool(ctx, dsn, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	// Minimal schema the middleware needs (mirrors document migration 000066,
	// minus the org FK which we satisfy with a stub organizations table).
	_, err = pool.Exec(ctx, `
		CREATE TABLE organizations (id UUID PRIMARY KEY);
		CREATE TABLE idempotency_keys (
			tenant_id        UUID        NOT NULL REFERENCES organizations(id),
			idempotency_key  TEXT        NOT NULL,
			request_method   TEXT        NOT NULL,
			request_path     TEXT        NOT NULL,
			status           TEXT        NOT NULL DEFAULT 'in_progress',
			response_status  INT         NULL,
			response_body    BYTEA       NULL,
			created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
			completed_at     TIMESTAMPTZ NULL,
			PRIMARY KEY (tenant_id, idempotency_key)
		);`)
	require.NoError(t, err)

	tenant := uuid.Must(uuid.NewV7())
	_, err = pool.Exec(ctx, `INSERT INTO organizations (id) VALUES ($1)`, tenant)
	require.NoError(t, err)
	return pool, tenant
}

// reqWith builds a POST carrying the key + tenant on ctx.
func reqWith(tenant uuid.UUID, path, key string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, nil)
	r.Header.Set("Idempotency-Key", key)
	ctx := auth.SetTenantID(r.Context(), tenant)
	ctx = auth.WithUser(ctx, auth.UserInfo{ID: uuid.New(), TenantID: tenant, Role: apiKeyRole})
	return r.WithContext(ctx)
}

func TestIdempotency_ReplayRunsHandlerOnce(t *testing.T) {
	pool, tenant := idempotencyFixture(t)
	var calls int64
	h := IdempotencyRequired(pool)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"folder-1"}`))
	}))

	// First call → handler runs, 201, not replayed.
	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, reqWith(tenant, "/api/v1/workspaces/ws/folders", "key-A"))
	require.Equal(t, http.StatusCreated, w1.Code)
	require.Equal(t, "false", w1.Header().Get("Idempotency-Replayed"))
	require.JSONEq(t, `{"id":"folder-1"}`, w1.Body.String())

	// Replay same key → cached response, handler NOT run again.
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, reqWith(tenant, "/api/v1/workspaces/ws/folders", "key-A"))
	require.Equal(t, http.StatusCreated, w2.Code)
	require.Equal(t, "true", w2.Header().Get("Idempotency-Replayed"))
	require.JSONEq(t, `{"id":"folder-1"}`, w2.Body.String())

	require.Equal(t, int64(1), atomic.LoadInt64(&calls), "handler must run exactly once; replay creates nothing new")
}

func TestIdempotency_KeyReusedForDifferentPath_422(t *testing.T) {
	pool, tenant := idempotencyFixture(t)
	h := IdempotencyRequired(pool)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))

	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, reqWith(tenant, "/api/v1/workspaces/ws/folders", "key-B"))
	require.Equal(t, http.StatusCreated, w1.Code)

	// Same key, different path → 422 (the key identifies a different request).
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, reqWith(tenant, "/api/v1/documents", "key-B"))
	require.Equal(t, http.StatusUnprocessableEntity, w2.Code)
}

func TestIdempotency_ConcurrentSameKey_OneRuns(t *testing.T) {
	pool, tenant := idempotencyFixture(t)
	var calls int64
	release := make(chan struct{})
	h := IdempotencyRequired(pool)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&calls, 1)
		<-release // hold the reservation so the racer sees in_progress
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))

	var wg sync.WaitGroup
	codes := make([]int, 2)
	wg.Add(1)
	go func() {
		defer wg.Done()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, reqWith(tenant, "/p/folders", "key-C"))
		codes[0] = w.Code
	}()
	// Give the first goroutine time to reserve the key.
	time.Sleep(200 * time.Millisecond)
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, reqWith(tenant, "/p/folders", "key-C"))
	codes[1] = w2.Code
	close(release)
	wg.Wait()

	require.Equal(t, int64(1), atomic.LoadInt64(&calls), "only one execution under contention")
	require.Equal(t, http.StatusConflict, codes[1], "the in-flight racer gets 409")
}
