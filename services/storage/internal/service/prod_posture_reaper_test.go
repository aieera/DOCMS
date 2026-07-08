//go:build integration && prodposture
// +build integration,prodposture

// Wave A.1 (issue #76) — the BlobReaper end to end under prod
// NOBYPASSRLS. The reaper's list phase used `SET LOCAL row_security =
// off`, which ERRORS under the dms_app role, so the hourly reaper died
// every cycle and orphaned blobs accumulated forever. This drives the
// REAL reaper cycle against a dms_app pool + MinIO: it must complete N
// cycles cleanly and ACTUALLY delete an orphaned blob (both the S3
// object and the content_blobs row), while a still-referenced blob
// survives. In-package so it can call the unexported reap().
package service

import (
	"bytes"
	"context"
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

const reaperFixtureDDL = `
CREATE TABLE organizations (
	id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	deleted_at TIMESTAMPTZ
);
CREATE TABLE content_blobs (
	id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	tenant_id         UUID NOT NULL REFERENCES organizations(id),
	sha256_hash       TEXT NOT NULL,
	storage_region    TEXT NOT NULL DEFAULT 'us-east-1',
	storage_bucket    TEXT NOT NULL,
	storage_key       TEXT NOT NULL,
	storage_class     TEXT NOT NULL DEFAULT 'hot'
	                       CHECK (storage_class IN ('hot','warm','cold','quarantine')),
	size_bytes        BIGINT NOT NULL,
	mime_type         TEXT,
	encryption_key_id TEXT,
	reference_count   INT NOT NULL DEFAULT 1,
	created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
	encrypted_dek     BYTEA,
	dek_nonce         BYTEA,
	kek_id            TEXT,
	UNIQUE (tenant_id, sha256_hash)
);
CREATE INDEX idx_content_blobs_refcount_zero ON content_blobs(tenant_id) WHERE reference_count = 0;
ALTER TABLE content_blobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE content_blobs FORCE  ROW LEVEL SECURITY;
CREATE POLICY content_blobs_tenant_isolation ON content_blobs
	USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY content_blobs_tenant_isolation_insert ON content_blobs
	FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
GRANT SELECT, INSERT, UPDATE, DELETE ON content_blobs TO dms_app;
GRANT SELECT ON organizations TO dms_app;
`

func TestProdPosture_BlobReaperCycles(t *testing.T) {
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

	tenantA := uuid.Must(uuid.NewV7())
	tenantB := uuid.Must(uuid.NewV7())
	_, err = db.Super.Exec(ctx, `INSERT INTO organizations (id) VALUES ($1), ($2)`, tenantA, tenantB)
	require.NoError(t, err)

	repos := repository.New(db.App)

	// One reap-eligible orphan for tenant A (ref=0, 48h old) with a real
	// S3 object, and one still-referenced blob for tenant B that must
	// survive — proving the reaper deletes only what it should, per
	// tenant, under NOBYPASSRLS.
	seedBlob := func(tenant uuid.UUID, key string, refCount int, age time.Duration) *model.ContentBlob {
		require.NoError(t, s3.PutObject(ctx, bucket, key, bytes.NewReader([]byte("payload")), 7, "application/octet-stream"))
		b := &model.ContentBlob{
			ID: uuid.Must(uuid.NewV7()), TenantID: tenant,
			SHA256Hash: uuid.Must(uuid.NewV7()).String(), StorageRegion: "us-east-1",
			StorageBucket: bucket, StorageKey: key, StorageClass: "hot",
			SizeBytes: 7, EncryptedDEK: []byte("dek"), DEKNonce: []byte("nonce1234567"),
			KEKID: "kek-1", ReferenceCount: refCount, CreatedAt: time.Now().Add(-age).UTC(),
		}
		require.NoError(t, database.WithTenantTx(ctx, db.App, tenant, func(tx pgx.Tx) error {
			return repos.ContentBlobs.Insert(ctx, tx, b)
		}))
		return b
	}
	orphan := seedBlob(tenantA, "blobs/a/orphan", 0, 48*time.Hour)
	live := seedBlob(tenantB, "blobs/b/live", 1, 48*time.Hour)

	reaper := NewBlobReaper(db.App, repos, s3, zerolog.Nop())

	// Run N cycles. The first deletes the orphan; the rest are clean
	// no-ops (nothing left to reap) — proving the reaper survives repeated
	// cycles under NOBYPASSRLS instead of dying on row_security=off.
	const cycles = 3
	for i := 0; i < cycles; i++ {
		reaper.reap(ctx)
	}

	// The orphan's S3 object is gone.
	_, err = s3.GetObjectInfo(ctx, bucket, orphan.StorageKey)
	require.Error(t, err, "reaper must delete the orphaned S3 object")

	// The orphan's row is gone (superuser sees through RLS).
	var orphanRows int
	require.NoError(t, db.Super.QueryRow(ctx,
		`SELECT count(*) FROM content_blobs WHERE id = $1`, orphan.ID).Scan(&orphanRows))
	require.Zero(t, orphanRows, "reaper must hard-delete the orphaned content_blobs row")

	// The still-referenced blob (other tenant) survives — object + row.
	_, err = s3.GetObjectInfo(ctx, bucket, live.StorageKey)
	require.NoError(t, err, "a still-referenced blob must NOT be reaped")
	var liveRows int
	require.NoError(t, db.Super.QueryRow(ctx,
		`SELECT count(*) FROM content_blobs WHERE id = $1`, live.ID).Scan(&liveRows))
	require.Equal(t, 1, liveRows, "referenced blob row must survive")
}
