//go:build integration && prodposture
// +build integration,prodposture

// H-4: the connector ingest path acts with the folder/config creator's
// CURRENT role, read from users under the tenant's RLS context. This pins
// the lookup under the prod NOBYPASSRLS dms_app role: the creator's role is
// visible in their own tenant, invisible from another tenant, and a
// suspended or deleted creator has no role at all.
package ingest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/testutil"
)

func TestProdPosture_IngestCreatorRoleLookup(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)

	db := testutil.NewProdPostureDB(ctx, t, "")
	_, err := db.Super.Exec(ctx, `
		CREATE TABLE organizations (id UUID PRIMARY KEY, deleted_at TIMESTAMPTZ);
		CREATE TABLE users (
			tenant_id  UUID NOT NULL REFERENCES organizations(id),
			id         UUID NOT NULL DEFAULT gen_random_uuid(),
			role       TEXT NOT NULL DEFAULT 'member',
			status     TEXT NOT NULL DEFAULT 'active',
			deleted_at TIMESTAMPTZ,
			PRIMARY KEY (tenant_id, id)
		);
		ALTER TABLE users ENABLE ROW LEVEL SECURITY;
		ALTER TABLE users FORCE ROW LEVEL SECURITY;
		CREATE POLICY users_tenant_isolation ON users
			USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
		GRANT SELECT ON organizations, users TO dms_app;
	`)
	require.NoError(t, err)

	tenantA, tenantB := uuid.New(), uuid.New()
	member, suspended, deleted := uuid.New(), uuid.New(), uuid.New()
	_, err = db.Super.Exec(ctx, `INSERT INTO organizations (id) VALUES ($1), ($2)`, tenantA, tenantB)
	require.NoError(t, err)
	_, err = db.Super.Exec(ctx, `
		INSERT INTO users (tenant_id, id, role, status, deleted_at) VALUES
		  ($1, $2, 'member', 'active', NULL),
		  ($1, $3, 'admin',  'suspended', NULL),
		  ($1, $4, 'admin',  'active', now())`,
		tenantA, member, suspended, deleted)
	require.NoError(t, err)

	c := &Client{pool: db.App}
	role, err := c.lookupRole(ctx, tenantA.String(), member.String())
	require.NoError(t, err)
	require.Equal(t, "member", role, "creator's real role under NOBYPASSRLS")

	for _, uid := range []uuid.UUID{suspended, deleted} {
		_, err := c.lookupRole(ctx, tenantA.String(), uid.String())
		require.True(t, errors.Is(err, ErrNoActiveActor), "inactive creator must have no role: %v", err)
	}
	// Cross-tenant: tenant B's context cannot see tenant A's user.
	_, err = c.lookupRole(ctx, tenantB.String(), member.String())
	require.True(t, errors.Is(err, ErrNoActiveActor), "cross-tenant lookup must fail closed: %v", err)
}
