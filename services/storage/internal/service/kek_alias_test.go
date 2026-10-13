//go:build integration
// +build integration

// D-22 — liveKEKAlias swallowed every DB error and fell back to the base
// alias "vaultdms/tenant/<uuid>". After a rotation the base alias is the
// RETIRED v1 key, so a transient tenant_keks read failure made RewrapDEK
// re-wrap live blobs back under the retired KEK and made new uploads
// encrypt under it. Only "no tenant_keks row" (ErrNoRows) may fall back.
package service

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestLiveKEKAlias_DBErrorAborts(t *testing.T) {
	h := newHarness(t, harnessConfig{encrypt: true})
	base := aliasForTenant(h.tenant)
	live := base + "@v2"
	_, err := h.pool.Exec(h.ctx, `
		INSERT INTO tenant_keks (tenant_id, version, alias, retired_at) VALUES
			($1, 1, $2, now() - interval '1 day'),
			($1, 2, $3, NULL)`, h.tenant, base, live)
	require.NoError(t, err)

	// A blob encrypted under the live (rotated) KEK.
	payload := []byte("encrypted under the rotated v2 key")
	sum := sha256.Sum256(payload)
	res, err := h.svc.InitiateUpload(h.ctx, InitiateUploadInput{
		TenantID: h.tenant, UserID: uuid.Must(uuid.NewV7()),
		Filename: "rotated.txt", MimeType: "text/plain", SizeBytes: int64(len(payload)),
	})
	require.NoError(t, err)
	putObject(t, h, res.StorageBucket, res.StorageKey, payload)
	_, err = h.svc.CompleteUpload(h.ctx, CompleteUploadInput{TenantID: h.tenant, UploadID: res.UploadID})
	require.NoError(t, err)
	var blobID uuid.UUID
	var kek string
	require.NoError(t, h.pool.QueryRow(h.ctx,
		`SELECT id, kek_id FROM content_blobs WHERE tenant_id=$1 AND sha256_hash=$2`,
		h.tenant, hex.EncodeToString(sum[:])).Scan(&blobID, &kek))
	require.Equal(t, live, kek, "new encrypts use the live alias")

	// tenant_keks becomes unreadable (any DB error).
	_, err = h.pool.Exec(h.ctx, `ALTER TABLE tenant_keks RENAME TO tenant_keks_offline`)
	require.NoError(t, err)

	// Rewrap must abort, not re-wrap the blob back under the retired base KEK.
	_, err = h.svc.RewrapDEK(h.ctx, RewrapDEKInput{TenantID: h.tenant, BlobID: blobID})
	require.Error(t, err, "a tenant_keks read error must abort the rewrap")
	require.NoError(t, h.pool.QueryRow(h.ctx,
		`SELECT kek_id FROM content_blobs WHERE id=$1`, blobID).Scan(&kek))
	require.Equal(t, live, kek, "the blob stays on the live KEK")

	// A new upload must fail rather than encrypt under the retired KEK.
	other := []byte("must not be encrypted under a guessed key")
	res2, err := h.svc.InitiateUpload(h.ctx, InitiateUploadInput{
		TenantID: h.tenant, UserID: uuid.Must(uuid.NewV7()),
		Filename: "second.txt", MimeType: "text/plain", SizeBytes: int64(len(other)),
	})
	require.NoError(t, err)
	putObject(t, h, res2.StorageBucket, res2.StorageKey, other)
	_, err = h.svc.CompleteUpload(h.ctx, CompleteUploadInput{TenantID: h.tenant, UploadID: res2.UploadID})
	require.Error(t, err, "a tenant_keks read error must fail the upload")
	require.Equal(t, "failed", sessionStatus(t, h, res2.UploadID))
	var n int
	require.NoError(t, h.pool.QueryRow(h.ctx,
		`SELECT count(*) FROM content_blobs WHERE tenant_id=$1 AND kek_id=$2`, h.tenant, base).Scan(&n))
	require.Zero(t, n, "nothing is ever encrypted under the retired base alias")
}

// A tenant that never ran `kms create` has no tenant_keks row: that (and
// only that) falls back to the base alias.
func TestLiveKEKAlias_NoRowFallsBackToBase(t *testing.T) {
	h := newHarness(t, harnessConfig{encrypt: true})
	payload := []byte("tenant without a tenant_keks row")
	res, err := h.svc.InitiateUpload(h.ctx, InitiateUploadInput{
		TenantID: h.tenant, UserID: uuid.Must(uuid.NewV7()),
		Filename: "plainkek.txt", MimeType: "text/plain", SizeBytes: int64(len(payload)),
	})
	require.NoError(t, err)
	putObject(t, h, res.StorageBucket, res.StorageKey, payload)
	_, err = h.svc.CompleteUpload(h.ctx, CompleteUploadInput{TenantID: h.tenant, UploadID: res.UploadID})
	require.NoError(t, err)
	var kek string
	require.NoError(t, h.pool.QueryRow(h.ctx,
		`SELECT kek_id FROM content_blobs WHERE tenant_id=$1`, h.tenant).Scan(&kek))
	require.Equal(t, aliasForTenant(h.tenant), kek)
}
