//go:build integration && prodposture
// +build integration,prodposture

// C-5 (2026-10-12 review) — the permission loaders behind every Check
// ran their reads in a transaction opened AFTER database.WithTenant had
// set app.current_tenant in autocommit, so the GUC was already gone when
// the query ran. Under the prod NOBYPASSRLS posture permissions,
// group_members and workspace_members read 0 rows: every non-org-admin
// check denied, and the empty answer was cached in Redis.
//
// This test drives Service.Check against the real document schema as
// dms_app and requires each loader (direct grant, group grant,
// workspace-admin membership) to see its rows, the cache to hold the
// real grant, and an errored read to leave the cache untouched.
package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/policy/internal/cache"
	"github.com/aieera/sedoc/services/policy/internal/opa"
	"github.com/aieera/sedoc/services/policy/internal/repository"
)

// documentMigrations is the schema-of-record chain that owns the policy
// tables (permissions, group_members, workspace_members).
const documentMigrations = "../../../document/migrations"

func TestProdPosture_PolicyCheckSeesGrants(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	t.Cleanup(cancel)

	// Empty DB first so the dms_app pool (posture gate armed) is built
	// before the schema exists; the schema is then applied as the owner.
	db := testutil.NewProdPostureDB(ctx, t, "")
	require.NoError(t, database.RunMigrations(db.SuperDSN, documentMigrations))
	grantAppRole(ctx, t, db.Super)

	tenantA := uuid.Must(uuid.NewV7())
	tenantB := uuid.Must(uuid.NewV7())
	user := uuid.Must(uuid.NewV7())
	userB := uuid.Must(uuid.NewV7())
	group := uuid.Must(uuid.NewV7())
	ws := uuid.Must(uuid.NewV7())
	docDirect := uuid.Must(uuid.NewV7())
	docGroup := uuid.Must(uuid.NewV7())
	docInWS := uuid.Must(uuid.NewV7())

	// Seed as the owner (BYPASSRLS), the way ops/fixtures do.
	_, err := db.Super.Exec(ctx,
		`INSERT INTO organizations (id, name, slug) VALUES ($1, 'A', $2), ($3, 'B', $4)`,
		tenantA, "a-"+tenantA.String(), tenantB, "b-"+tenantB.String())
	require.NoError(t, err)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO users (tenant_id, id, email, display_name) VALUES ($1, $2, 'u@a.test', 'U')`, []any{tenantA, user}},
		{`INSERT INTO users (tenant_id, id, email, display_name) VALUES ($1, $2, 'u@b.test', 'UB')`, []any{tenantB, userB}},
		{`INSERT INTO groups (tenant_id, id, name) VALUES ($1, $2, 'g')`, []any{tenantA, group}},
		{`INSERT INTO group_members (tenant_id, group_id, user_id) VALUES ($1, $2, $3)`, []any{tenantA, group, user}},
		{`INSERT INTO workspaces (tenant_id, id, name) VALUES ($1, $2, 'w')`, []any{tenantA, ws}},
		{`INSERT INTO workspace_members (tenant_id, workspace_id, user_id, role) VALUES ($1, $2, $3, 'admin')`, []any{tenantA, ws, user}},
		{`INSERT INTO permissions (tenant_id, resource_type, resource_id, principal_type, principal_id, capability)
		  VALUES ($1, 'document', $2, 'user', $3, 'view')`, []any{tenantA, docDirect, user}},
		{`INSERT INTO permissions (tenant_id, resource_type, resource_id, principal_type, principal_id, capability)
		  VALUES ($1, 'document', $2, 'group', $3, 'view')`, []any{tenantA, docGroup, group}},
	} {
		_, err := db.Super.Exec(ctx, q.sql, q.args...)
		require.NoError(t, err, q.sql)
	}

	c, _, _ := newTestCache(t)
	eng, err := opa.New(ctx)
	require.NoError(t, err)
	svc := New(Config{
		Repos:  repository.New(db.App),
		Cache:  c,
		Engine: eng,
		Outbox: database.NewOutboxRepository(),
		Logger: zerolog.Nop(),
	})

	check := func(tenant, subject uuid.UUID, action string, doc uuid.UUID, wsID string) (bool, error) {
		ctxMap := map[string]string{}
		if wsID != "" {
			ctxMap["workspace_id"] = wsID
		}
		res, err := svc.Check(ctx, CheckInput{
			TenantID: tenant, SubjectType: "user", SubjectID: subject.String(),
			Action: action, ResourceType: "document", ResourceID: doc.String(),
			Context: ctxMap,
		})
		return res.Allowed, err
	}

	t.Run("direct user grant", func(t *testing.T) {
		ok, err := check(tenantA, user, "view", docDirect, "")
		require.NoError(t, err)
		require.True(t, ok, "a direct view grant must allow under NOBYPASSRLS")

		var cached []opa.PermissionDoc
		hit, err := c.GetJSON(ctx, cache.ResourcePermsKey(tenantA, "document", docDirect), &cached)
		require.NoError(t, err)
		require.True(t, hit, "the loaded grant must be cached")
		require.Len(t, cached, 1, "the cache must hold the real grant, not an empty RLS-filtered read")
	})

	t.Run("group grant", func(t *testing.T) {
		ok, err := check(tenantA, user, "view", docGroup, "")
		require.NoError(t, err)
		require.True(t, ok, "group_members must be visible so a group grant allows")
	})

	t.Run("workspace admin membership", func(t *testing.T) {
		ok, err := check(tenantA, user, "edit", docInWS, ws.String())
		require.NoError(t, err)
		require.True(t, ok, "workspace_members must be visible so a workspace admin is allowed")
	})

	t.Run("other tenant sees nothing", func(t *testing.T) {
		ok, err := check(tenantB, userB, "view", docDirect, "")
		require.NoError(t, err)
		require.False(t, ok, "tenant B must not see tenant A's grant")
	})

	t.Run("errored read is never cached", func(t *testing.T) {
		docErr := uuid.Must(uuid.NewV7())
		_, err := db.Super.Exec(ctx, `REVOKE SELECT ON permissions FROM dms_app`)
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = db.Super.Exec(context.Background(), `GRANT SELECT ON permissions TO dms_app`)
		})

		_, err = check(tenantA, user, "view", docErr, "")
		require.Error(t, err, "a failed permissions read must surface, not decide")

		var cached []opa.PermissionDoc
		hit, err := c.GetJSON(ctx, cache.ResourcePermsKey(tenantA, "document", docErr), &cached)
		require.NoError(t, err)
		require.False(t, hit, "a result from an errored read must never be cached")
	})
}

// grantAppRole gives dms_app the table grants the schema-of-record
// expects (scripts/init-db.sql default privileges). NewProdPostureDB ran
// its grant before the schema existed, so re-apply after migrating.
func grantAppRole(ctx context.Context, t *testing.T, super *pgxpool.Pool) {
	t.Helper()
	_, err := super.Exec(ctx, `
		GRANT USAGE ON SCHEMA public TO dms_app;
		GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dms_app;
		GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO dms_app;
	`)
	require.NoError(t, err)
}
