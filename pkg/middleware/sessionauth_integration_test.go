//go:build integration
// +build integration

// Real-database tests for the shared identity middleware (SessionAuth,
// SessionAuthOptional, APIKeyAuth) under the PRODUCTION posture: the
// middleware runs on a dms_app NOBYPASSRLS pool against the real schema of
// record (document migration 000001 — sessions, users, api_keys and
// group_members are all ENABLE + FORCE RLS) plus the real auth migration
// chain (the SECURITY DEFINER pre-tenant lookups, incl. 000003
// auth_session_identity). C-3: before the fix these middlewares read the
// FORCE-RLS tables on the raw pool, so under dms_app every session was 401
// and every API key "invalid".
//
// Run:
//
//	go test -tags integration -run 'TestSessionAuth_|TestAPIKeyAuth_' ./middleware/
//	go test -tags "integration prodposture" -run '^TestProdPosture_' ./middleware/
package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

type identityFixture struct {
	db     *testutil.ProdPostureDB
	tenant uuid.UUID
	ctx    context.Context
}

func repoRootForTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("go.work not found above test dir")
	return ""
}

// newIdentityFixture boots a prod-posture Postgres with the real schema:
// document 000001 (applied verbatim, as scripts/migrate-all.sh does first
// on a fresh database) and the full auth migration track.
func newIdentityFixture(t *testing.T) *identityFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	db := testutil.NewProdPostureDB(ctx, t, "")
	root := repoRootForTest(t)

	ddl, err := os.ReadFile(filepath.Join(root, "services", "document", "migrations", "000001_initial_schema.up.sql"))
	require.NoError(t, err)
	_, err = db.Super.Exec(ctx, string(ddl))
	require.NoError(t, err, "document 000001 (schema of record)")
	require.NoError(t, database.RunServiceMigrations(db.SuperDSN,
		filepath.Join(root, "services", "auth", "migrations"), "auth"), "auth migrations")
	_, err = db.Super.Exec(ctx, `
		GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dms_app;
		GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO dms_app;`)
	require.NoError(t, err)

	tenant := uuid.Must(uuid.NewV7())
	_, err = db.Super.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1, 'Org', $2)`,
		tenant, "org-"+tenant.String()[:8])
	require.NoError(t, err)
	return &identityFixture{db: db, tenant: tenant, ctx: ctx}
}

// seedUser inserts a user (as the superuser, like provisioning tooling).
func (f *identityFixture) seedUser(t *testing.T, email, role, status string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	_, err := f.db.Super.Exec(f.ctx,
		`INSERT INTO users (tenant_id, id, email, display_name, role, status) VALUES ($1,$2,$3,$3,$4,$5)`,
		f.tenant, id, email, role, status)
	require.NoError(t, err)
	return id
}

// seedSession creates a live session for user and returns its plaintext token.
func (f *identityFixture) seedSession(t *testing.T, user uuid.UUID) string {
	t.Helper()
	token := "sess-" + uuid.NewString()
	sum := sha256.Sum256([]byte(token))
	_, err := f.db.Super.Exec(f.ctx,
		`INSERT INTO sessions (tenant_id, user_id, token_hash, expires_at) VALUES ($1,$2,$3, now() + interval '1 hour')`,
		f.tenant, user, hex.EncodeToString(sum[:]))
	require.NoError(t, err)
	return token
}

// seedAPIKey creates an API key for user and returns its plaintext token.
func (f *identityFixture) seedAPIKey(t *testing.T, user uuid.UUID, scopes []string, revoked, expired bool) (string, uuid.UUID) {
	t.Helper()
	token := "vdms_" + uuid.NewString()
	sum := sha256.Sum256([]byte(token))
	id := uuid.Must(uuid.NewV7())
	var revokedAt, expiresAt any
	if revoked {
		revokedAt = time.Now().Add(-time.Minute)
	}
	if expired {
		expiresAt = time.Now().Add(-time.Minute)
	}
	_, err := f.db.Super.Exec(f.ctx,
		`INSERT INTO api_keys (tenant_id, id, user_id, name, key_hash, key_prefix, scopes, revoked_at, expires_at)
		 VALUES ($1,$2,$3,'k',$4,$5,$6,$7,$8)`,
		f.tenant, id, user, hex.EncodeToString(sum[:]), token[:12], scopes, revokedAt, expiresAt)
	require.NoError(t, err)
	return token, id
}

// capture runs mw around a handler that records the identity it sees.
func capture(mw func(http.Handler) http.Handler, req *http.Request) (int, *auth.UserInfo) {
	var seen *auth.UserInfo
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, err := auth.User(r.Context()); err == nil {
			seen = &u
		}
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr.Code, seen
}

func sessionReq(token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.AddCookie(&http.Cookie{Name: "dms_session", Value: token})
	return r
}

// TestSessionAuth_SuspendedUser401 pins M-8: a suspended, deactivated or
// deleted user's still-unexpired session must not authenticate on the
// shared middleware — auth_session_identity returns rows for active users
// only. Run on both the prod (dms_app) and the dev-like superuser pool:
// the status check must not depend on RLS. An active user's session is
// the positive control so the 401s are not vacuous.
func TestSessionAuth_SuspendedUser401(t *testing.T) {
	f := newIdentityFixture(t)
	active := f.seedUser(t, "active@example.com", "member", "active")
	suspended := f.seedUser(t, "suspended@example.com", "admin", "suspended")
	deactivated := f.seedUser(t, "gone@example.com", "member", "deactivated")
	deleted := f.seedUser(t, "deleted@example.com", "member", "active")
	_, err := f.db.Super.Exec(f.ctx, `UPDATE users SET deleted_at = now() WHERE tenant_id = $1 AND id = $2`, f.tenant, deleted)
	require.NoError(t, err)
	activeToken := f.seedSession(t, active)
	blocked := map[string]string{
		"suspended":   f.seedSession(t, suspended),
		"deactivated": f.seedSession(t, deactivated),
		"deleted":     f.seedSession(t, deleted),
	}

	for poolName, pool := range map[string]*pgxpool.Pool{"dms_app": f.db.App, "superuser": f.db.Super} {
		required := SessionAuth(SessionAuthConfig{Pool: pool})
		optional := SessionAuthOptional(SessionAuthConfig{Pool: pool})
		t.Run(poolName+"/active", func(t *testing.T) {
			code, seen := capture(required, sessionReq(activeToken))
			require.Equal(t, http.StatusOK, code, "active user must authenticate")
			require.NotNil(t, seen)
		})
		for name, token := range blocked {
			t.Run(poolName+"/"+name, func(t *testing.T) {
				code, _ := capture(required, sessionReq(token))
				require.Equal(t, http.StatusUnauthorized, code, "SessionAuth must 401 a %s user", name)
				code, seen := capture(optional, sessionReq(token))
				require.Equal(t, http.StatusOK, code)
				require.Nil(t, seen, "SessionAuthOptional must not bind a %s user's identity", name)
			})
		}
	}
}

// mustSeedGroup creates a group in the fixture tenant with user as member.
func mustSeedGroup(t *testing.T, f *identityFixture, user uuid.UUID) uuid.UUID {
	t.Helper()
	g := uuid.Must(uuid.NewV7())
	_, err := f.db.Super.Exec(f.ctx, `INSERT INTO groups (tenant_id, id, name) VALUES ($1,$2,'eng')`, f.tenant, g)
	require.NoError(t, err)
	_, err = f.db.Super.Exec(f.ctx, `INSERT INTO group_members (tenant_id, group_id, user_id) VALUES ($1,$2,$3)`, f.tenant, g, user)
	require.NoError(t, err)
	return g
}

// TestAPIKeyAuth_TouchSurvivesRequestEnd pins R-4: the last_used_at touch
// used to run in a goroutine on r.Context(), which is cancelled the
// moment the response is written — so it was usually lost (and under
// NOBYPASSRLS it updated zero rows anyway, outside a tenant tx). It must
// land even when the request context is cancelled right after the
// handler returns, and it is throttled to once per minute per key.
func TestAPIKeyAuth_TouchSurvivesRequestEnd(t *testing.T) {
	f := newIdentityFixture(t)
	user := f.seedUser(t, "touch@example.com", "member", "active")
	token, keyID := f.seedAPIKey(t, user, []string{"documents:read"}, false, false)
	mw := APIKeyAuth(APIKeyAuthConfig{Pool: f.db.App, RequiredScope: "documents:read"})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	serve := func() {
		ctx, cancel := context.WithCancel(context.Background())
		r := httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(ctx)
		r.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, r)
		cancel() // the request is over: net/http cancels its context here
		require.Equal(t, http.StatusOK, rr.Code)
	}
	lastUsed := func() *time.Time {
		var ts *time.Time
		require.NoError(t, f.db.Super.QueryRow(f.ctx,
			`SELECT last_used_at FROM api_keys WHERE tenant_id = $1 AND id = $2`, f.tenant, keyID).Scan(&ts))
		return ts
	}

	serve()
	var first *time.Time
	require.Eventually(t, func() bool { first = lastUsed(); return first != nil },
		5*time.Second, 50*time.Millisecond, "last_used_at must be written after the request context is cancelled")

	serve()
	time.Sleep(500 * time.Millisecond)
	second := lastUsed()
	require.NotNil(t, second)
	require.True(t, second.Equal(*first), "touch is throttled to once per minute per key (got %v then %v)", first, second)
}
