//go:build integration && prodposture
// +build integration,prodposture

// Wave A.1 (issue #71) — event-stream bearer auth under prod
// NOBYPASSRLS. LookupTokenTenant is a PRE-tenant lookup (the token IS
// how the tenant is learned) on the FORCE-RLS tenant_event_tokens
// table: the raw-pool version failed closed in prod and event-stream
// auth was silently dead. The fix probes each tenant from the
// organizations registry under that tenant's context; this test pins
// the round-trip and that tokens never resolve across tenants.
//
// In-package so it can use hashBearer and construct the Service without
// the JetStream half (LookupTokenTenant never touches NATS).
package eventstream

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

func TestProdPosture_ConnectorEventStreamToken(t *testing.T) {
	testutil.AssertProdPosture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)

	db := testutil.NewProdPostureDB(ctx, t, "")
	_, err := db.Super.Exec(ctx, `
		CREATE TABLE organizations (
			id         UUID PRIMARY KEY,
			deleted_at TIMESTAMPTZ
		);
		-- Minimal shape of document 000040's tenant_event_tokens: just
		-- the columns LookupTokenTenant touches, with the load-bearing
		-- FORCE RLS + tenant policy.
		CREATE TABLE tenant_event_tokens (
			tenant_id    UUID NOT NULL REFERENCES organizations(id),
			id           UUID NOT NULL DEFAULT gen_random_uuid(),
			token_hash   TEXT NOT NULL,
			revoked_at   TIMESTAMPTZ,
			expires_at   TIMESTAMPTZ,
			last_used_at TIMESTAMPTZ,
			created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (tenant_id, id)
		);
		ALTER TABLE tenant_event_tokens ENABLE ROW LEVEL SECURITY;
		ALTER TABLE tenant_event_tokens FORCE  ROW LEVEL SECURITY;
		CREATE POLICY tet_tenant_isolation ON tenant_event_tokens
			USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
			WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
		GRANT SELECT, INSERT, UPDATE, DELETE ON organizations, tenant_event_tokens TO dms_app;
	`)
	require.NoError(t, err)

	tenantA := uuid.Must(uuid.NewV7())
	tenantB := uuid.Must(uuid.NewV7())
	_, err = db.Super.Exec(ctx, `INSERT INTO organizations (id) VALUES ($1), ($2)`, tenantA, tenantB)
	require.NoError(t, err)

	bearerA := "evt_" + uuid.Must(uuid.NewV7()).String()
	seed := func(tenant uuid.UUID, bearer string) {
		require.NoError(t, database.WithTenantTx(ctx, db.App, tenant, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx,
				`INSERT INTO tenant_event_tokens (tenant_id, token_hash) VALUES ($1, $2)`,
				tenant, hashBearer(bearer))
			return err
		}))
	}
	seed(tenantA, bearerA)
	seed(tenantB, "evt_"+uuid.Must(uuid.NewV7()).String())

	s := &Service{pool: db.App, log: zerolog.Nop()}

	got, err := s.LookupTokenTenant(ctx, bearerA)
	require.NoError(t, err)
	require.Equal(t, tenantA.String(), got,
		"bearer must resolve to its issuing tenant under NOBYPASSRLS (pre-tenant iteration)")

	got, err = s.LookupTokenTenant(ctx, "evt_unknown-token")
	require.NoError(t, err)
	require.Empty(t, got, "unknown bearer must resolve to no tenant")
}
