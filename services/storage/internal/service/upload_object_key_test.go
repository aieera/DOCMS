//go:build integration
// +build integration

// H-9 / DEC-9 — upload object keys must not be recomputed from the
// current date.
//
// Keys are laid out {tenant}/{yyyy}/{mm}/{upload_id}/{filename}. The old
// code rebuilt the key from time.Now() on initiate, complete, abort and
// download, so anything that crossed a month boundary addressed the
// wrong object: complete failed ("object missing in storage"), abort
// deleted nothing, and presigned downloads of older unencrypted blobs
// 404'd. Initiate now persists the bucket/key (migration 000107);
// complete/abort use it; download prefers the blob's own key, then the
// persisted session key, then (legacy rows only) the key derived from
// upload_sessions.created_at.
package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type seedSession struct {
	id            uuid.UUID
	filename      string
	status        string
	createdAt     time.Time
	expiresAt     time.Time
	bucket, key   string // "" → NULL (legacy, pre-000107 shape)
	contentBlobID *uuid.UUID
	legacyShape   bool // insert without the 000107 columns (pre-migration schema)
}

func insertSession(t *testing.T, h *completeHarness, s seedSession, size int64) {
	t.Helper()
	if s.legacyShape {
		_, err := h.pool.Exec(h.ctx, `
			INSERT INTO upload_sessions (tenant_id, id, filename, total_size, mime_type,
				upload_type, storage_region, status, created_at, expires_at, content_blob_id)
			VALUES ($1,$2,$3,$4,'text/plain','single','us-east-1',$5,$6,$7,$8)`,
			h.tenant, s.id, s.filename, size, s.status, s.createdAt, s.expiresAt, s.contentBlobID)
		require.NoError(t, err)
		return
	}
	nullable := func(v string) any {
		if v == "" {
			return nil
		}
		return v
	}
	_, err := h.pool.Exec(h.ctx, `
		INSERT INTO upload_sessions (tenant_id, id, filename, total_size, mime_type,
			upload_type, storage_region, status, created_at, expires_at, content_blob_id,
			storage_bucket, storage_key)
		VALUES ($1,$2,$3,$4,'text/plain','single','us-east-1',$5,$6,$7,$8,$9,$10)`,
		h.tenant, s.id, s.filename, size, s.status, s.createdAt, s.expiresAt, s.contentBlobID,
		nullable(s.bucket), nullable(s.key))
	require.NoError(t, err)
}

func insertPlainBlob(t *testing.T, h *completeHarness, bucket, key string, payload []byte) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	sum := sha256.Sum256(payload)
	_, err := h.pool.Exec(h.ctx, `
		INSERT INTO content_blobs (id, tenant_id, sha256_hash, storage_region, storage_bucket,
			storage_key, storage_class, size_bytes, mime_type, reference_count)
		VALUES ($1,$2,$3,'us-east-1',$4,$5,'hot',$6,'text/plain',1)`,
		id, h.tenant, hex.EncodeToString(sum[:]), bucket, key, len(payload))
	require.NoError(t, err)
	return id
}

func putObject(t *testing.T, h *completeHarness, bucket, key string, payload []byte) {
	t.Helper()
	require.NoError(t, h.s3.PutObject(h.ctx, bucket, key, bytes.NewReader(payload), int64(len(payload)), "text/plain"))
}

func TestInitiate_PersistsObjectKey(t *testing.T) {
	h := newHarness(t, harnessConfig{})
	initiatedAt := previousMonth()
	h.svc.now = func() time.Time { return initiatedAt }

	res, err := h.svc.InitiateUpload(h.ctx, InitiateUploadInput{
		TenantID: h.tenant, UserID: uuid.Must(uuid.NewV7()),
		Filename: "report.txt", MimeType: "text/plain", SizeBytes: 5,
	})
	require.NoError(t, err)
	require.Equal(t, objectKeyAt(h.tenant, res.UploadID, "report.txt", initiatedAt), res.StorageKey,
		"the key is derived from the initiation instant, not the wall clock of a later call")

	var bucket, key *string
	require.NoError(t, h.pool.QueryRow(h.ctx,
		`SELECT storage_bucket, storage_key FROM upload_sessions WHERE tenant_id=$1 AND id=$2`,
		h.tenant, res.UploadID).Scan(&bucket, &key))
	require.NotNil(t, bucket, "initiate must persist the bucket it signed the PUT for")
	require.NotNil(t, key, "initiate must persist the key it signed the PUT for")
	require.Equal(t, res.StorageBucket, *bucket)
	require.Equal(t, res.StorageKey, *key)
}

func TestComplete_AcrossMonthBoundary(t *testing.T) {
	h := newHarness(t, harnessConfig{})
	payload := []byte("initiated at the end of last month, completed this month")

	// Initiated in the last second of the previous month; the client's PUT
	// landed at the key signed then. Completion happens now (this month).
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	initiatedAt := monthStart.Add(-time.Second)
	id := uuid.Must(uuid.NewV7())
	bucket := bucketName("us-east-1", "hot")
	key := objectKeyAt(h.tenant, id, "boundary.txt", initiatedAt)
	insertSession(t, h, seedSession{
		id: id, filename: "boundary.txt", status: "initiated",
		createdAt: initiatedAt, expiresAt: now.Add(time.Hour),
		bucket: bucket, key: key,
	}, int64(len(payload)))
	putObject(t, h, bucket, key, payload)

	res, err := h.svc.CompleteUpload(h.ctx, CompleteUploadInput{TenantID: h.tenant, UploadID: id})
	require.NoError(t, err, "a complete that crosses a month boundary must find the uploaded object")
	require.Equal(t, key, res.StorageKey)

	var status, blobKey string
	require.NoError(t, h.pool.QueryRow(h.ctx,
		`SELECT s.status, b.storage_key FROM upload_sessions s
		   JOIN content_blobs b ON b.tenant_id = s.tenant_id AND b.id = s.content_blob_id
		  WHERE s.tenant_id=$1 AND s.id=$2`, h.tenant, id).Scan(&status, &blobKey))
	require.Equal(t, "completed", status)
	require.Equal(t, key, blobKey, "the blob records the key the bytes actually live at")
}

