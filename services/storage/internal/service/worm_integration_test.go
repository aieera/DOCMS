//go:build integration
// +build integration

// D-17 — re-applying WORM must not shorten or weaken an existing lock.
//
// ApplyWORM always server-side copied the blob "into" the WORM bucket —
// even when the blob already lived there — and then set the requested
// retention. A copy of an object onto itself is a new version in a
// versioned bucket, i.e. a way around S3's rule that a locked version's
// retention cannot be shortened or downgraded. In practice MinIO and AWS
// reject that self-copy outright, so every re-apply — including a
// legitimate extension — failed with a 500. A blob already in the WORM
// bucket is now never copied: the retention change is applied to the
// locked object itself, S3 enforces "extend only, never downgrade", and a
// refusal surfaces as a Conflict. (The document side additionally keeps
// worm_retain_until monotonic with GREATEST.)
package service

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
)

func seedWORMCandidate(t *testing.T, h *completeHarness, payload []byte) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	key := objectKeyAt(h.tenant, id, "worm.txt", time.Now())
	putObject(t, h, bucketName("us-east-1", "hot"), key, payload)
	return insertPlainBlob(t, h, bucketName("us-east-1", "hot"), key, payload)
}

func TestApplyWORM_CannotShorten(t *testing.T) {
	h := newHarness(t, harnessConfig{})
	blobID := seedWORMCandidate(t, h, []byte("retained for two days"))
	long := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)

	first, err := h.svc.ApplyWORM(h.ctx, ApplyWORMInput{TenantID: h.tenant, BlobID: blobID, RetainUntil: long, Mode: "COMPLIANCE"})
	require.NoError(t, err)
	require.Equal(t, bucketName("us-east-1", "worm"), first.WORMBucket)

	_, err = h.svc.ApplyWORM(h.ctx, ApplyWORMInput{
		TenantID: h.tenant, BlobID: blobID, RetainUntil: long.Add(-24 * time.Hour), Mode: "COMPLIANCE",
	})
	require.Error(t, err, "an earlier retain-until must be refused")
	require.Equal(t, vdmserr.KindConflict, vdmserr.KindOf(err), "got %v", err)

	// Extending stays allowed and touches only the locked object.
	_, err = h.svc.ApplyWORM(h.ctx, ApplyWORMInput{
		TenantID: h.tenant, BlobID: blobID, RetainUntil: long.Add(24 * time.Hour), Mode: "COMPLIANCE",
	})
	require.NoError(t, err, "extending an existing lock is allowed")
	require.Equal(t, bucketName("us-east-1", "worm"), loadBlob(t, h, blobID).StorageBucket)
}

func TestApplyWORM_CannotDowngradeMode(t *testing.T) {
	h := newHarness(t, harnessConfig{})
	blobID := seedWORMCandidate(t, h, []byte("compliance-locked"))
	until := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)

	_, err := h.svc.ApplyWORM(h.ctx, ApplyWORMInput{TenantID: h.tenant, BlobID: blobID, RetainUntil: until, Mode: "COMPLIANCE"})
	require.NoError(t, err)

	_, err = h.svc.ApplyWORM(h.ctx, ApplyWORMInput{
		TenantID: h.tenant, BlobID: blobID, RetainUntil: until.Add(24 * time.Hour), Mode: "GOVERNANCE",
	})
	require.Error(t, err, "COMPLIANCE must not be downgraded to GOVERNANCE")
	require.Equal(t, vdmserr.KindConflict, vdmserr.KindOf(err), "got %v", err)
}
