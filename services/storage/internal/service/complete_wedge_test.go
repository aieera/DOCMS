//go:build integration
// +build integration

// L-6 — CompleteUpload must never wedge a session in `scanning`.
//
//   - content_blobs is UNIQUE (tenant_id, sha256_hash) with no region, but
//     the dedup lookup filtered by region: re-uploading identical bytes
//     into a second region hit the unique violation, the region-filtered
//     lookup found nothing, the final transaction errored, and nothing
//     moved the session out of `scanning`.
//   - the claim checked `scanning` (409 "in progress") before expiry, so
//     a session whose claimer died stayed wedged forever; and the expired
//     branch flipped the status inside a transaction it then rolled back.
package service

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
)

func sessionStatus(t *testing.T, h *completeHarness, id uuid.UUID) string {
	t.Helper()
	var status string
	require.NoError(t, h.pool.QueryRow(h.ctx,
		`SELECT status FROM upload_sessions WHERE tenant_id=$1 AND id=$2`, h.tenant, id).Scan(&status))
	return status
}

func TestComplete_SecondRegionDuplicateFailsCleanly(t *testing.T) {
	h := newHarness(t, harnessConfig{})
	require.NoError(t, h.s3.CreateBucket(h.ctx, bucketName("eu-west-1", "hot"), "eu-west-1"))
	payload := []byte("identical bytes, first stored in us-east-1")
	sum := sha256.Sum256(payload)

	firstID := uuid.Must(uuid.NewV7())
	firstKey := objectKeyAt(h.tenant, firstID, "doc.txt", previousMonth())
	putObject(t, h, bucketName("us-east-1", "hot"), firstKey, payload)
	existing := insertPlainBlob(t, h, bucketName("us-east-1", "hot"), firstKey, payload)

	res, err := h.svc.InitiateUpload(h.ctx, InitiateUploadInput{
		TenantID: h.tenant, UserID: uuid.Must(uuid.NewV7()), RegionPin: "eu-west-1",
		Filename: "doc.txt", MimeType: "text/plain", SizeBytes: int64(len(payload)),
	})
	require.NoError(t, err)
	require.Equal(t, bucketName("eu-west-1", "hot"), res.StorageBucket)
	putObject(t, h, res.StorageBucket, res.StorageKey, payload)

	_, err = h.svc.CompleteUpload(h.ctx, CompleteUploadInput{
		TenantID: h.tenant, UploadID: res.UploadID, SHA256Hash: hex.EncodeToString(sum[:]),
	})
	require.Error(t, err, "the eu-west-1 copy cannot dedup onto a us-east-1 blob")
	require.Equal(t, vdmserr.KindRegionViolation, vdmserr.KindOf(err), "got %v", err)

	require.Equal(t, "failed", sessionStatus(t, h, res.UploadID), "the session must not be left in scanning")
	_, err = h.s3.GetObjectInfo(h.ctx, res.StorageBucket, res.StorageKey)
	require.Error(t, err, "the rejected upload's object is removed")

	var blobs, refcount int
	require.NoError(t, h.pool.QueryRow(h.ctx,
		`SELECT count(*) FROM content_blobs WHERE tenant_id=$1`, h.tenant).Scan(&blobs))
	require.Equal(t, 1, blobs)
	require.NoError(t, h.pool.QueryRow(h.ctx,
		`SELECT reference_count FROM content_blobs WHERE id=$1`, existing).Scan(&refcount))
	require.Equal(t, 1, refcount, "the other region's blob gains no reference")
	_, err = h.s3.GetObjectInfo(h.ctx, bucketName("us-east-1", "hot"), firstKey)
	require.NoError(t, err, "the existing blob's bytes are untouched")
}

