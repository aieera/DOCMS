//go:build integration && prodposture
// +build integration,prodposture

// Wave A.1 (issue #74) — the audit service end to end under prod
// NOBYPASSRLS, exercising BOTH coupled defects together:
//
//   - RLS: the hash-chained log must be readable and the chain must
//     link across >=3 events; verify-integrity returns a valid proof
//     and detects a tampered event.
//   - RBAC: a non-admin tenant member is denied (403) on read/export/
//     redact and the denial is itself recorded into the hash chain; an
//     admin succeeds. Tenant isolation holds on reads.
//
// Fixing RLS alone would open the trail to any tenant member (the
// defects mask each other), so this test asserts both at once. The
// repository-level TestProdPosture_AuditRepo stays as the read-level
// guard; this is the service+handler acceptance suite.
package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/audit/internal/handler"
	"github.com/aieera/sedoc/services/audit/internal/repository"
	"github.com/aieera/sedoc/services/audit/internal/service"
)

func TestProdPosture_AuditRBACAndChain(t *testing.T) {
	testutil.AssertProdPosture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)

	// audit_events lives in the document schema-of-record (000001:
	// partitioned + FORCE RLS); audit's own migrations ALTER it and add
	// checkpoints/sinks. Same fixture path as the repo repro.
	db := testutil.NewProdPostureDB(ctx, t, "")
	m, err := migrate.New("file://../../../../services/document/migrations", db.SuperDSN)
	require.NoError(t, err)
	require.NoError(t, m.Steps(1), "document 000001 (audit_events + FORCE RLS)")
	_, _ = m.Close()
	require.NoError(t, database.RunServiceMigrations(db.SuperDSN, "../../migrations", "audit"))
	_, err = db.Super.Exec(ctx, `
		GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dms_app;
		GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO dms_app;
	`)
	require.NoError(t, err)

	// Real repo + service + handler over the dms_app NOBYPASSRLS pool,
	// exactly as cmd/server wires them (Redis nil — lockTenant degrades
	// to the in-proc mutex).
	svc := service.New(service.Config{Repo: repository.New(db.App), Logger: zerolog.Nop()})
	h := handler.New(svc, zerolog.Nop())
	mux := http.NewServeMux()
	h.Register(mux)

	tenantA := uuid.Must(uuid.NewV7()).String()
	tenantB := uuid.Must(uuid.NewV7()).String()
	adminID := uuid.Must(uuid.NewV7())
	memberID := uuid.Must(uuid.NewV7())

	// Seed the hash chain the real way: svc.IngestEvent → GetLastHash
	// (the now-fixed prev-hash read) → computeHash → Insert.
	ingest := func(tenant, action string) {
		env, _ := json.Marshal(map[string]any{
			"tenantid": tenant,
			"type":     action,
			"data": map[string]any{
				"tenant_id":     tenant,
				"actor_id":      uuid.Must(uuid.NewV7()).String(),
				"resource_type": "document",
				"resource_id":   uuid.Must(uuid.NewV7()).String(),
			},
		})
		require.NoError(t, svc.IngestEvent(ctx, "dms."+action+".v1", env),
			"IngestEvent must chain-link under NOBYPASSRLS (prev-hash read was raw-pool)")
	}
	for i := 0; i < 3; i++ {
		ingest(tenantA, "document.viewed")
	}
	ingest(tenantB, "document.viewed") // tenant B's own event, for isolation

	// req builds a request with the auth context SessionAuth would set.
	req := func(method, path, tenant string, uid uuid.UUID, role string) *http.Request {
		r := httptest.NewRequest(method, path, http.NoBody)
		tid := uuid.MustParse(tenant)
		c := auth.SetTenantID(r.Context(), tid)
		c = auth.WithUser(c, auth.UserInfo{ID: uid, TenantID: tid, Role: role})
		return r.WithContext(c)
	}
	do := func(r *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		return rec
	}

	t.Run("ChainLinks_And_VerifyIntegrityValid", func(t *testing.T) {
		rec := do(req(http.MethodPost, "/api/v1/audit/verify-integrity", tenantA, adminID, "admin"))
		require.Equal(t, http.StatusOK, rec.Code)
		var res struct {
			Valid       bool `json:"valid"`
			TotalEvents int  `json:"total_events"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
		require.True(t, res.Valid, "hash chain must verify valid across the seeded events")
		require.GreaterOrEqual(t, res.TotalEvents, 3, "verify-integrity must see the >=3 chained events")
	})

	t.Run("NonAdminDenied", func(t *testing.T) {
		// read, export, and redact must all 403 for a non-admin; the
		// AdminReads_All subtest below then proves the denial landed in
		// the hash chain.
		for _, tc := range []struct{ method, path string }{
			{http.MethodGet, "/api/v1/audit/events"},
			{http.MethodGet, "/api/v1/audit/export"},
			{http.MethodPost, "/api/v1/audit/data-subject/anonymize"},
		} {
			rec := do(req(tc.method, tc.path, tenantA, memberID, "member"))
			require.Equal(t, http.StatusForbidden, rec.Code,
				"non-admin must be denied %s %s", tc.method, tc.path)
		}
	})

	t.Run("AdminReads_All", func(t *testing.T) {
		rec := do(req(http.MethodGet, "/api/v1/audit/events?page_size=100", tenantA, adminID, "admin"))
		require.Equal(t, http.StatusOK, rec.Code)
		var res struct {
			Events []map[string]any `json:"events"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
		// 3 seeded + 1 denial event from the previous subtest.
		require.GreaterOrEqual(t, len(res.Events), 4)
		var sawDenial bool
		for _, e := range res.Events {
			if a, _ := e["action"].(string); a == "audit.read.denied" {
				sawDenial = true
			}
		}
		require.True(t, sawDenial, "the non-admin denial must appear in the audit trail (issue #74)")
	})

	t.Run("TenantIsolation", func(t *testing.T) {
		rec := do(req(http.MethodGet, "/api/v1/audit/events?page_size=100", tenantB, adminID, "admin"))
		require.Equal(t, http.StatusOK, rec.Code)
		var res struct {
			Events []map[string]any `json:"events"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
		require.Len(t, res.Events, 1, "tenant B must see only its own event, never tenant A's")
	})

	t.Run("VerifyIntegrityDetectsTamper", func(t *testing.T) {
		// Superuser mutates one of tenant A's rows (bypassing the
		// append-only REVOKE + RLS). verify-integrity re-derives each
		// hash and must catch the mismatch.
		// Mutate a field that feeds computeHash (action) so the
		// re-derived hash no longer matches the stored event_hash.
		// actor_name/ip are NOT hashed, so tampering those would be
		// invisible — action is.
		_, err := db.Super.Exec(ctx, `
			UPDATE audit_events SET action = 'document.tampered'
			WHERE id = (SELECT id FROM audit_events
			            WHERE tenant_id = $1 AND action = 'document.viewed'
			            ORDER BY created_at ASC LIMIT 1)`, tenantA)
		require.NoError(t, err)

		rec := do(req(http.MethodPost, "/api/v1/audit/verify-integrity", tenantA, adminID, "admin"))
		require.Equal(t, http.StatusOK, rec.Code)
		var res struct {
			Valid    bool   `json:"valid"`
			BrokenAt string `json:"broken_at"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
		require.False(t, res.Valid, "verify-integrity must detect the tampered event")
		require.NotEmpty(t, res.BrokenAt, "the broken event id must be reported")
	})
}
