//go:build integration && prodposture

// Prod-posture coverage for the signed-blob id lookup in the gRPC ingest
// client. content_blobs is FORCE RLS; the lookup ran on the raw pool, so
// under the production dms_app role every signed-PDF hand-off failed with
// "resolve blob id: no rows".
package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/signature/internal/repository/repotest"
)

func TestProdPosture_SignedBlobIDLookup(t *testing.T) {
	testutil.AssertProdPosture(t)
	db := repotest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	a := repotest.SeedTenant(ctx, t, db.Super)
	b := repotest.SeedTenant(ctx, t, db.Super)
	blob := uuid.Must(uuid.NewV7())
	_, err := db.Super.Exec(ctx, `
		INSERT INTO content_blobs (id, tenant_id, sha256_hash, storage_bucket, storage_key, size_bytes)
		VALUES ($1, $2, 'deadbeef', 'b', 'k', 1)`, blob, a.ID)
	require.NoError(t, err)

	c := &GRPCIngestClient{pool: db.App}
	got, err := c.lookupBlobID(ctx, a.ID, "deadbeef")
	require.NoError(t, err)
	require.Equal(t, blob.String(), got)

	_, err = c.lookupBlobID(ctx, b.ID, "deadbeef")
	require.Error(t, err, "another tenant must not resolve the blob")
}