// A claimer that died after flipping the session to `scanning` must not
// wedge it: once the session is past expires_at the claim releases it
// (status failed, persisted) so the client starts a fresh upload, instead
// of answering "already in progress" forever.
func TestClaim_StaleScanningReclaimable(t *testing.T) {
	h := newHarness(t, harnessConfig{})
	past := time.Now().UTC().Add(-2 * time.Hour)

	stale := uuid.Must(uuid.NewV7())
	insertSession(t, h, seedSession{
		id: stale, filename: "stale.txt", status: "scanning",
		createdAt: past, expiresAt: past.Add(time.Hour),
		bucket: bucketName("us-east-1", "hot"), key: objectKeyAt(h.tenant, stale, "stale.txt", past),
	}, 5)
	_, err := h.svc.CompleteUpload(h.ctx, CompleteUploadInput{TenantID: h.tenant, UploadID: stale})
	require.Error(t, err)
	require.Contains(t, err.Error(), "expired", "a stale claim is reported as expired, not in progress")
	require.Equal(t, "failed", sessionStatus(t, h, stale), "the stale scanning claim is released")

	// The expired branch's status flip must actually commit.
	expired := uuid.Must(uuid.NewV7())
	insertSession(t, h, seedSession{
		id: expired, filename: "late.txt", status: "initiated",
		createdAt: past, expiresAt: past.Add(time.Hour),
		bucket: bucketName("us-east-1", "hot"), key: objectKeyAt(h.tenant, expired, "late.txt", past),
	}, 5)
	_, err = h.svc.CompleteUpload(h.ctx, CompleteUploadInput{TenantID: h.tenant, UploadID: expired})
	require.Error(t, err)
	require.Contains(t, err.Error(), "expired")
	require.Equal(t, "failed", sessionStatus(t, h, expired))

	// A live claim (not expired) still answers "in progress".
	live := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()
	insertSession(t, h, seedSession{
		id: live, filename: "live.txt", status: "scanning",
		createdAt: now, expiresAt: now.Add(time.Hour),
		bucket: bucketName("us-east-1", "hot"), key: objectKeyAt(h.tenant, live, "live.txt", now),
	}, 5)
	_, err = h.svc.CompleteUpload(h.ctx, CompleteUploadInput{TenantID: h.tenant, UploadID: live})
	require.Error(t, err)
	require.Contains(t, err.Error(), "in progress")
	require.Equal(t, "scanning", sessionStatus(t, h, live))
}

// O-9 / M-7 — RegionEnforcer had no callers, so nothing checked that the
// bucket an upload completes into belongs to the session's pinned region.
// CompleteUpload now runs CheckBucket before reading the object; a
// mismatch fails the session and creates no blob.
func TestComplete_BucketOutsideRegionRejected(t *testing.T) {
	h := newHarness(t, harnessConfig{})
	foreign := bucketName("me-central-1", "hot")
	require.NoError(t, h.s3.CreateBucket(h.ctx, foreign, "us-east-1"))
	payload := []byte("pinned to us-east-1 but parked in a me-central-1 bucket")
	now := time.Now().UTC()
	id := uuid.Must(uuid.NewV7())
	key := objectKeyAt(h.tenant, id, "pinned.txt", now)
	insertSession(t, h, seedSession{
		id: id, filename: "pinned.txt", status: "initiated", // storage_region us-east-1
		createdAt: now, expiresAt: now.Add(time.Hour),
		bucket: foreign, key: key,
	}, int64(len(payload)))
	putObject(t, h, foreign, key, payload)

	_, err := h.svc.CompleteUpload(h.ctx, CompleteUploadInput{TenantID: h.tenant, UploadID: id})
	require.Error(t, err, "a bucket outside the session's region must be rejected")
	require.Equal(t, vdmserr.KindRegionViolation, vdmserr.KindOf(err), "got %v", err)
	require.Equal(t, "failed", sessionStatus(t, h, id))
	var blobs int
	require.NoError(t, h.pool.QueryRow(h.ctx,
		`SELECT count(*) FROM content_blobs WHERE tenant_id=$1`, h.tenant).Scan(&blobs))
	require.Zero(t, blobs, "no blob may record bytes stored outside its region")
}
