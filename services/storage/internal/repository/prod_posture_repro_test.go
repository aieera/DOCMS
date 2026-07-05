//go:build integration && prodposture
// +build integration,prodposture

// Prod-posture repro: storage BlobReaper dies every cycle under the
// production NOBYPASSRLS role.
//
// BUG (audit-verified, STATE_OF_THE_PROJECT 2026-07-03 "storage" row):
// contentBlobRepo.ListZeroRefOlderThan (content_blobs.go:104) runs
// `SET LOCAL row_security = off` for its cross-tenant zero-ref sweep.
// row_security is a USERSET GUC, so the SET itself succeeds even for
// dms_app — but the prod app role is NOBYPASSRLS and does not own the
// FORCE-RLS table (helm postInit / migration 000065), so the very next
// query fails with SQLSTATE 42501: `ERROR: query would be affected by
// row-level security policy for table "content_blobs"`. The hourly
// BlobReaper (services/storage/internal/service/reaper.go:78) logs the
// error and reaps nothing — orphaned ciphertext accumulates forever. Dev
// and the default integration lane mask this by connecting as the
// testcontainer superuser (BYPASSRLS).
//
// The SAME pattern exists in services/document/internal/janitor/
// orphan_gc.go:140 and needs the same fix.
//
// Tracked: https://github.com/aieera/DOCMS/issues/76
// Allow-listed in ci/prod-posture-allowlist.txt until the Wave A fix
// lands; the assertions below encode the correct POST-fix behavior (no
// error, seeded orphan returned), so today this test FAILS — that is the
// point of the prod-posture lane.
//
// Fixture note: services/storage has no migrations/ directory — the
// storage service runs against the shared document-DB schema, whose
// content_blobs DDL lives in services/document/migrations/000001 (lines
// 252-276: ENABLE + FORCE ROW LEVEL SECURITY + tenant policies) and
// 000003 (envelope columns). That migration chain is currently broken on
// a clean DB by 000021 (STATE 2026-07-03 "Migration blocker"), so — like
// pkg/database/prod_posture_demo_test.go — this test recreates the exact
// schema-of-record DDL itself instead of passing a migrationsDir.
package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/storage/internal/model"
	"github.com/aieera/sedoc/services/storage/internal/repository"
)

func TestProdPosture_StorageBlobReaper(t *testing.T) {
	testutil.AssertProdPosture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	// No migrationsDir — see fixture note in the header comment.
	db := testutil.NewProdPostureDB(ctx, t, "")

	// Exact schema-of-record shape for content_blobs:
	// services/document/migrations/000001_initial_schema.up.sql:252-276
	// (table + FORCE RLS + tenant policies) plus the 000003 envelope
	// columns the repo scans. organizations is reduced to the FK target.
	_, err := db.Super.Exec(ctx, `
		CREATE TABLE organizations (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid()
		);
		CREATE TABLE content_blobs (
			id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			tenant_id         UUID NOT NULL REFERENCES organizations(id),
			sha256_hash       TEXT NOT NULL,
			storage_region    TEXT NOT NULL DEFAULT 'us-east-1',
			storage_bucket    TEXT NOT NULL,
			storage_key       TEXT NOT NULL,
			storage_class     TEXT NOT NULL DEFAULT 'hot'
			                       CHECK (storage_class IN ('hot', 'warm', 'cold', 'quarantine')),
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
		CREATE INDEX idx_content_blobs_refcount_zero
			ON content_blobs(tenant_id) WHERE reference_count = 0;
		ALTER TABLE content_blobs ENABLE ROW LEVEL SECURITY;
		ALTER TABLE content_blobs FORCE  ROW LEVEL SECURITY;
		CREATE POLICY content_blobs_tenant_isolation ON content_blobs
			USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
		CREATE POLICY content_blobs_tenant_isolation_insert ON content_blobs
			FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
		GRANT SELECT, INSERT, UPDATE, DELETE ON content_blobs TO dms_app;
	`)
	require.NoError(t, err, "content_blobs fixture DDL")

	tenant := uuid.Must(uuid.NewV7())
	_, err = db.Super.Exec(ctx, `INSERT INTO organizations (id) VALUES ($1)`, tenant)
	require.NoError(t, err, "seed organization")

	// The REAL repository over the dms_app pool, exactly as production
	// wires it (cmd/server -> repository.New(pool) -> BlobReaper).
	repos := repository.New(db.App)

	// Seed one reap-eligible orphan: reference_count 0, created 48h ago
	// (well past the reaper's 24h grace). Tenant-correct write via
	// WithTenantTx through the repo's own Insert.
	orphan := &model.ContentBlob{
		ID:             uuid.Must(uuid.NewV7()),
		TenantID:       tenant,
		SHA256Hash:     "0e5751c026e543b2e8ab2eb06099daa1d1e5df47778f7787faab45cdf12fe3a8",
		StorageRegion:  "us-east-1",
		StorageBucket:  "sedoc-tenant-blobs",
		StorageKey:     "blobs/" + tenant.String() + "/orphan",
		StorageClass:   "hot",
		SizeBytes:      42,
		MimeType:       "application/pdf",
		EncryptedDEK:   []byte("wrapped-dek"),
		DEKNonce:       []byte("nonce-123456"),
		KEKID:          "kek-test-1",
		ReferenceCount: 0,
		CreatedAt:      time.Now().Add(-48 * time.Hour).UTC(),
	}
	require.NoError(t, database.WithTenantTx(ctx, db.App, tenant, func(tx pgx.Tx) error {
		return repos.ContentBlobs.Insert(ctx, tx, orphan)
	}), "seed zero-ref blob")

	// Sanity: the row is really there under its tenant context, so the
	// only assertion that can fail below is the reaper query itself.
	require.NoError(t, database.WithTenantTx(ctx, db.App, tenant, func(tx pgx.Tx) error {
		_, err := repos.ContentBlobs.GetByID(ctx, tx, tenant, orphan.ID)
		return err
	}), "seeded blob must be readable in tenant context")

	// The reaper's exact call (reaper.go:78): cross-tenant list of
	// zero-ref blobs older than the grace cutoff, on the app pool.
	//
	// POST-FIX contract: no error, and the seeded orphan is returned.
	// TODAY: after `SET LOCAL row_security = off`, the select fails for
	// dms_app (NOBYPASSRLS, not table owner) with SQLSTATE 42501 "query
	// would be affected by row-level security policy for table
	// content_blobs" and the reaper dies every cycle.
	cutoff := time.Now().Add(-24 * time.Hour)
	blobs, err := repos.ContentBlobs.ListZeroRefOlderThan(ctx, db.App, cutoff, 100)
	require.NoError(t, err,
		"BlobReaper sweep must work under the prod NOBYPASSRLS role; "+
			"`SET LOCAL row_security = off` has no lawful effect for dms_app "+
			"(content_blobs.go:104, issues/76)")
	require.Len(t, blobs, 1, "sweep must return the seeded zero-ref orphan")
	require.Equal(t, orphan.ID, blobs[0].ID)
	require.Equal(t, tenant, blobs[0].TenantID)
}
