//go:build integration
// +build integration

// D-15 — ReencryptBlob must never destroy the live ciphertext before the
// database commit.
//
// A same-bucket re-encrypt (same region, new regional KEK) PUT the new
// ciphertext over the live object (same bucket AND key) and only then
// updated the row. If the UPDATE failed, the row still held the old DEK
// and nonce while the object held the new ciphertext: the blob was
// permanently undecryptable. Re-encrypt now writes a fresh key, verifies
// the new envelope round-trips before the PUT, flips bucket/key in the
// UPDATE, and deletes the old object only after the commit.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	pkgcrypto "github.com/aieera/sedoc/pkg/crypto"
	"github.com/aieera/sedoc/services/storage/internal/model"
	"github.com/aieera/sedoc/services/storage/internal/repository"
)

// uploadEncrypted runs a real initiate → PUT → complete and returns the
// resulting (envelope-encrypted) blob id.
func uploadEncrypted(t *testing.T, h *completeHarness, name string, payload []byte) uuid.UUID {
	t.Helper()
	res, err := h.svc.InitiateUpload(h.ctx, InitiateUploadInput{
		TenantID: h.tenant, UserID: uuid.Must(uuid.NewV7()),
		Filename: name, MimeType: "text/plain", SizeBytes: int64(len(payload)),
	})
	require.NoError(t, err)
	putObject(t, h, res.StorageBucket, res.StorageKey, payload)
	_, err = h.svc.CompleteUpload(h.ctx, CompleteUploadInput{TenantID: h.tenant, UploadID: res.UploadID})
	require.NoError(t, err)
	sum := sha256.Sum256(payload)
	var id uuid.UUID
	require.NoError(t, h.pool.QueryRow(h.ctx,
		`SELECT id FROM content_blobs WHERE tenant_id=$1 AND sha256_hash=$2`,
		h.tenant, hex.EncodeToString(sum[:])).Scan(&id))
	return id
}

func loadBlob(t *testing.T, h *completeHarness, id uuid.UUID) *model.ContentBlob {
	t.Helper()
	tx, err := h.pool.Begin(h.ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(h.ctx) }()
	b, err := repository.New(h.pool).ContentBlobs.GetByID(h.ctx, tx, h.tenant, id)
	require.NoError(t, err)
	return b
}

// readBlob decrypts the blob exactly the way a download does: the row's
// bucket/key/envelope against the object currently in S3.
func readBlob(t *testing.T, h *completeHarness, id uuid.UUID) ([]byte, error) {
	t.Helper()
	b := loadBlob(t, h, id)
	rc, err := h.s3.GetObject(h.ctx, b.StorageBucket, b.StorageKey)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return h.svc.decryptAll(h.ctx, rc, b.KEKID, b.EncryptedDEK, b.DEKNonce)
}

// failingBlobRepo makes the commit-point UPDATE fail.
type failingBlobRepo struct {
	repository.ContentBlobRepo
}

func (failingBlobRepo) UpdateMigration(context.Context, pgx.Tx, *model.ContentBlob, *model.ContentBlob) error {
	return errors.New("injected: database unavailable at commit")
}

// corruptingKMS returns a wrapped DEK that does not unwrap for one KEK.
type corruptingKMS struct {
	pkgcrypto.KeyManager
	kek string
}

func (k corruptingKMS) GenerateDataKey(ctx context.Context, kekID string) ([]byte, []byte, error) {
	pt, enc, err := k.KeyManager.GenerateDataKey(ctx, kekID)
	if err == nil && kekID == k.kek {
		enc = append([]byte(nil), enc...)
		enc[len(enc)-1] ^= 0xff
	}
	return pt, enc, err
}

func TestReencrypt_SameBucketFailureKeepsOldReadable(t *testing.T) {
	h := newHarness(t, harnessConfig{encrypt: true})
	payload := []byte("live ciphertext that must survive a failed re-encrypt")
	blobID := uploadEncrypted(t, h, "keep.txt", payload)
	before := loadBlob(t, h, blobID)

	repos := repository.New(h.pool)
	repos.ContentBlobs = failingBlobRepo{repos.ContentBlobs}
	failing := h.newService(repos)

	// Same region, new regional KEK → target bucket == source bucket.
	_, err := failing.ReencryptBlob(h.ctx, ReencryptBlobInput{TenantID: h.tenant, BlobID: blobID, TargetRegion: "us-east-1"})
	require.Error(t, err, "the injected UPDATE failure surfaces")

	after := loadBlob(t, h, blobID)
	require.Equal(t, before.StorageKey, after.StorageKey)
	require.Equal(t, before.KEKID, after.KEKID)
	got, err := readBlob(t, h, blobID)
	require.NoError(t, err, "the blob must still decrypt with its committed envelope")
	require.Equal(t, payload, got)
}

func TestReencrypt_SameBucketMovesToFreshKey(t *testing.T) {
	h := newHarness(t, harnessConfig{encrypt: true})
	payload := []byte("re-keyed in place, new object key")
	blobID := uploadEncrypted(t, h, "move.txt", payload)
	before := loadBlob(t, h, blobID)

	res, err := h.svc.ReencryptBlob(h.ctx, ReencryptBlobInput{TenantID: h.tenant, BlobID: blobID, TargetRegion: "us-east-1"})
	require.NoError(t, err)
	after := loadBlob(t, h, blobID)
	require.Equal(t, before.StorageBucket, after.StorageBucket)
	require.NotEqual(t, before.StorageKey, after.StorageKey, "same-bucket re-encrypt writes a new key")
	require.Equal(t, res.TargetKey, after.StorageKey)
	require.Equal(t, aliasForTenantInRegion(h.tenant, "us-east-1"), after.KEKID)

	got, err := readBlob(t, h, blobID)
	require.NoError(t, err)
	require.Equal(t, payload, got)
	_, err = h.s3.GetObjectInfo(h.ctx, before.StorageBucket, before.StorageKey)
	require.Error(t, err, "the old object is deleted after the commit")

	// Idempotent: a second call is a no-op on the already-migrated blob.
	again, err := h.svc.ReencryptBlob(h.ctx, ReencryptBlobInput{TenantID: h.tenant, BlobID: blobID, TargetRegion: "us-east-1"})
	require.NoError(t, err)
	require.Equal(t, after.StorageKey, again.TargetKey)
}

func TestReencrypt_RoundTripFailureAbortsBeforePut(t *testing.T) {
	h := newHarness(t, harnessConfig{encrypt: true})
	payload := []byte("a broken wrap must never reach S3")
	blobID := uploadEncrypted(t, h, "wrap.txt", payload)
	before := loadBlob(t, h, blobID)

	h.kms = corruptingKMS{KeyManager: h.kms, kek: aliasForTenantInRegion(h.tenant, "us-east-1")}
	broken := h.newService(repository.New(h.pool))
	_, err := broken.ReencryptBlob(h.ctx, ReencryptBlobInput{TenantID: h.tenant, BlobID: blobID, TargetRegion: "us-east-1"})
	require.Error(t, err, "an envelope that does not round-trip must abort the re-encrypt")

	after := loadBlob(t, h, blobID)
	require.Equal(t, before.StorageKey, after.StorageKey)
	require.Equal(t, before.KEKID, after.KEKID)
	got, err := readBlob(t, h, blobID)
	require.NoError(t, err)
	require.Equal(t, payload, got)
}
