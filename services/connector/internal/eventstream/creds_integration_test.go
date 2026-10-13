//go:build integration && prodposture
// +build integration,prodposture

package eventstream

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

// N-11: NATS creds cannot work (no operator mode / resolver) and cannot be
// revoked, so issuance mints none and stores no nkey seed; the polling
// bearer still works.
func TestEventStreamToken_CredsNotIssued(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	db := testutil.NewProdPostureDB(ctx, t, "")
	ddl, err := os.ReadFile("../../../document/migrations/000040_tenant_event_tokens.up.sql")
	require.NoError(t, err)
	_, err = db.Super.Exec(ctx, `CREATE TABLE organizations (id UUID PRIMARY KEY, deleted_at TIMESTAMPTZ);
		GRANT SELECT ON organizations TO dms_app;`)
	require.NoError(t, err)
	_, err = db.Super.Exec(ctx, string(ddl))
	require.NoError(t, err)
	tenant := uuid.New()
	_, err = db.Super.Exec(ctx, `INSERT INTO organizations (id) VALUES ($1)`, tenant)
	require.NoError(t, err)

	s, err := New(Config{Pool: db.App, JS: &fakeJS{}, Logger: zerolog.Nop()})
	require.NoError(t, err)
	tok, err := s.IssueToken(ctx, tenant.String(), "", "receiver")
	require.NoError(t, err)
	require.NotEmpty(t, tok.BearerToken, "polling bearer still issued")
	require.Empty(t, tok.NATSCredsFile, "no NATS creds file")
	require.Empty(t, tok.NATSAccountID)
	require.NotEmpty(t, tok.NATSCredsStatus, "the response explains why there are no creds")

	var seed, jwtStr *string
	require.NoError(t, database.WithTenantTx(ctx, db.App, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT nats_user_nkey_seed, nats_user_jwt FROM tenant_event_tokens WHERE tenant_id = $1 AND id = $2`,
			tenant, tok.ID).Scan(&seed, &jwtStr)
	}))
	require.Nil(t, seed, "no nkey seed stored")
	require.Nil(t, jwtStr)

	got, err := s.LookupTokenTenant(ctx, tok.BearerToken)
	require.NoError(t, err)
	require.Equal(t, tenant.String(), got)
}
