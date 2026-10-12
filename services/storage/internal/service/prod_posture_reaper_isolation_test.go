//go:build integration && prodposture
// +build integration,prodposture

// D-21 — one tenant's failure must not abort the whole reaper cycle.
//
// The reaper listed zero-ref blobs through database.ForEachTenant, which
// stops at the first tenant whose transaction errors and discards every
// result gathered so far: a single tenant with a broken row, a policy
// error or a lock timeout stopped all orphan reclamation for every tenant,
// every hour. The reaper now walks the tenant list itself, logs a failing
// tenant and carries on.
package service

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/storage"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/storage/internal/model"
	"github.com/aieera/sedoc/services/storage/internal/repository"
)

func TestReaper_TenantErrorDoesNotAbort(t *testing.T) {
	testutil.AssertProdPosture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)

	db := testutil.NewProdPostureDB(ctx, t, "")
	_, err := db.Super.Exec(ctx, reaperFixtureDDL)
	require.NoError(t, err)

	endpoint, access, secret, mcleanup, err := testutil.NewMinIOContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(mcleanup)
	s3, err := storage.NewS3Client(endpoint, access, secret, false)
	require.NoError(t, err)
	const bucket = "sedoc-tenant-blobs"
	require.NoError(t, s3.CreateBucket(ctx, bucket, "us-east-1"))

	// Tenant A is enumerated first and every read of its blobs errors
	// (a restrictive RLS policy that raises inside A's own tenant context
	// only); tenant B is healthy.
	tenantA, tenantB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	_, err = db.Super.Exec(ctx, `INSERT INTO organizations (id) VALUES ($1)`, tenantA)
	require.NoError(t, err)
	_, err = db.Super.Exec(ctx, `INSERT INTO organizations (id) VALUES ($1)`, tenantB)
	require.NoError(t, err)
	_, err = db.Super.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION reaper_fault(t uuid) RETURNS boolean LANGUAGE plpgsql AS $$
		BEGIN
			IF t = '%[1]s' AND current_setting('app.current_tenant', true) = '%[1]s' THEN
				RAISE EXCEPTION 'injected storage fault for tenant %%', t;
			END IF;
			RETURN true;
		END $$;
		CREATE POLICY content_blobs_reaper_fault ON content_blobs AS RESTRICTIVE
			USING (reaper_fault(tenant_id));`, tenantA))
	require.NoError(t, err)

	repos := repository.New(db.App)
	seed := func(tenant uuid.UUID, key string) *model.ContentBlob {
		require.NoError(t, s3.PutObject(ctx, bucket, key, bytes.NewReader([]byte("payload")), 7, "application/octet-stream"))
		b := &model.ContentBlob{
			ID: uuid.Must(uuid.NewV7()), TenantID: tenant,
			SHA256Hash: uuid.Must(uuid.NewV7()).String(), StorageRegion: "us-east-1",
			StorageBucket: bucket, StorageKey: key, StorageClass: "hot", SizeBytes: 7,
			ReferenceCount: 0, CreatedAt: time.Now().Add(-48 * time.Hour).UTC(),
		}
		_, err := db.Super.Exec(ctx, `
			INSERT INTO content_blobs (id, tenant_id, sha256_hash, storage_region, storage_bucket,
				storage_key, storage_class, size_bytes, reference_count, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			b.ID, b.TenantID, b.SHA256Hash, b.StorageRegion, b.StorageBucket, b.StorageKey,
			b.StorageClass, b.SizeBytes, b.ReferenceCount, b.CreatedAt)
		require.NoError(t, err)
		return b
	}
	orphanA := seed(tenantA, "blobs/a/orphan")
	orphanB := seed(tenantB, "blobs/b/orphan")

	// Sanity: tenant A really is broken under its own context.
	err = database.WithTenantTx(ctx, db.App, tenantA, func(tx pgx.Tx) error {
		_, gerr := repos.ContentBlobs.GetByID(ctx, tx, tenantA, orphanA.ID)
		return gerr
	})
	require.ErrorContains(t, err, "injected storage fault")

	NewBlobReaper(db.App, repos, s3, zerolog.Nop()).reap(ctx)

	_, err = s3.GetObjectInfo(ctx, bucket, orphanB.StorageKey)
	require.Error(t, err, "tenant B's orphan is reaped despite tenant A failing")
	var rows int
	require.NoError(t, db.Super.QueryRow(ctx, `SELECT count(*) FROM content_blobs WHERE id = $1`, orphanB.ID).Scan(&rows))
	require.Zero(t, rows)

	// Tenant A is left exactly as it was, for the next cycle.
	_, err = s3.GetObjectInfo(ctx, bucket, orphanA.StorageKey)
	require.NoError(t, err)
	require.NoError(t, db.Super.QueryRow(ctx, `SELECT count(*) FROM content_blobs WHERE id = $1`, orphanA.ID).Scan(&rows))
	require.Equal(t, 1, rows)
}
