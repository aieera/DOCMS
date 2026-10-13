//go:build integration
// +build integration

// IRM RLS policy — the license-check callback is cross-tenant-by-token, and
// migration 000089 made irm_containers / irm_licenses readable by ANY query
// that runs without a tenant context (USING allowed current_setting(...) IS
// NULL OR = ”). Under the NOBYPASSRLS app role that is a fail-OPEN policy:
// a raw-pool query, or a pooled connection carrying ” GUC residue after a
// WithTenantTx, reads every tenant's sealed-container coordinates and
// wrapped DEKs. Migration 000109 replaces it with a strict policy and moves
// the token lookup to a SECURITY DEFINER function.
//
// Uses the prod-posture pool (dms_app NOBYPASSRLS) — the superuser would
// bypass RLS and prove nothing.
//
// Run:
//
//	go test -tags integration -run '^TestIRMPolicy_EmptyGUCSeesNothing$' ./internal/service/
package service_test

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

// irmFixture is one sealed container with one email-bound license.
type irmFixture struct {
	tenant, user, doc, version, container, license uuid.UUID
	token                                          string
}

// seedIRMLicense seeds org → user → workspace/folder/doc → version → IRM
// container + license as the superuser (out-of-band, like ops tooling).
func seedIRMLicense(ctx context.Context, t *testing.T, super *pgxpool.Pool) irmFixture {
	t.Helper()
	f := irmFixture{
		// v4 tenant: seedOrg derives the org slug from the first 8 hex chars,
		// which collide for v7 ids minted in the same millisecond.
		tenant: uuid.New(), user: uuid.Must(uuid.NewV7()),
		doc: uuid.Must(uuid.NewV7()), version: uuid.Must(uuid.NewV7()),
		container: uuid.Must(uuid.NewV7()), license: uuid.Must(uuid.NewV7()),
		token: "irm-token-" + uuid.NewString(),
	}
	ws, folder, blob := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	seedOrg(ctx, t, super, f.tenant)
	seedUser(ctx, t, super, f.tenant, f.user)
	seedWorkspaceFolderDoc(ctx, t, super, f.tenant, ws, folder, f.doc)
	seedContentBlob(ctx, t, super, f.tenant, blob, "irm-bucket", "k/"+blob.String(), 10, "application/pdf")
	_, err := super.Exec(ctx, `
		INSERT INTO document_versions
			(tenant_id, id, document_id, version_number, content_blob_id, size_bytes, mime_type, sha256_hash)
		VALUES ($1, $2, $3, 1, $4, 10, 'application/pdf', $5)`,
		f.tenant, f.version, f.doc, blob, "sha-"+blob.String()[:8])
	require.NoError(t, err)
	_, err = super.Exec(ctx, `
		INSERT INTO irm_containers
			(tenant_id, id, document_id, version_id, sealed_bucket, sealed_key, payload_nonce,
			 payload_sha256, mime, title, expires_at, created_by)
		VALUES ($1, $2, $3, $4, 'irm-bucket', $5, '\x00'::bytea, '\x00'::bytea,
		        'application/pdf', 'Sealed', now() + interval '1 day', $6)`,
		f.tenant, f.container, f.doc, f.version, f.tenant.String()+"/irm/"+f.container.String(), f.user)
	require.NoError(t, err)
	th := sha256.Sum256([]byte(f.token))
	_, err = super.Exec(ctx, `
		INSERT INTO irm_licenses
			(tenant_id, id, container_id, recipient_type, recipient_ref, token_hash,
			 wrapped_dek, key_ref, created_by)
		VALUES ($1, $2, $3, 'email', 'recipient@example.com', $4, '\x01'::bytea, 'kek', $5)`,
		f.tenant, f.license, f.container, th[:], f.user)
	require.NoError(t, err)
	return f
}

func TestIRMPolicy_EmptyGUCSeesNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	t.Cleanup(cancel)

	db := testutil.NewProdPostureDB(ctx, t, "../../migrations")
	f := seedIRMLicense(ctx, t, db.Super)

	countBoth := func(q interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	}) (containers, licenses int) {
		require.NoError(t, q.QueryRow(ctx, `SELECT count(*) FROM irm_containers`).Scan(&containers))
		require.NoError(t, q.QueryRow(ctx, `SELECT count(*) FROM irm_licenses`).Scan(&licenses))
		return
	}

	// 1. No tenant context at all (fresh dms_app connection, GUC unset).
	db.App.Reset()
	c, l := countBoth(db.App)
	require.Zero(t, c, "irm_containers must be invisible without a tenant context")
	require.Zero(t, l, "irm_licenses must be invisible without a tenant context")

	// 2. Explicitly empty GUC — the residue a pooled connection carries after
	//    a WithTenantTx commits. Must still see nothing (and not error).
	require.NoError(t, database.WithTx(ctx, db.App, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', '', true)`); err != nil {
			return err
		}
		c, l = countBoth(tx)
		return nil
	}))
	require.Zero(t, c, "empty GUC must not widen irm_containers visibility")
	require.Zero(t, l, "empty GUC must not widen irm_licenses visibility")

	// 3. Another tenant's context sees nothing; the owner's sees its rows.
	require.NoError(t, database.WithTenantTx(ctx, db.App, uuid.Must(uuid.NewV7()), func(tx pgx.Tx) error {
		c, l = countBoth(tx)
		return nil
	}))
	require.Zero(t, c)
	require.Zero(t, l)
	require.NoError(t, database.WithTenantTx(ctx, db.App, f.tenant, func(tx pgx.Tx) error {
		c, l = countBoth(tx)
		return nil
	}))
	require.Equal(t, 1, c)
	require.Equal(t, 1, l)
}
