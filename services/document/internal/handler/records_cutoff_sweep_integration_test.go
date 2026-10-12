//go:build integration

// D-20: /internal/v1/records/cutoff-sweep had neither session nor
// internal-key auth and read the tenant from the request context, so the
// cron could never drive it (always 401) while any caller that did carry a
// session could. It is now gated by the internal key (constant-time, same
// as the retention sweep) and takes the tenant from the JSON body.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/document/internal/records"
)

func TestCutoffSweep_InternalKeyAndBodyTenant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	dsn, pgClean, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(pgClean)
	require.NoError(t, database.RunMigrations(dsn, "../../migrations"))
	cfg := database.DefaultPoolConfig()
	cfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	tenant, user, ws, folder, docID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, name, slug) VALUES ($1, 'T', $2)`, []any{tenant, "t-" + tenant.String()[:8]}},
		{`INSERT INTO users (tenant_id, id, email, display_name, role) VALUES ($1, $2, $3, 'U', 'admin')`,
			[]any{tenant, user, "u-" + user.String()[:8] + "@test.local"}},
		{`INSERT INTO workspaces (tenant_id, id, name) VALUES ($1, $2, 'ws')`, []any{tenant, ws}},
		{`INSERT INTO folders (tenant_id, id, workspace_id, name, path, depth) VALUES ($1, $2, $3, 'root', 'root', 0)`,
			[]any{tenant, folder, ws}},
		{`INSERT INTO documents (tenant_id, id, workspace_id, folder_id, title, lifecycle_state, created_by, updated_by, created_at)
		  VALUES ($1, $2, $3, $4, 'old', 'active', $5, $5, now() - interval '400 days')`, []any{tenant, docID, ws, folder, user}},
	} {
		_, err := pool.Exec(ctx, q.sql, q.args...)
		require.NoError(t, err)
	}
	rsvc := records.New(pool)
	sched, err := rsvc.CreateSchedule(ctx, tenant, records.ScheduleInput{Name: "c", TriggerEvent: "creation", RetentionPeriodDays: 30})
	require.NoError(t, err)
	cat, err := rsvc.CreateCategory(ctx, tenant, records.CategoryInput{Name: "cat", RetentionScheduleID: &sched.ID})
	require.NoError(t, err)
	_, err = rsvc.Declare(ctx, tenant, user, records.DeclareInput{DocumentID: docID, CategoryID: cat.ID})
	require.NoError(t, err)

	const key = "test-internal-key-0123456789"
	t.Setenv("SEDOC_INTERNAL_API_KEY", key)
	mux := http.NewServeMux()
	NewRecordsHandler(rsvc, zerolog.Nop()).RegisterInternal(mux)
	call := func(reqCtx context.Context, authz string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/internal/v1/records/cutoff-sweep", bytes.NewReader(raw)).WithContext(reqCtx)
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	body := map[string]string{"tenant_id": tenant.String()}

	// A session alone (tenant on ctx) is not the cron's credential.
	sessionCtx := auth.WithUser(auth.SetTenantID(ctx, tenant), auth.UserInfo{ID: user, TenantID: tenant, Role: "admin"})
	require.Equal(t, http.StatusUnauthorized, call(sessionCtx, "", body).Code)
	require.Equal(t, http.StatusUnauthorized, call(ctx, "Bearer wrong-key-0123456789abcd", body).Code)
	require.Equal(t, http.StatusUnauthorized, call(ctx, "Bearer "+key[:len(key)-1], body).Code)
	require.Equal(t, http.StatusBadRequest, call(ctx, "Bearer "+key, map[string]string{"tenant_id": "not-a-uuid"}).Code)

	rec := call(ctx, "Bearer "+key, body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out struct {
		Proposed int `json:"proposed"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Equal(t, 1, out.Proposed, "the body tenant's past-cutoff record is proposed")
}
