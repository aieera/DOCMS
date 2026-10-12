//go:build integration
// +build integration

// M-1 (storage half) — no blob claiming through a client-supplied hash.
//
// Dedup-on-initiate answered "do you already have bytes with SHA-256 X?"
// by returning the existing blob's id, bucket and key and bumping its
// reference count — so anyone who knew (or guessed) a file's hash could
// link and download bytes they were never given. Initiate now always
// issues a PUT URL for a fresh session; dedup happens only at complete,
// after the server has hashed the bytes the caller actually uploaded.
package service

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestInitiateUpload_KnownHashNeverReturnsExistingBlob(t *testing.T) {
	h := newHarness(t, harnessConfig{})
	secret := []byte("bytes the caller has never been given")
	sum := sha256.Sum256(secret)
	hash := hex.EncodeToString(sum[:])

	// Another user's upload already stored these bytes.
	ownerID := uuid.Must(uuid.NewV7())
	ownerKey := objectKeyAt(h.tenant, ownerID, "secret.txt", previousMonth())
	putObject(t, h, bucketName("us-east-1", "hot"), ownerKey, secret)
	existing := insertPlainBlob(t, h, bucketName("us-east-1", "hot"), ownerKey, secret)

	res, err := h.svc.InitiateUpload(h.ctx, InitiateUploadInput{
		TenantID: h.tenant, UserID: uuid.Must(uuid.NewV7()),
		Filename: "guess.txt", MimeType: "text/plain", SizeBytes: int64(len(secret)),
		SHA256Hash: hash,
	})
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, res.UploadID, "a fresh upload session is always created")
	require.NotEmpty(t, res.PresignedPutURL, "the caller must upload the bytes itself")
	require.NotEqual(t, ownerKey, res.StorageKey, "the existing blob's key must never be disclosed")
	require.Equal(t, objectKeyAt(h.tenant, res.UploadID, "guess.txt", time.Now()), res.StorageKey)

	var refcount, dedupEvents int
	require.NoError(t, h.pool.QueryRow(h.ctx,
		`SELECT reference_count FROM content_blobs WHERE id=$1`, existing).Scan(&refcount))
	require.Equal(t, 1, refcount, "a hash alone must not take a reference on someone else's blob")
	require.NoError(t, h.pool.QueryRow(h.ctx,
		`SELECT count(*) FROM outbox WHERE event_type='dms.storage.upload_deduplicated.v1'`).Scan(&dedupEvents))
	require.Zero(t, dedupEvents)

	// Late dedup still works once the caller proves possession by uploading.
	putObject(t, h, res.StorageBucket, res.StorageKey, secret)
	done, err := h.svc.CompleteUpload(h.ctx, CompleteUploadInput{TenantID: h.tenant, UploadID: res.UploadID, SHA256Hash: hash})
	require.NoError(t, err)
	require.Equal(t, ownerKey, done.StorageKey, "proven-possession dedup reuses the stored blob")
	var linked uuid.UUID
	require.NoError(t, h.pool.QueryRow(h.ctx,
		`SELECT content_blob_id FROM upload_sessions WHERE tenant_id=$1 AND id=$2`, h.tenant, res.UploadID).Scan(&linked))
	require.Equal(t, existing, linked)
	require.NoError(t, h.pool.QueryRow(h.ctx,
		`SELECT reference_count FROM content_blobs WHERE id=$1`, existing).Scan(&refcount))
	require.Equal(t, 2, refcount)
	_, err = h.s3.GetObjectInfo(h.ctx, res.StorageBucket, res.StorageKey)
	require.Error(t, err, "the duplicate upload's own object is cleaned up")
}