func TestGetDownloadURL_OldMonthBlob(t *testing.T) {
	h := newHarness(t, harnessConfig{})
	payload := []byte("an unencrypted blob uploaded last month")
	createdAt := previousMonth()
	id := uuid.Must(uuid.NewV7())
	bucket := bucketName("us-east-1", "hot")
	key := objectKeyAt(h.tenant, id, "old.txt", createdAt)
	putObject(t, h, bucket, key, payload)
	blobID := insertPlainBlob(t, h, bucket, key, payload)
	insertSession(t, h, seedSession{
		id: id, filename: "old.txt", status: "completed",
		createdAt: createdAt, expiresAt: createdAt.Add(time.Hour),
		bucket: bucket, key: key, contentBlobID: &blobID,
	}, int64(len(payload)))

	url, _, err := h.svc.GetDownloadURL(h.ctx, h.tenant, id, 60)
	require.NoError(t, err)
	code, body := httpGet(t, url)
	require.Equal(t, http.StatusOK, code, "presigned download of last month's blob must resolve to its real key")
	require.Equal(t, payload, body)
}

// A late-dedup completion points the session at a PRE-EXISTING blob whose
// key belongs to the earlier upload; the session's own object was deleted
// as a duplicate. Download must follow content_blob_id to the blob's key.
func TestGetDownloadURL_DedupSessionServesBlobKey(t *testing.T) {
	h := newHarness(t, harnessConfig{})
	payload := []byte("same bytes uploaded twice, stored once")
	bucket := bucketName("us-east-1", "hot")

	firstID := uuid.Must(uuid.NewV7())
	firstKey := objectKeyAt(h.tenant, firstID, "a.txt", previousMonth())
	putObject(t, h, bucket, firstKey, payload)
	blobID := insertPlainBlob(t, h, bucket, firstKey, payload)

	now := time.Now().UTC()
	dupID := uuid.Must(uuid.NewV7())
	insertSession(t, h, seedSession{
		id: dupID, filename: "b.txt", status: "completed",
		createdAt: now, expiresAt: now.Add(time.Hour),
		bucket: bucket, key: objectKeyAt(h.tenant, dupID, "b.txt", now), // object deleted as a duplicate
		contentBlobID: &blobID,
	}, int64(len(payload)))

	url, _, err := h.svc.GetDownloadURL(h.ctx, h.tenant, dupID, 60)
	require.NoError(t, err)
	code, body := httpGet(t, url)
	require.Equal(t, http.StatusOK, code, "download must follow the session's content_blob_id")
	require.Equal(t, payload, body)
}

// Sessions completed before migration 000107 (and before 000095's
// content_blob_id) carry neither a persisted key nor a blob reference.
// Their key was derived from the month the legacy initiate ran in, which
// is the month of created_at.
func TestGetDownloadURL_LegacySessionUsesCreatedAtMonth(t *testing.T) {
	payload := []byte("completed by a pre-000107 storage build")
	createdAt := previousMonth()
	id := uuid.Must(uuid.NewV7())
	h := newHarness(t, harnessConfig{beforeMigration: func(h *completeHarness) {
		insertSession(t, h, seedSession{
			id: id, filename: "legacy.txt", status: "completed",
			createdAt: createdAt, expiresAt: createdAt.Add(time.Hour), legacyShape: true,
		}, int64(len(payload)))
		putObject(t, h, bucketName("us-east-1", "hot"), objectKeyAt(h.tenant, id, "legacy.txt", createdAt), payload)
	}})

	url, _, err := h.svc.GetDownloadURL(h.ctx, h.tenant, id, 60)
	require.NoError(t, err)
	code, body := httpGet(t, url)
	require.Equal(t, http.StatusOK, code, "legacy session must download from its created_at-month key")
	require.Equal(t, payload, body)
}

func TestAbort_DeletesInitiatedKey(t *testing.T) {
	h := newHarness(t, harnessConfig{})
	payload := []byte("abandoned upload from last month")
	createdAt := previousMonth()
	id := uuid.Must(uuid.NewV7())
	bucket := bucketName("us-east-1", "hot")
	key := objectKeyAt(h.tenant, id, "abandon.txt", createdAt)
	insertSession(t, h, seedSession{
		id: id, filename: "abandon.txt", status: "initiated",
		createdAt: createdAt, expiresAt: time.Now().UTC().Add(time.Hour),
		bucket: bucket, key: key,
	}, int64(len(payload)))
	putObject(t, h, bucket, key, payload)

	require.NoError(t, h.svc.AbortUpload(h.ctx, h.tenant, id))

	_, err := h.s3.GetObjectInfo(context.Background(), bucket, key)
	require.Error(t, err, "abort must delete the object at the key the PUT was signed for")
	var status string
	require.NoError(t, h.pool.QueryRow(h.ctx,
		`SELECT status FROM upload_sessions WHERE tenant_id=$1 AND id=$2`, h.tenant, id).Scan(&status))
	require.Equal(t, "failed", status)
}
