//go:build integration && prodposture
// +build integration,prodposture

package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/billing/internal/repository"
)

// The readiness probe must not read the FORCE-RLS subscriptions table on the
// raw pool: as dms_app a reused session carries an empty app.current_tenant
// after any tenant tx, and the policy's ::uuid cast then fails the probe —
// /readyz flaps to 503 on a healthy service.
func TestProdPosture_ReadyProbeOnReusedSession(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	db, _ := setupBillingSchema(ctx, t)
	cfg, err := pgxpool.ParseConfig(db.AppDSN)
	require.NoError(t, err)
	cfg.MaxConns = 1 // force the probe onto the session the tenant tx used
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	repo := repository.New(pool)

	require.NoError(t, repo.Ready(ctx), "probe on a fresh session")
	require.NoError(t, database.WithTenantTx(ctx, pool, uuid.Must(uuid.NewV7()), func(pgx.Tx) error { return nil }))
	require.NoError(t, repo.Ready(ctx), "probe on a session with tenant-GUC residue")
}
