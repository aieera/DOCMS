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
	"github.com/aieera/sedoc/services/storage/internal/repository"
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

	// The eu-west-1 deployment (same tenant database) receives the copy.
	eu := h.newServiceIn("eu-west-1", repository.New(h.pool))
	res, err := eu.InitiateUpload(h.ctx, InitiateUploadInput{
		TenantID: h.tenant, UserID: uuid.Must(uuid.NewV7()), RegionPin: "eu-west-1",
		Filename: "doc.txt", MimeType: "text/plain", SizeBytes: int64(len(payload)),
	})
	require.NoError(t, err)
	require.Equal(t, bucketName("eu-west-1", "hot"), res.StorageBucket)
	putObject(t, h, res.StorageBucket, res.StorageKey, payload)

	_, err = eu.CompleteUpload(h.ctx, CompleteUploadInput{
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

// O-9 / M-7 — a deployment serves exactly one region. Its buckets are
// dms-<SEDOC_REGION>-*, so CheckBucket at complete could never fire for a
// session this service created (the bucket was derived from the session's
// own region). Initiate now refuses any requested region other than the
// deployment's with REGION_VIOLATION; an empty request still defaults to
// the deployment region.
func TestInitiate_RegionOutsideDeploymentRejected(t *testing.T) {
	h := newHarness(t, harnessConfig{})
	in := InitiateUploadInput{
		TenantID: h.tenant, UserID: uuid.Must(uuid.NewV7()),
		Filename: "pinned.txt", MimeType: "text/plain", SizeBytes: 5,
	}

	for _, region := range []string{"eu-west-1", "me-central-1"} {
		req := in
		req.RegionPin = region
		_, err := h.svc.InitiateUpload(h.ctx, req)
		require.Error(t, err, "a %s upload must not be accepted by a us-east-1 deployment", region)
		require.Equal(t, vdmserr.KindRegionViolation, vdmserr.KindOf(err), "got %v", err)
	}
	var sessions int
	require.NoError(t, h.pool.QueryRow(h.ctx,
		`SELECT count(*) FROM upload_sessions WHERE tenant_id=$1`, h.tenant).Scan(&sessions))
	require.Zero(t, sessions, "a rejected initiate creates no session")

	for _, region := range []string{"", "us-east-1"} {
		req := in
		req.RegionPin = region
		res, err := h.svc.InitiateUpload(h.ctx, req)
		require.NoError(t, err, "region %q", region)
		require.Equal(t, bucketName("us-east-1", "hot"), res.StorageBucket)
	}
}

// A `scanning` session belongs to a CompleteUpload pipeline that may still
// be running a little past expires_at (it claimed just in time; scan +
// encrypt + re-PUT take a while). Failing it then — on a client retry or
// in the 5-minute sweep — orphans the claimer's encrypted overwrite. A
// scanning session only counts as expired once expires_at + ScanningGrace
// has passed; initiated/uploading sessions expire at expires_at as before.
func TestClaim_ScanningWithinGraceNotExpired(t *testing.T) {
	h := newHarness(t, harnessConfig{})
	bucket := bucketName("us-east-1", "hot")
	now := time.Now().UTC()
	seed := func(status string, expiredFor time.Duration) uuid.UUID {
		id := uuid.Must(uuid.NewV7())
		exp := now.Add(-expiredFor)
		insertSession(t, h, seedSession{
			id: id, filename: "s.txt", status: status,
			createdAt: exp.Add(-time.Hour), expiresAt: exp,
			bucket: bucket, key: objectKeyAt(h.tenant, id, "s.txt", exp),
		}, 5)
		return id
	}
	justExpired := ScanningGrace / 3

	// Client retry against a claim that is still within its grace.
	busy := seed("scanning", justExpired)
	_, err := h.svc.CompleteUpload(h.ctx, CompleteUploadInput{TenantID: h.tenant, UploadID: busy})
	require.Error(t, err)
	require.Contains(t, err.Error(), "in progress", "a scanning claim within its grace is still in progress")
	require.Equal(t, "scanning", sessionStatus(t, h, busy))

	// The sweeper applies the same rule.
	stale := seed("scanning", ScanningGrace+5*time.Minute)
	idle := seed("initiated", justExpired)
	n, err := h.svc.SweepExpiredUploads(h.ctx)
	require.NoError(t, err)
	require.EqualValues(t, 2, n)
	require.Equal(t, "scanning", sessionStatus(t, h, busy), "the sweep leaves an in-grace claim alone")
	require.Equal(t, "failed", sessionStatus(t, h, stale), "a claim past its grace is released")
	require.Equal(t, "failed", sessionStatus(t, h, idle), "initiated sessions expire at expires_at")
}
