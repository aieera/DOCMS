//go:build integration
// +build integration

// AbortUpload must never destroy bytes a CompleteUpload is committing.
//
// Abort read the session without a lock, accepted `scanning`, deleted the
// object before its own commit and then flipped the status to failed
// unconditionally. A CompleteUpload whose final transaction committed in
// between left a content_blobs row with no bytes, and the session went
// completed → failed. Abort now locks the session, refuses anything but
// initiated/uploading (409), fails it through a guarded transition that
// also ends its validity window (so no later complete can re-claim it), and
// deletes the object only after that commit.
package service

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
)

func TestAbort_DuringCompleteNeverDeletesCommittedBytes(t *testing.T) {
	h := newHarness(t, harnessConfig{})
	bucket := bucketName("us-east-1", "hot")
	now := time.Now().UTC()
	payload := []byte("bytes a completion is processing right now")

	for _, status := range []string{"scanning", "completed", "quarantined"} {
		t.Run(status, func(t *testing.T) {
			id := uuid.Must(uuid.NewV7())
			key := objectKeyAt(h.tenant, id, "busy.txt", now)
			insertSession(t, h, seedSession{
				id: id, filename: "busy.txt", status: status,
				createdAt: now, expiresAt: now.Add(time.Hour), bucket: bucket, key: key,
			}, int64(len(payload)))
			putObject(t, h, bucket, key, payload)

			err := h.svc.AbortUpload(h.ctx, h.tenant, id)
			require.Error(t, err, "abort of a %s session must be refused", status)
			require.Equal(t, vdmserr.KindConflict, vdmserr.KindOf(err), "got %v", err)
			_, err = h.s3.GetObjectInfo(h.ctx, bucket, key)
			require.NoError(t, err, "the object of a %s session must survive an abort attempt", status)
			require.Equal(t, status, sessionStatus(t, h, id), "status must not change")
		})
	}
}

// An aborted session is finished: its object is gone and a late
// CompleteUpload cannot re-claim it (failed sessions are otherwise
// re-claimable so a client can retry after e.g. a scanner outage).
func TestAbort_ThenCompleteCannotReclaim(t *testing.T) {
	h := newHarness(t, harnessConfig{})
	bucket := bucketName("us-east-1", "hot")
	now := time.Now().UTC()
	id := uuid.Must(uuid.NewV7())
	key := objectKeyAt(h.tenant, id, "gone.txt", now)
	insertSession(t, h, seedSession{
		id: id, filename: "gone.txt", status: "initiated",
		createdAt: now, expiresAt: now.Add(time.Hour), bucket: bucket, key: key,
	}, 5)
	putObject(t, h, bucket, key, []byte("12345"))

	require.NoError(t, h.svc.AbortUpload(h.ctx, h.tenant, id))
	_, err := h.s3.GetObjectInfo(h.ctx, bucket, key)
	require.Error(t, err, "abort deletes the uploaded object")
	require.Equal(t, "failed", sessionStatus(t, h, id))

	putObject(t, h, bucket, key, []byte("12345")) // a straggling PUT lands afterwards
	_, err = h.svc.CompleteUpload(h.ctx, CompleteUploadInput{TenantID: h.tenant, UploadID: id})
	require.Error(t, err)
	require.Contains(t, err.Error(), "expired", "an aborted session is closed, not re-claimable")
	require.Equal(t, "failed", sessionStatus(t, h, id))
	var blobs int
	require.NoError(t, h.pool.QueryRow(h.ctx, `SELECT count(*) FROM content_blobs WHERE tenant_id=$1`, h.tenant).Scan(&blobs))
	require.Zero(t, blobs)
}
